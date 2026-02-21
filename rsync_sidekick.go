package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/action"
	"github.com/m-manu/rsync-sidekick/bytesutil"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/fmte"
	rsfs "github.com/m-manu/rsync-sidekick/fs"
	"github.com/m-manu/rsync-sidekick/lib"
	"github.com/m-manu/rsync-sidekick/remote"
	"github.com/m-manu/rsync-sidekick/service"
	"github.com/pkg/sftp"
)

const unixCommandLengthGuess = 200

func getSyncActionsWithProgress(runID string, sourceDirPath string, exclusions set.Set[string],
	destinationDirPath string, verbose bool, progressFrequency time.Duration,
	copyDuplicates bool, useReflink bool, archivePaths []string,
) ([]action.SyncAction, error) {
	return getSyncActionsWithProgressFS(runID, sourceDirPath, nil, exclusions,
		destinationDirPath, nil, verbose, progressFrequency,
		copyDuplicates, useReflink, archivePaths)
}

func getSyncActionsWithProgressFS(runID string, sourceDirPath string, sourceFS rsfs.FileSystem,
	exclusions set.Set[string], destinationDirPath string, destFS rsfs.FileSystem,
	verbose bool, progressFrequency time.Duration,
	copyDuplicates bool, useReflink bool, archivePaths []string,
) ([]action.SyncAction, error) {
	if verbose {
		fmte.VerboseOn()
	}
	var start, end time.Time
	fmte.Printf("Scanning source (%s) and destination (%s) directories...\n", sourceDirPath, destinationDirPath)
	start = time.Now()
	var sourceFiles, destinationFiles map[string]entity.FileMeta
	var sourceSize, destinationSize int64
	var sourceFilesErr, destinationFilesErr error
	var scanSourceCounter, scanDestCounter int32
	var sourceScanDone, destScanDone int32
	var wgDirScan sync.WaitGroup
	wgDirScan.Add(2)
	go func() {
		defer wgDirScan.Done()
		if sourceFS != nil {
			sourceFiles, sourceSize, sourceFilesErr = service.FindFilesFromDirectoryWithFS(sourceFS, sourceDirPath, exclusions, &scanSourceCounter)
		} else {
			sourceFiles, sourceSize, sourceFilesErr = service.FindFilesFromDirectory(sourceDirPath, exclusions, &scanSourceCounter)
		}
		atomic.StoreInt32(&sourceScanDone, 1)
	}()
	go func() {
		defer wgDirScan.Done()
		if destFS != nil {
			destinationFiles, destinationSize, destinationFilesErr = service.FindFilesFromDirectoryWithFS(destFS, destinationDirPath, exclusions, &scanDestCounter)
		} else {
			destinationFiles, destinationSize, destinationFilesErr = service.FindFilesFromDirectory(destinationDirPath, exclusions, &scanDestCounter)
		}
		atomic.StoreInt32(&destScanDone, 1)
	}()
	scanDone := make(chan struct{})
	if progressFrequency > 0 {
		go func() {
			ticker := time.NewTicker(progressFrequency)
			defer ticker.Stop()
			for {
				select {
				case <-scanDone:
					return
				case <-ticker.C:
					srcFinished := ""
					if atomic.LoadInt32(&sourceScanDone) == 1 {
						srcFinished = " [FINISHED]"
					}
					dstFinished := ""
					if atomic.LoadInt32(&destScanDone) == 1 {
						dstFinished = " [FINISHED]"
					}
					fmte.Printf("Scanning files: %d at source%s, %d at destination%s...\n",
						atomic.LoadInt32(&scanSourceCounter), srcFinished,
						atomic.LoadInt32(&scanDestCounter), dstFinished)
				}
			}
		}()
	}
	wgDirScan.Wait()
	close(scanDone)
	end = time.Now()
	if sourceFilesErr != nil {
		return nil, fmt.Errorf("error scanning source directory: %+v", sourceFilesErr)
	}
	if destinationFilesErr != nil {
		return nil, fmt.Errorf("error scanning destination directory: %+v", destinationFilesErr)
	}
	fmte.Printf("Found %d files (total size %s) at source and %d files (total size %s) at destination in %.1fs\n",
		len(sourceFiles), bytesutil.BinaryFormat(sourceSize), len(destinationFiles),
		bytesutil.BinaryFormat(destinationSize), end.Sub(start).Seconds())
	fmte.Printf("Finding files at source that don't have counterparts at destination...\n")
	orphansAtSource := service.FindOrphans(sourceFiles, destinationFiles)
	if len(orphansAtSource) == 0 {
		fmte.Printf("All files at source directory have counterparts. So, no action needed 🙂!\n")
		return []action.SyncAction{}, nil
	}
	sort.Strings(orphansAtSource)
	fmte.Printf("Found %d files\n", len(orphansAtSource))
	if verbose {
		lib.WriteSliceToFile(orphansAtSource, fmt.Sprintf("./info_%s_orphans_at_source.txt", runID))
	}
	fmte.Printf("Finding candidates at destination...\n")
	candidatesAtDestination := findCandidatesAtDestination(sourceFiles, destinationFiles, orphansAtSource)
	var actions []action.SyncAction
	if len(candidatesAtDestination) == 0 {
		fmte.Printf("No candidates found. Looks like all %d files are new.\n", len(orphansAtSource))
	} else {
		sort.Strings(candidatesAtDestination)
		if verbose {
			lib.WriteSliceToFile(candidatesAtDestination,
				fmt.Sprintf("./info_%s_candidates_at_destination.txt", runID),
			)
		}
		fmte.Printf("Found %d candidates.\n", len(candidatesAtDestination))
		fmte.Printf("Identifying file renames/movements and timestamp changes...\n")
		start = time.Now()
		var savings int64
		var syncErr error
		var sourceCounter, destinationCounter int32
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			actions, savings, syncErr = service.ComputeSyncActionsWithFS(sourceFS, destFS,
				sourceDirPath, sourceFiles, orphansAtSource,
				destinationDirPath, destinationFiles, candidatesAtDestination, &sourceCounter, &destinationCounter,
				copyDuplicates, useReflink)
		}()
		go func() {
			defer wg.Done()
			reportProgress(&sourceCounter, int32(len(orphansAtSource)),
				&destinationCounter, int32(len(candidatesAtDestination)),
				progressFrequency,
			)
		}()
		wg.Wait()
		end = time.Now()
		if syncErr != nil {
			return nil, fmt.Errorf("error while computing sync actions: %+v", syncErr)
		}
		fmte.Printf("Completed in %.1fs\n", end.Sub(start).Seconds())
		if len(actions) > 0 {
			fmte.Printf("Found %d actions that can save you %s of files transfer!\n",
				len(actions), bytesutil.BinaryFormat(savings))
		}
	}

	// Archive scanning (independent of --copy-duplicates)
	if len(archivePaths) > 0 {
		// Determine which orphans are still unmatched
		resolvedOrphans := set.NewSet[string]()
		for _, a := range actions {
			switch act := a.(type) {
			case action.MoveFileAction:
				resolvedOrphans.Add(act.RelativeToPath)
			case action.CopyFileAction:
				// Extract relative path from absolute dest path
				if len(act.AbsDestPath) > len(destinationDirPath)+1 {
					resolvedOrphans.Add(act.AbsDestPath[len(destinationDirPath)+1:])
				}
			}
		}
		var unmatchedOrphans []string
		for _, o := range orphansAtSource {
			if !resolvedOrphans.Contains(o) {
				unmatchedOrphans = append(unmatchedOrphans, o)
			}
		}
		if len(unmatchedOrphans) > 0 {
			fmte.Printf("Scanning %d archive path(s) for %d unmatched orphans...\n",
				len(archivePaths), len(unmatchedOrphans))
			// Compute digests for unmatched orphans at source
			orphanDigests := make(map[string]entity.FileDigest)
			for _, o := range unmatchedOrphans {
				absPath := sourceDirPath + "/" + o
				var digest entity.FileDigest
				var err error
				if sourceFS != nil {
					digest, err = service.GetDigestWithFS(sourceFS, absPath)
				} else {
					digest, err = service.GetDigest(absPath)
				}
				if err == nil {
					orphanDigests[o] = digest
				}
			}
			archiveActions, archiveErr := service.ScanArchivesForCopiesWithDigests(
				archivePaths, exclusions, unmatchedOrphans, orphanDigests,
				sourceFiles, destinationDirPath, useReflink, destFS)
			if archiveErr != nil {
				return nil, fmt.Errorf("error scanning archives: %+v", archiveErr)
			}
			if len(archiveActions) > 0 {
				fmte.Printf("Found %d additional actions from archive paths\n", len(archiveActions))
				actions = append(actions, archiveActions...)
			}
		}
	}

	if len(actions) == 0 {
		fmte.Printf("No sync actions found. You may run rsync.\n")
		return []action.SyncAction{}, nil
	}
	return actions, nil
}

func rsyncSidekick(runID string, sourceDirPath string, exclusions set.Set[string], destinationDirPath string,
	outputScriptPath string, verbose bool, dryRun bool, syncDirTimestamps bool, progressFrequency time.Duration,
	copyDuplicates bool, useReflink bool, archivePaths []string,
) error {
	actions, err := getSyncActionsWithProgress(runID, sourceDirPath, exclusions, destinationDirPath, verbose, progressFrequency,
		copyDuplicates, useReflink, archivePaths)
	if err != nil {
		return err // no extra info needed
	}
	if syncDirTimestamps {
		dirActions, dirErr := computeDirTimestampActions(sourceDirPath, nil, exclusions, destinationDirPath, nil)
		if dirErr != nil {
			return dirErr
		}
		actions = append(actions, dirActions...)
	}
	if len(actions) == 0 {
		return nil
	}
	if outputScriptPath != "" {
		return generateScript(actions, outputScriptPath, nil)
	} else {
		return performActions(actions, destinationDirPath, dryRun)
	}
}

// rsyncSidekickRemote handles the remote sync flow.
// sourceIsRemote: true if source is on the remote host, false if destination is remote.
func rsyncSidekickRemote(runID string, remoteLoc remote.Location, localPath string,
	sourceIsRemote bool, sshKeyPath string, agentClient *remote.AgentClient,
	exclusions set.Set[string], outputScriptPath string,
	verbose bool, dryRun bool, syncDirTimestamps bool, progressFrequency time.Duration,
	copyDuplicates bool, useReflink bool, archivePaths []string,
) error {
	remotePath := remoteLoc.Path

	if agentClient != nil {
		return rsyncSidekickRemoteExec(runID, remoteLoc, remotePath, localPath,
			sourceIsRemote, agentClient, exclusions, outputScriptPath,
			verbose, dryRun, syncDirTimestamps, progressFrequency,
			copyDuplicates, useReflink, archivePaths)
	}

	// SFTP mode: launch ssh with -s sftp subsystem and pipe through sftp client
	sshCmd := remote.SSHSubsystemCommand(remoteLoc, sshKeyPath, "sftp")
	sshCmd.Stderr = os.Stderr

	sshStdin, err := sshCmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("SFTP stdin pipe failed: %w", err)
	}
	sshStdout, err := sshCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("SFTP stdout pipe failed: %w", err)
	}
	if err := sshCmd.Start(); err != nil {
		return fmt.Errorf("SFTP ssh command failed: %w", err)
	}

	sftpClient, err := sftp.NewClientPipe(sshStdout, sshStdin)
	if err != nil {
		sshCmd.Process.Kill()
		sshCmd.Wait()
		return fmt.Errorf("SFTP connection failed: %w", err)
	}
	defer func() {
		sftpClient.Close()
		sshStdin.Close()
		sshCmd.Wait()
	}()

	sftpFS := rsfs.NewSFTPFS(sftpClient)

	var sourceFS, destFS rsfs.FileSystem
	var sourceDirPath, destDirPath string
	if sourceIsRemote {
		sourceFS = sftpFS
		sourceDirPath = remotePath
		destDirPath = localPath
	} else {
		destFS = sftpFS
		sourceDirPath = localPath
		destDirPath = remotePath
	}

	actions, actionsErr := getSyncActionsWithProgressFS(runID, sourceDirPath, sourceFS,
		exclusions, destDirPath, destFS, verbose, progressFrequency,
		copyDuplicates, useReflink, archivePaths)
	if actionsErr != nil {
		return actionsErr
	}
	if syncDirTimestamps {
		dirActions, dirErr := computeDirTimestampActions(sourceDirPath, sourceFS, exclusions, destDirPath, destFS)
		if dirErr != nil {
			return dirErr
		}
		actions = append(actions, dirActions...)
	}
	if len(actions) == 0 {
		return nil
	}
	if outputScriptPath != "" {
		var sshSpec *string
		if destFS != nil {
			spec := remoteLoc.SSHSpec()
			sshSpec = &spec
		}
		return generateScript(actions, outputScriptPath, sshSpec)
	}
	return performActions(actions, destDirPath, dryRun)
}

// rsyncSidekickRemoteExec handles the remote-execution mode where the agent
// runs on the remote side.
func rsyncSidekickRemoteExec(runID string, remoteLoc remote.Location,
	remotePath, localPath string, sourceIsRemote bool,
	agentClient *remote.AgentClient, exclusions set.Set[string],
	outputScriptPath string, verbose bool, dryRun bool,
	syncDirTimestamps bool, progressFrequency time.Duration,
	copyDuplicates bool, useReflink bool, archivePaths []string,
) error {
	if verbose {
		fmte.VerboseOn()
	}

	// Convert exclusions to slice
	excludedNames := make([]string, 0, exclusions.Cardinality())
	exclusions.Each(func(s string) bool {
		excludedNames = append(excludedNames, s)
		return false
	})

	var start, end time.Time
	var sourceDirPath, destDirPath string
	if sourceIsRemote {
		sourceDirPath = remotePath
		destDirPath = localPath
	} else {
		sourceDirPath = localPath
		destDirPath = remotePath
	}

	fmte.Printf("Scanning source (%s) and destination (%s) directories...\n", sourceDirPath, destDirPath)
	start = time.Now()

	var sourceFiles, destinationFiles map[string]entity.FileMeta
	var sourceDirs, destDirs map[string]int64
	var sourceSize, destinationSize int64
	var sourceFilesErr, destinationFilesErr error
	var localScanCounter, remoteScanCounter int32
	var localScanDone, remoteScanDone int32
	intervalMs := progressFrequency.Milliseconds()
	var wgDirScan sync.WaitGroup
	wgDirScan.Add(2)

	go func() {
		defer wgDirScan.Done()
		if sourceIsRemote {
			sourceFiles, sourceDirs, sourceSize, sourceFilesErr = agentClient.Walk(sourceDirPath, excludedNames, &remoteScanCounter, intervalMs)
			atomic.StoreInt32(&remoteScanDone, 1)
		} else {
			sourceFiles, sourceSize, sourceFilesErr = service.FindFilesFromDirectory(sourceDirPath, exclusions, &localScanCounter)
			if sourceFilesErr == nil && syncDirTimestamps {
				sourceDirs, sourceFilesErr = service.FindDirsFromDirectory(sourceDirPath, exclusions)
			}
			atomic.StoreInt32(&localScanDone, 1)
		}
	}()
	go func() {
		defer wgDirScan.Done()
		if sourceIsRemote {
			destinationFiles, destinationSize, destinationFilesErr = service.FindFilesFromDirectory(destDirPath, exclusions, &localScanCounter)
			if destinationFilesErr == nil && syncDirTimestamps {
				destDirs, destinationFilesErr = service.FindDirsFromDirectory(destDirPath, exclusions)
			}
			atomic.StoreInt32(&localScanDone, 1)
		} else {
			destinationFiles, destDirs, destinationSize, destinationFilesErr = agentClient.Walk(destDirPath, excludedNames, &remoteScanCounter, intervalMs)
			atomic.StoreInt32(&remoteScanDone, 1)
		}
	}()
	scanDone := make(chan struct{})
	if progressFrequency > 0 {
		go func() {
			ticker := time.NewTicker(progressFrequency)
			defer ticker.Stop()
			for {
				select {
				case <-scanDone:
					return
				case <-ticker.C:
					localFinished := ""
					if atomic.LoadInt32(&localScanDone) == 1 {
						localFinished = " [FINISHED]"
					}
					remoteFinished := ""
					if atomic.LoadInt32(&remoteScanDone) == 1 {
						remoteFinished = " [FINISHED]"
					}
					if sourceIsRemote {
						fmte.Printf("Scanning files: %d at source (remote)%s, %d at destination (local)%s...\n",
							atomic.LoadInt32(&remoteScanCounter), remoteFinished,
							atomic.LoadInt32(&localScanCounter), localFinished)
					} else {
						fmte.Printf("Scanning files: %d at source (local)%s, %d at destination (remote)%s...\n",
							atomic.LoadInt32(&localScanCounter), localFinished,
							atomic.LoadInt32(&remoteScanCounter), remoteFinished)
					}
				}
			}
		}()
	}
	wgDirScan.Wait()
	close(scanDone)
	end = time.Now()

	if sourceFilesErr != nil {
		return fmt.Errorf("error scanning source directory: %+v", sourceFilesErr)
	}
	if destinationFilesErr != nil {
		return fmt.Errorf("error scanning destination directory: %+v", destinationFilesErr)
	}

	fmte.Printf("Found %d files (total size %s) at source and %d files (total size %s) at destination in %.1fs\n",
		len(sourceFiles), bytesutil.BinaryFormat(sourceSize), len(destinationFiles),
		bytesutil.BinaryFormat(destinationSize), end.Sub(start).Seconds())

	fmte.Printf("Finding files at source that don't have counterparts at destination...\n")
	orphansAtSource := service.FindOrphans(sourceFiles, destinationFiles)

	var actions []action.SyncAction
	if len(orphansAtSource) == 0 {
		fmte.Printf("All files at source directory have counterparts.\n")
	} else {
		sort.Strings(orphansAtSource)
		fmte.Printf("Found %d files\n", len(orphansAtSource))

		fmte.Printf("Finding candidates at destination...\n")
		candidatesAtDestination := findCandidatesAtDestination(sourceFiles, destinationFiles, orphansAtSource)
		if len(candidatesAtDestination) == 0 {
			fmte.Printf("No candidates found. Looks like all %d files are new. rsync will do the rest.\n", len(orphansAtSource))
		} else {
			sort.Strings(candidatesAtDestination)
			fmte.Printf("Found %d candidates.\n", len(candidatesAtDestination))

			// Compute digests via agent for the remote side
			fmte.Printf("Identifying file renames/movements and timestamp changes...\n")
			start = time.Now()

			var remoteOrphans, remoteCandiates []string
			var localOrphans, localCandidates []string
			if sourceIsRemote {
				remoteOrphans = orphansAtSource
				localCandidates = candidatesAtDestination
			} else {
				localOrphans = orphansAtSource
				remoteCandiates = candidatesAtDestination
			}

			// Hash remote files via agent, local files locally
			var remoteDigests, localDigests map[string]entity.FileDigest
			var remoteDigestErr, localDigestErr error
			var localCounter, remoteCounter int32
			var localTotal, remoteTotal int32
			if sourceIsRemote {
				remoteTotal = int32(len(remoteOrphans))
				localTotal = int32(len(localCandidates))
			} else {
				localTotal = int32(len(localOrphans))
				remoteTotal = int32(len(remoteCandiates))
			}
			var wgDigest sync.WaitGroup
			wgDigest.Add(2)

			go func() {
				defer wgDigest.Done()
				if sourceIsRemote && len(remoteOrphans) > 0 {
					remoteDigests, remoteDigestErr = agentClient.BatchDigest(sourceDirPath, remoteOrphans, &remoteCounter)
				} else if !sourceIsRemote && len(remoteCandiates) > 0 {
					remoteDigests, remoteDigestErr = agentClient.BatchDigest(destDirPath, remoteCandiates, &remoteCounter)
				}
			}()
			go func() {
				defer wgDigest.Done()
				if sourceIsRemote && len(localCandidates) > 0 {
					localDigests, localDigestErr = batchDigestLocal(destDirPath, localCandidates, &localCounter)
				} else if !sourceIsRemote && len(localOrphans) > 0 {
					localDigests, localDigestErr = batchDigestLocal(sourceDirPath, localOrphans, &localCounter)
				}
			}()
			if localTotal > 0 && remoteTotal > 0 {
				wgDigest.Add(1)
				go func() {
					defer wgDigest.Done()
					if sourceIsRemote {
						// remote = orphans (source), local = candidates (destination)
						reportProgress(&remoteCounter, remoteTotal, &localCounter, localTotal, progressFrequency)
					} else {
						// local = orphans (source), remote = candidates (destination)
						reportProgress(&localCounter, localTotal, &remoteCounter, remoteTotal, progressFrequency)
					}
				}()
			}
			wgDigest.Wait()

			if remoteDigestErr != nil {
				return fmt.Errorf("error computing remote digests: %+v", remoteDigestErr)
			}
			if localDigestErr != nil {
				return fmt.Errorf("error computing local digests: %+v", localDigestErr)
			}

			// Build the orphan and candidate digest maps
			orphanDigests := make(map[string]entity.FileDigest)
			candidateDigests := make(map[string]entity.FileDigest)
			if sourceIsRemote {
				orphanDigests = remoteDigests
				candidateDigests = localDigests
			} else {
				orphanDigests = localDigests
				candidateDigests = remoteDigests
			}

			// Match digests and build actions
			actions = matchAndBuildActions(sourceDirPath, sourceFiles, orphansAtSource, orphanDigests,
				destDirPath, destinationFiles, candidatesAtDestination, candidateDigests,
				copyDuplicates, useReflink)

			end = time.Now()
			fmte.Printf("Completed in %.1fs\n", end.Sub(start).Seconds())

			if len(actions) == 0 {
				fmte.Printf("No sync actions found. You may run rsync.\n")
			} else {
				savings := int64(0)
				for _, a := range actions {
					if mfa, ok := a.(action.MoveFileAction); ok {
						if fm, exists := sourceFiles[mfa.RelativeToPath]; exists {
							savings += fm.Size
						}
					}
					if pta, ok := a.(action.PropagateTimestampAction); ok {
						if fm, exists := sourceFiles[pta.SourceFileRelativePath]; exists {
							savings += fm.Size
						}
					}
					if cfa, ok := a.(action.CopyFileAction); ok {
						// Extract relative path from absolute dest path
						if len(cfa.AbsDestPath) > len(destDirPath)+1 {
							relPath := cfa.AbsDestPath[len(destDirPath)+1:]
							if fm, exists := sourceFiles[relPath]; exists {
								savings += fm.Size
							}
						}
					}
				}
				fmte.Printf("Found %d actions that can save you %s of files transfer!\n",
					len(actions), bytesutil.BinaryFormat(savings))
			}
		}
	}

	if syncDirTimestamps && sourceDirs != nil && destDirs != nil {
		dirActions := computeDirTimestampActionsFromMaps(sourceDirPath, sourceDirs, destDirPath, destDirs)
		if len(dirActions) > 0 {
			fmte.Printf("Found %d directory timestamp actions\n", len(dirActions))
			actions = append(actions, dirActions...)
		}
	}

	// Archive scanning — archives are on the destination side
	if len(archivePaths) > 0 && len(orphansAtSource) > 0 {
		resolvedOrphans := set.NewSet[string]()
		for _, a := range actions {
			switch act := a.(type) {
			case action.MoveFileAction:
				resolvedOrphans.Add(act.RelativeToPath)
			case action.CopyFileAction:
				if len(act.AbsDestPath) > len(destDirPath)+1 {
					resolvedOrphans.Add(act.AbsDestPath[len(destDirPath)+1:])
				}
			}
		}
		var unmatchedOrphans []string
		for _, o := range orphansAtSource {
			if !resolvedOrphans.Contains(o) {
				unmatchedOrphans = append(unmatchedOrphans, o)
			}
		}
		if len(unmatchedOrphans) > 0 {
			fmte.Printf("Scanning %d archive path(s) for %d unmatched orphans...\n",
				len(archivePaths), len(unmatchedOrphans))
			// Compute digests for unmatched orphans at source
			var unmatchedDigests map[string]entity.FileDigest
			var digestErr error
			if sourceIsRemote {
				unmatchedDigests, digestErr = agentClient.BatchDigest(sourceDirPath, unmatchedOrphans, nil)
			} else {
				unmatchedDigests, digestErr = batchDigestLocal(sourceDirPath, unmatchedOrphans, nil)
			}
			if digestErr != nil {
				return fmt.Errorf("error computing orphan digests for archive scan: %+v", digestErr)
			}
			if sourceIsRemote {
				// Dest is local: scan archives locally
				archiveActions, archiveErr := service.ScanArchivesForCopiesWithDigests(
					archivePaths, exclusions, unmatchedOrphans, unmatchedDigests,
					sourceFiles, destDirPath, useReflink, nil)
				if archiveErr != nil {
					return fmt.Errorf("error scanning archives: %+v", archiveErr)
				}
				if len(archiveActions) > 0 {
					fmte.Printf("Found %d additional actions from archive paths\n", len(archiveActions))
					actions = append(actions, archiveActions...)
				}
			} else {
				// Dest is remote: scan archives via agent
				archiveActions, archiveErr := scanArchivesViaAgent(agentClient, archivePaths, excludedNames,
					unmatchedOrphans, unmatchedDigests, sourceFiles, destDirPath, useReflink)
				if archiveErr != nil {
					return fmt.Errorf("error scanning archives via agent: %+v", archiveErr)
				}
				if len(archiveActions) > 0 {
					fmte.Printf("Found %d additional actions from archive paths\n", len(archiveActions))
					actions = append(actions, archiveActions...)
				}
			}
		}
	}

	if len(actions) == 0 {
		return nil
	}

	if outputScriptPath != "" {
		var sshSpec *string
		if !sourceIsRemote {
			spec := remoteLoc.SSHSpec()
			sshSpec = &spec
		}
		return generateScript(actions, outputScriptPath, sshSpec)
	}

	// For remote-execution: actions on the remote side go through the agent
	if !sourceIsRemote {
		// Destination is remote: send actions to agent
		return performActionsViaAgent(agentClient, actions, destDirPath, dryRun)
	}

	// Source is remote, destination is local: perform locally
	return performActions(actions, destDirPath, dryRun)
}

func batchDigestLocal(basePath string, files []string, counter *int32) (map[string]entity.FileDigest, error) {
	digests := make(map[string]entity.FileDigest, len(files))
	for _, relPath := range files {
		absPath := fmt.Sprintf("%s/%s", basePath, relPath)
		digest, err := service.GetDigest(absPath)
		if counter != nil {
			atomic.AddInt32(counter, 1)
		}
		if err != nil {
			continue
		}
		digests[relPath] = digest
	}
	return digests, nil
}

// scanArchivesViaAgent scans archive paths on the remote destination via the agent,
// matching archive files against unmatched orphans by digest.
func scanArchivesViaAgent(agentClient *remote.AgentClient, archivePaths []string, excludedNames []string,
	unmatchedOrphans []string, orphanDigests map[string]entity.FileDigest,
	sourceFiles map[string]entity.FileMeta, destDirPath string, useReflink bool,
) ([]action.SyncAction, error) {
	// Build ext+size set from unmatched orphans
	type orphanKey struct {
		ext  string
		size int64
	}
	orphansByKey := make(map[orphanKey][]string)
	for _, o := range unmatchedOrphans {
		fm := sourceFiles[o]
		k := orphanKey{ext: lib.GetFileExt(o), size: fm.Size}
		orphansByKey[k] = append(orphansByKey[k], o)
	}

	matchedOrphans := set.NewSet[string]()
	var actions []action.SyncAction
	uniqueness := set.NewSet[string]()

	for _, archivePath := range archivePaths {
		// Walk archive via agent
		archiveFiles, _, _, walkErr := agentClient.Walk(archivePath, excludedNames, nil, 0)
		if walkErr != nil {
			return nil, fmt.Errorf("error scanning archive %s via agent: %w", archivePath, walkErr)
		}

		// Filter archive files by ext+size match with orphans
		var candidates []string
		for relPath, meta := range archiveFiles {
			k := orphanKey{ext: lib.GetFileExt(relPath), size: meta.Size}
			if _, ok := orphansByKey[k]; ok {
				candidates = append(candidates, relPath)
			}
		}
		if len(candidates) == 0 {
			continue
		}

		// Digest archive candidates via agent
		archiveDigests, digestErr := agentClient.BatchDigest(archivePath, candidates, nil)
		if digestErr != nil {
			return nil, fmt.Errorf("error computing archive digests via agent: %w", digestErr)
		}

		// Match orphans against archive digests
		for relPath, archiveDigest := range archiveDigests {
			archiveAbsPath := archivePath + "/" + relPath
			k := orphanKey{ext: lib.GetFileExt(relPath), size: archiveFiles[relPath].Size}
			orphans, ok := orphansByKey[k]
			if !ok {
				continue
			}
			for _, orphan := range orphans {
				if matchedOrphans.Contains(orphan) {
					continue
				}
				oDigest, ok := orphanDigests[orphan]
				if !ok {
					continue
				}
				if oDigest == archiveDigest {
					absDest := destDirPath + "/" + orphan
					parentDir := parentPath(orphan)
					mkdirAction := action.MakeDirectoryAction{
						AbsoluteDirPath: destDirPath + "/" + parentDir,
					}
					if !uniqueness.Contains(mkdirAction.Uniqueness()) {
						actions = append(actions, mkdirAction)
						uniqueness.Add(mkdirAction.Uniqueness())
					}
					copyAction := action.CopyFileAction{
						AbsSourcePath: archiveAbsPath,
						AbsDestPath:   absDest,
						SourceModTime: time.Unix(sourceFiles[orphan].ModifiedTimestamp, 0),
						UseReflink:    useReflink,
					}
					if !uniqueness.Contains(copyAction.Uniqueness()) {
						actions = append(actions, copyAction)
						uniqueness.Add(copyAction.Uniqueness())
						matchedOrphans.Add(orphan)
					}
				}
			}
		}
	}

	return actions, nil
}

func matchAndBuildActions(
	sourceDirPath string, sourceFiles map[string]entity.FileMeta,
	orphansAtSource []string, orphanDigests map[string]entity.FileDigest,
	destDirPath string, destinationFiles map[string]entity.FileMeta,
	candidatesAtDestination []string, candidateDigests map[string]entity.FileDigest,
	copyDuplicates bool, useReflink bool,
) []action.SyncAction {
	// Build reverse map: digest → candidate files
	candidateDigestToFiles := make(map[entity.FileDigest][]string)
	for _, f := range candidatesAtDestination {
		if d, ok := candidateDigests[f]; ok {
			candidateDigestToFiles[d] = append(candidateDigestToFiles[d], f)
		}
	}

	actions := make([]action.SyncAction, 0)
	uniqueness := set.NewSet[string]()
	usedCandidates := set.NewSet[string]()

	for _, orphanAtSource := range orphansAtSource {
		orphanDigest, ok := orphanDigests[orphanAtSource]
		if !ok {
			continue
		}
		candidates, hasCandidates := candidateDigestToFiles[orphanDigest]
		if !hasCandidates {
			continue
		}
		candidateAtDestination := service.PickBestCandidate(candidates, orphanAtSource, sourceFiles, usedCandidates)
		if candidateAtDestination == "" {
			continue
		}
		_, candidateExistsAtSource := sourceFiles[candidateAtDestination]
		if destinationFiles[candidateAtDestination].ModifiedTimestamp != sourceFiles[orphanAtSource].ModifiedTimestamp {
			if srcMetaForCandidate, existsAtSourceForCandidate := sourceFiles[candidateAtDestination]; !(existsAtSourceForCandidate && srcMetaForCandidate == destinationFiles[candidateAtDestination]) {
				timestampAction := action.PropagateTimestampAction{
					SourceBaseDirPath:           sourceDirPath,
					DestinationBaseDirPath:      destDirPath,
					SourceFileRelativePath:      orphanAtSource,
					DestinationFileRelativePath: candidateAtDestination,
					SourceModTime:               time.Unix(sourceFiles[orphanAtSource].ModifiedTimestamp, 0),
				}
				if !uniqueness.Contains(timestampAction.Uniqueness()) {
					actions = append(actions, timestampAction)
					uniqueness.Add(timestampAction.Uniqueness())
				}
			}
		}
		if !candidateExistsAtSource && candidateAtDestination != orphanAtSource {
			// Move: candidate doesn't exist at source, safe to move
			usedCandidates.Add(candidateAtDestination)
			parentDir := destDirPath + "/" + parentPath(orphanAtSource)
			directoryAction := action.MakeDirectoryAction{
				AbsoluteDirPath: parentDir,
			}
			if !uniqueness.Contains(directoryAction.Uniqueness()) {
				actions = append(actions, directoryAction)
				uniqueness.Add(directoryAction.Uniqueness())
			}
			moveFileAction := action.MoveFileAction{
				BasePath:         destDirPath,
				RelativeFromPath: candidateAtDestination,
				RelativeToPath:   orphanAtSource,
			}
			if !uniqueness.Contains(moveFileAction.Uniqueness()) {
				actions = append(actions, moveFileAction)
				uniqueness.Add(moveFileAction.Uniqueness())
			}
		} else if copyDuplicates && candidateExistsAtSource && candidateAtDestination != orphanAtSource {
			// Copy: candidate exists at source too, so we can't move it — copy instead
			absSource := destDirPath + "/" + candidateAtDestination
			absDest := destDirPath + "/" + orphanAtSource
			parentDir := destDirPath + "/" + parentPath(orphanAtSource)
			directoryAction := action.MakeDirectoryAction{
				AbsoluteDirPath: parentDir,
			}
			if !uniqueness.Contains(directoryAction.Uniqueness()) {
				actions = append(actions, directoryAction)
				uniqueness.Add(directoryAction.Uniqueness())
			}
			copyAction := action.CopyFileAction{
				AbsSourcePath: absSource,
				AbsDestPath:   absDest,
				SourceModTime: time.Unix(sourceFiles[orphanAtSource].ModifiedTimestamp, 0),
				UseReflink:    useReflink,
			}
			if !uniqueness.Contains(copyAction.Uniqueness()) {
				actions = append(actions, copyAction)
				uniqueness.Add(copyAction.Uniqueness())
			}
		}
	}
	return actions
}

// computeDirTimestampActions scans source and destination for directories and
// returns PropagateTimestampActions for directories that exist at both sides
// but have different modification times.
func computeDirTimestampActions(sourceDirPath string, sourceFS rsfs.FileSystem,
	exclusions set.Set[string], destDirPath string, destFS rsfs.FileSystem,
) ([]action.SyncAction, error) {
	var sourceDirs, destDirs map[string]int64
	var srcErr, dstErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if sourceFS != nil {
			sourceDirs, srcErr = service.FindDirsFromDirectoryWithFS(sourceFS, sourceDirPath, exclusions)
		} else {
			sourceDirs, srcErr = service.FindDirsFromDirectory(sourceDirPath, exclusions)
		}
	}()
	go func() {
		defer wg.Done()
		if destFS != nil {
			destDirs, dstErr = service.FindDirsFromDirectoryWithFS(destFS, destDirPath, exclusions)
		} else {
			destDirs, dstErr = service.FindDirsFromDirectory(destDirPath, exclusions)
		}
	}()
	wg.Wait()
	if srcErr != nil {
		return nil, fmt.Errorf("error scanning source directories: %w", srcErr)
	}
	if dstErr != nil {
		return nil, fmt.Errorf("error scanning destination directories: %w", dstErr)
	}
	return computeDirTimestampActionsFromMaps(sourceDirPath, sourceDirs, destDirPath, destDirs), nil
}

// computeDirTimestampActionsFromMaps builds PropagateTimestampActions for directories
// that exist at both source and destination but have different timestamps.
func computeDirTimestampActionsFromMaps(sourceDirPath string, sourceDirs map[string]int64,
	destDirPath string, destDirs map[string]int64,
) []action.SyncAction {
	actions := make([]action.SyncAction, 0)
	for relPath, srcModTime := range sourceDirs {
		dstModTime, exists := destDirs[relPath]
		if !exists || srcModTime == dstModTime {
			continue
		}
		actions = append(actions, action.PropagateTimestampAction{
			SourceBaseDirPath:           sourceDirPath,
			DestinationBaseDirPath:      destDirPath,
			SourceFileRelativePath:      relPath,
			DestinationFileRelativePath: relPath,
			SourceModTime:               time.Unix(srcModTime, 0),
		})
	}
	return actions
}

func parentPath(relPath string) string {
	for i := len(relPath) - 1; i >= 0; i-- {
		if relPath[i] == '/' {
			return relPath[:i]
		}
	}
	return "."
}

func performActionsViaAgent(agentClient *remote.AgentClient, actions []action.SyncAction, destDirPath string, dryRun bool) error {
	if dryRun {
		fmte.Printf("Simulating sync actions at destination (dry run)...\n")
	} else {
		fmte.Printf("Applying sync actions at destination via remote agent...\n")
	}

	specs := make([]remote.ActionSpec, 0, len(actions))
	for _, a := range actions {
		switch act := a.(type) {
		case action.MoveFileAction:
			specs = append(specs, remote.ActionSpec{
				Type:     "move",
				BasePath: act.BasePath,
				FromRelPath: act.RelativeFromPath,
				ToRelPath:   act.RelativeToPath,
			})
		case action.PropagateTimestampAction:
			specs = append(specs, remote.ActionSpec{
				Type:         "timestamp",
				DestBasePath: act.DestinationBaseDirPath,
				DestRelPath:  act.DestinationFileRelativePath,
				ModTimestamp:  act.SourceModTime.Unix(),
			})
		case action.MakeDirectoryAction:
			specs = append(specs, remote.ActionSpec{
				Type:    "mkdir",
				DirPath: act.AbsoluteDirPath,
			})
		case action.CopyFileAction:
			specs = append(specs, remote.ActionSpec{
				Type:         "copy",
				FromAbsPath:  act.AbsSourcePath,
				ToAbsPath:    act.AbsDestPath,
				ModTimestamp:  act.SourceModTime.Unix(),
				UseReflink:   act.UseReflink,
			})
		}
	}

	start := time.Now()
	results, err := agentClient.Perform(specs, dryRun)
	end := time.Now()
	if err != nil {
		return fmt.Errorf("remote perform failed: %w", err)
	}

	successCount := 0
	for i, r := range results {
		fmte.Print(strings.Replace(
			fmt.Sprintf("%4d/%d %s: ", i+1, len(actions), actions[i]),
			destDirPath+"/", "", -1,
		))
		if r.Success {
			if dryRun {
				fmte.Printf("skipping (dry run)\n")
			} else {
				fmte.Printf("done\n")
			}
			successCount++
		} else {
			fmte.Printf("failed due to: %s\n", r.Error)
		}
	}

	if dryRun {
		fmte.Printf("Dry run completed in %.1fs: %d actions would be performed\n",
			end.Sub(start).Seconds(), successCount)
	} else {
		fmte.Printf("Sync completed in %.1fs: %d out of %d actions succeeded\n",
			end.Sub(start).Seconds(), successCount, len(actions))
	}
	return nil
}

func performActions(actions []action.SyncAction, destinationDirPath string, dryRun bool) error {
	var start, end time.Time
	if dryRun {
		fmte.Printf("Simulating sync actions at destination (dry run)...\n")
	} else {
		fmte.Printf("Applying sync actions at destination...\n")
	}
	successCount := 0
	start = time.Now()
	for i, syncAction := range actions {
		fmte.Print(strings.Replace(
			fmt.Sprintf("%4d/%d %s: ", i+1, len(actions), syncAction),
			destinationDirPath+"/", "", -1,
		))
		if dryRun {
			fmte.Printf("skipping (dry run)\n")
			successCount++
		} else {
			aErr := syncAction.Perform()
			if aErr == nil {
				fmte.Printf("done\n")
				successCount++
			} else {
				fmte.Printf("failed due to: %+v\n", aErr)
			}
		}
	}
	end = time.Now()
	if dryRun {
		fmte.Printf("Dry run completed in %.1fs: %d actions would be performed\n",
			end.Sub(start).Seconds(), successCount)
	} else {
		fmte.Printf("Sync completed in %.1fs: %d out of %d actions succeeded\n",
			end.Sub(start).Seconds(), successCount, len(actions))
	}
	return nil
}

func generateScript(actions []action.SyncAction, shellScriptFileName string, remoteSSHSpec *string) error {
	fmte.Printf("Writing sync actions to shell script \"%s\"...\n", shellScriptFileName)
	shellScriptFile, shellScriptCreateErr := os.Create(shellScriptFileName)
	if shellScriptCreateErr != nil {
		return fmt.Errorf("couldn't create file '%s': %+v", shellScriptFileName, shellScriptCreateErr)
	}
	defer shellScriptFile.Close()
	permsErr := os.Chmod(shellScriptFileName, 0700)
	if permsErr != nil {
		return fmt.Errorf("couldn't change permissions on file '%s': %+v", shellScriptFileName, permsErr)
	}
	var sb strings.Builder
	sb.Grow(unixCommandLengthGuess * len(actions))
	for _, a := range actions {
		cmd := a.UnixCommand()
		if remoteSSHSpec != nil {
			// Wrap destination-side commands in ssh
			cmd = fmt.Sprintf(`ssh %s '%s'`, *remoteSSHSpec, strings.ReplaceAll(cmd, "'", "'\\''"))
		}
		sb.WriteString(cmd)
		sb.WriteString("\n")
	}
	_, errFC := shellScriptFile.WriteString(sb.String())
	if errFC != nil {
		return fmt.Errorf("couldn't write to file '%s': %+v", shellScriptFileName, errFC)
	}
	fmte.Printf("Done. You may run it now.\n")
	return nil
}

func reportProgress(sourceActual *int32, sourceExpected int32, destinationActual *int32, destinationExpected int32, reportingFrequency time.Duration) {
	var sourceProgress, destinationProgress float64
	time.Sleep(100 * time.Millisecond)
	for atomic.LoadInt32(sourceActual) < sourceExpected || atomic.LoadInt32(destinationActual) < destinationExpected {
		time.Sleep(reportingFrequency)
		sourceProgress = 100.0 * float64(atomic.LoadInt32(sourceActual)) / float64(sourceExpected)
		destinationProgress = 100.0 * float64(*destinationActual) / float64(destinationExpected)
		fmte.Printf("%.0f%% done at source and %.0f%% done at destination\n", sourceProgress, destinationProgress)
	}
}

func findCandidatesAtDestination(sourceFiles, destinationFiles map[string]entity.FileMeta, orphansAtSource []string) []string {
	orphansFileExtAndSizeMap := set.NewThreadUnsafeSetWithSize[entity.FileExtAndSize](len(orphansAtSource))
	for _, path := range orphansAtSource {
		fileMeta := sourceFiles[path]
		key := entity.FileExtAndSize{FileExtension: lib.GetFileExt(path), FileSize: fileMeta.Size}
		orphansFileExtAndSizeMap.Add(key)
	}
	candidatesAtDestination := make([]string, 0, len(orphansAtSource))
	for path, fileMeta := range destinationFiles {
		key := entity.FileExtAndSize{FileExtension: lib.GetFileExt(path), FileSize: fileMeta.Size}
		if orphansFileExtAndSizeMap.Contains(key) {
			candidatesAtDestination = append(candidatesAtDestination, path)
		}
	}
	return candidatesAtDestination
}
