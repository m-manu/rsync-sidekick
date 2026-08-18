package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/bytesutil"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/m-manu/rsync-sidekick/v2/fmte"
	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
	"github.com/m-manu/rsync-sidekick/v2/lib"
	"github.com/m-manu/rsync-sidekick/v2/remote"
	"github.com/m-manu/rsync-sidekick/v2/service"
	"github.com/pkg/sftp"
)

const unixCommandLengthGuess = 200

func getSyncActionsWithProgress(runID string, sourceDirPath string, exclusions set.Set[string],
	destinationDirPath string, verbose bool, progressFrequency time.Duration,
	copyDuplicates bool, useReflink bool, archivePaths []string,
	onArchiveAction service.ArchiveActionFunc,
) ([]action.SyncAction, error) {
	return getSyncActionsWithProgressFS(runID, sourceDirPath, nil, exclusions,
		destinationDirPath, nil, verbose, progressFrequency,
		copyDuplicates, useReflink, archivePaths, onArchiveAction)
}

func getSyncActionsWithProgressFS(runID string, sourceDirPath string, sourceFS rsfs.FileSystem,
	exclusions set.Set[string], destinationDirPath string, destFS rsfs.FileSystem,
	verbose bool, progressFrequency time.Duration,
	copyDuplicates bool, useReflink bool, archivePaths []string,
	onArchiveAction service.ArchiveActionFunc,
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
	destWalkDone := make(chan struct{})
	go func() {
		defer wgDirScan.Done()
		defer close(destWalkDone)
		if destFS != nil {
			destinationFiles, destinationSize, destinationFilesErr = service.FindFilesFromDirectoryWithFS(destFS, destinationDirPath, exclusions, &scanDestCounter)
		} else {
			destinationFiles, destinationSize, destinationFilesErr = service.FindFilesFromDirectory(destinationDirPath, exclusions, &scanDestCounter)
		}
		atomic.StoreInt32(&destScanDone, 1)
	}()

	// Archive paths sit on the destination side, so walking them competes with the
	// destination scan but not with the source scan. Start as soon as the destination is
	// done and let it run alongside the (often much longer) source scan.
	var archiveWalks []service.ArchiveWalk
	var archiveWalkErr error
	var scanArchiveCounter, archiveScanDone int32
	var wgArchiveWalk sync.WaitGroup
	if len(archivePaths) > 0 {
		wgArchiveWalk.Add(1)
		go func() {
			defer wgArchiveWalk.Done()
			<-destWalkDone
			if destinationFilesErr == nil {
				archiveWalks, archiveWalkErr = service.WalkArchives(archivePaths, exclusions, destFS,
					&scanArchiveCounter)
			}
			atomic.StoreInt32(&archiveScanDone, 1)
		}()
	}
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
					if len(archivePaths) == 0 {
						fmte.Printf("Scanning files: %d at source%s, %d at destination%s...\n",
							atomic.LoadInt32(&scanSourceCounter), srcFinished,
							atomic.LoadInt32(&scanDestCounter), dstFinished)
						continue
					}
					archFinished := ""
					if atomic.LoadInt32(&archiveScanDone) == 1 {
						archFinished = " [FINISHED]"
					}
					fmte.Printf("Scanning files: %d at source%s, %d at destination%s, %d in archives%s...\n",
						atomic.LoadInt32(&scanSourceCounter), srcFinished,
						atomic.LoadInt32(&scanDestCounter), dstFinished,
						atomic.LoadInt32(&scanArchiveCounter), archFinished)
				}
			}
		}()
	}
	// Reporting outlives the source and destination scans: the archive walk deliberately
	// keeps running into the next phase.
	go func() {
		wgDirScan.Wait()
		wgArchiveWalk.Wait()
		close(scanDone)
	}()
	wgDirScan.Wait()
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
	// Digests computed while matching moves; reused by the archive scan below. Empty when
	// there were no candidates at destination, in which case that phase never ran.
	var knownOrphanDigests map[string]entity.FileDigest
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
			actions, savings, knownOrphanDigests, syncErr = service.ComputeSyncActionsWithFS(sourceFS, destFS,
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
			// Digests of unmatched orphans at source, computed on demand: only orphans
			// that some archive file matches on extension and size are ever hashed.
			digestFn := func(orphans []string) (map[string]entity.FileDigest, error) {
				return withDigestProgress(len(orphans), progressFrequency,
					func(counter *int32) (map[string]entity.FileDigest, error) {
						return service.BatchDigestsParallel(sourceFS, sourceDirPath, orphans, counter), nil
					})
			}
			// The walk started back during the destination scan; collect it now.
			wgArchiveWalk.Wait()
			if archiveWalkErr != nil {
				return nil, fmt.Errorf("error scanning archive paths: %+v", archiveWalkErr)
			}
			var archiveProgress service.ArchiveScanProgress
			atomic.StoreInt32(&archiveProgress.FilesFound, atomic.LoadInt32(&scanArchiveCounter))
			stopArchiveProgress := startArchiveScanProgress(&archiveProgress, progressFrequency)
			archiveActions, archiveErr := service.ScanArchivesForCopiesWithDigests(
				archiveWalks, unmatchedOrphans, knownOrphanDigests, digestFn, onArchiveAction,
				sourceFiles, destinationDirPath, useReflink, destFS,
				&archiveProgress)
			stopArchiveProgress()
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
	// Archive matches are applied as they are found, so an interrupted run keeps them.
	// Not in script mode, which needs the complete list, and not for a dry run.
	var appliedArchiveActions int
	var onArchiveAction service.ArchiveActionFunc
	if outputScriptPath == "" && !dryRun {
		onArchiveAction = newLocalArchiveActionStreamer(&appliedArchiveActions)
	}
	actions, err := getSyncActionsWithProgress(runID, sourceDirPath, exclusions, destinationDirPath, verbose, progressFrequency,
		copyDuplicates, useReflink, archivePaths, onArchiveAction)
	if appliedArchiveActions > 0 {
		fmte.Printf("Applied %d actions from archive paths while scanning\n", appliedArchiveActions)
	}
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
		return rsyncSidekickRemoteExec(remoteLoc, remotePath, localPath, sourceIsRemote, agentClient, exclusions, outputScriptPath, verbose, dryRun, syncDirTimestamps, progressFrequency, copyDuplicates, useReflink, archivePaths)
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
		_ = sshCmd.Process.Kill()
		_ = sshCmd.Wait()
		return fmt.Errorf("SFTP connection failed: %w", err)
	}
	defer func() {
		sftpClient.Close()
		sshStdin.Close()
		_ = sshCmd.Wait()
	}()

	sftpFS := rsfs.NewSFTPFS(sftpClient)
	defer sftpFS.Close()

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

	// Stream archive matches only when the destination is local; over SFTP each action
	// would be its own round-trip.
	var appliedArchiveActions int
	var onArchiveAction service.ArchiveActionFunc
	if destFS == nil && outputScriptPath == "" && !dryRun {
		onArchiveAction = newLocalArchiveActionStreamer(&appliedArchiveActions)
	}
	actions, actionsErr := getSyncActionsWithProgressFS(runID, sourceDirPath, sourceFS,
		exclusions, destDirPath, destFS, verbose, progressFrequency,
		copyDuplicates, useReflink, archivePaths, onArchiveAction)
	if appliedArchiveActions > 0 {
		fmte.Printf("Applied %d actions from archive paths while scanning\n", appliedArchiveActions)
	}
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
func rsyncSidekickRemoteExec(remoteLoc remote.Location, remotePath, localPath string, sourceIsRemote bool, agentClient *remote.AgentClient, exclusions set.Set[string], outputScriptPath string, verbose, dryRun, syncDirTimestamps bool, progressFrequency time.Duration, copyDuplicates, useReflink bool, archivePaths []string) error {
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
			sourceFiles, sourceDirs, sourceSize, sourceFilesErr = agentClient.Walk(sourceDirPath, excludedNames, &remoteScanCounter, intervalMs, rsfs.DefaultOneFileSystem)
			atomic.StoreInt32(&remoteScanDone, 1)
		} else {
			sourceFiles, sourceSize, sourceFilesErr = service.FindFilesFromDirectory(sourceDirPath, exclusions, &localScanCounter)
			if sourceFilesErr == nil && syncDirTimestamps {
				sourceDirs, sourceFilesErr = service.FindDirsFromDirectory(sourceDirPath, exclusions)
			}
			atomic.StoreInt32(&localScanDone, 1)
		}
	}()
	destWalkDone := make(chan struct{})
	go func() {
		defer wgDirScan.Done()
		defer close(destWalkDone)
		if sourceIsRemote {
			destinationFiles, destinationSize, destinationFilesErr = service.FindFilesFromDirectory(destDirPath, exclusions, &localScanCounter)
			if destinationFilesErr == nil && syncDirTimestamps {
				destDirs, destinationFilesErr = service.FindDirsFromDirectory(destDirPath, exclusions)
			}
			atomic.StoreInt32(&localScanDone, 1)
		} else {
			destinationFiles, destDirs, destinationSize, destinationFilesErr = agentClient.Walk(destDirPath, excludedNames, &remoteScanCounter, intervalMs, rsfs.DefaultOneFileSystem)
			atomic.StoreInt32(&remoteScanDone, 1)
		}
	}()

	// Archive paths are on the destination side, so their walk competes with the
	// destination scan but not with the source scan: start it as soon as the destination
	// is done and let it overlap the source scan, which is usually the long one.
	//
	// With a remote destination the walk goes through the agent and can still be running
	// when the next phase sends its digest requests. That only works on a connection that
	// correlates messages by request ID.
	archivesAreLocal := sourceIsRemote
	prewalkArchives := len(archivePaths) > 0 && (archivesAreLocal || agentClient.IsConcurrent())
	var archiveWalks []service.ArchiveWalk
	var archiveWalkErr error
	var scanArchiveCounter, archiveScanDone int32
	var wgArchiveWalk sync.WaitGroup
	if prewalkArchives {
		wgArchiveWalk.Add(1)
		go func() {
			defer wgArchiveWalk.Done()
			<-destWalkDone
			if destinationFilesErr == nil {
				if archivesAreLocal {
					archiveWalks, archiveWalkErr = service.WalkArchives(archivePaths, exclusions, nil,
						&scanArchiveCounter)
				} else {
					archiveWalks, archiveWalkErr = walkArchivesViaAgent(agentClient, archivePaths,
						excludedNames, &scanArchiveCounter, intervalMs)
				}
			}
			atomic.StoreInt32(&archiveScanDone, 1)
		}()
	}
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
					archives := ""
					if prewalkArchives {
						archFinished := ""
						if atomic.LoadInt32(&archiveScanDone) == 1 {
							archFinished = " [FINISHED]"
						}
						archives = fmt.Sprintf(", %d in archives (local)%s",
							atomic.LoadInt32(&scanArchiveCounter), archFinished)
					}
					if sourceIsRemote {
						fmte.Printf("Scanning files: %d at source (remote)%s, %d at destination (local)%s%s...\n",
							atomic.LoadInt32(&remoteScanCounter), remoteFinished,
							atomic.LoadInt32(&localScanCounter), localFinished, archives)
					} else {
						fmte.Printf("Scanning files: %d at source (local)%s, %d at destination (remote)%s%s...\n",
							atomic.LoadInt32(&localScanCounter), localFinished,
							atomic.LoadInt32(&remoteScanCounter), remoteFinished, archives)
					}
				}
			}
		}()
	}
	// Reporting outlives the source and destination scans: the archive walk deliberately
	// keeps running into the next phase.
	go func() {
		wgDirScan.Wait()
		wgArchiveWalk.Wait()
		close(scanDone)
	}()
	wgDirScan.Wait()
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
	// Digests computed while matching moves; reused by the archive scan below. Empty when
	// there were no candidates at destination, in which case that phase never ran.
	var knownOrphanDigests map[string]entity.FileDigest
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
					remoteDigests, remoteDigestErr = agentClient.BatchDigest(sourceDirPath, remoteOrphans, &remoteCounter, intervalMs)
				} else if !sourceIsRemote && len(remoteCandiates) > 0 {
					remoteDigests, remoteDigestErr = agentClient.BatchDigest(destDirPath, remoteCandiates, &remoteCounter, intervalMs)
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
			var orphanDigests, candidateDigests map[string]entity.FileDigest
			if sourceIsRemote {
				orphanDigests = remoteDigests
				candidateDigests = localDigests
			} else {
				orphanDigests = localDigests
				candidateDigests = remoteDigests
			}
			knownOrphanDigests = orphanDigests

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
			// Digests of unmatched orphans at source, computed on demand: only orphans
			// that some archive file matches on extension and size are ever hashed.
			digestFn := func(orphans []string) (map[string]entity.FileDigest, error) {
				return withDigestProgress(len(orphans), progressFrequency,
					func(counter *int32) (map[string]entity.FileDigest, error) {
						if sourceIsRemote {
							return agentClient.BatchDigest(sourceDirPath, orphans, counter, progressFrequency.Milliseconds())
						}
						return service.BatchDigestsParallel(nil, sourceDirPath, orphans, counter), nil
					})
			}
			if sourceIsRemote {
				// Dest is local: the walk already ran alongside the source scan.
				wgArchiveWalk.Wait()
				if archiveWalkErr != nil {
					return fmt.Errorf("error scanning archive paths: %+v", archiveWalkErr)
				}
				// The destination is local, so matches can be applied as they are found.
				var appliedArchiveActions int
				var onArchiveAction service.ArchiveActionFunc
				if outputScriptPath == "" && !dryRun {
					onArchiveAction = newLocalArchiveActionStreamer(&appliedArchiveActions)
				}
				var archiveProgress service.ArchiveScanProgress
				atomic.StoreInt32(&archiveProgress.FilesFound, atomic.LoadInt32(&scanArchiveCounter))
				stopArchiveProgress := startArchiveScanProgress(&archiveProgress, progressFrequency)
				archiveActions, archiveErr := service.ScanArchivesForCopiesWithDigests(
					archiveWalks, unmatchedOrphans, knownOrphanDigests, digestFn, onArchiveAction,
					sourceFiles, destDirPath, useReflink, nil,
					&archiveProgress)
				stopArchiveProgress()
				if appliedArchiveActions > 0 {
					fmte.Printf("Applied %d actions from archive paths while scanning\n", appliedArchiveActions)
				}
				if archiveErr != nil {
					return fmt.Errorf("error scanning archives: %+v", archiveErr)
				}
				if len(archiveActions) > 0 {
					fmte.Printf("Found %d additional actions from archive paths\n", len(archiveActions))
					actions = append(actions, archiveActions...)
				}
			} else {
				// Dest is remote: scan archives via agent. The walk either already ran
				// alongside the source scan, or has to happen now.
				wgArchiveWalk.Wait()
				if archiveWalkErr != nil {
					return fmt.Errorf("error scanning archive paths: %+v", archiveWalkErr)
				}
				if !prewalkArchives {
					archiveWalks, archiveWalkErr = walkArchivesViaAgent(agentClient, archivePaths,
						excludedNames, &scanArchiveCounter, intervalMs)
					if archiveWalkErr != nil {
						return fmt.Errorf("error scanning archive paths: %+v", archiveWalkErr)
					}
				}
				archiveActions, archiveErr := scanArchivesViaAgent(agentClient, archiveWalks,
					unmatchedOrphans, knownOrphanDigests, digestFn, sourceFiles, destDirPath, useReflink,
					progressFrequency)
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

// newLocalArchiveActionStreamer applies each archive match immediately instead of
// collecting it for the end, so an interrupted run keeps every copy it already made. The
// count of applied actions is reported through applied.
//
// Only for a local destination: there a copy is a reflink or a plain local write, cheap
// enough to do one at a time. With a remote destination each action would be its own
// round-trip over the agent connection, where batching at the end is the better trade.
func newLocalArchiveActionStreamer(applied *int) service.ArchiveActionFunc {
	return func(a action.SyncAction) error {
		if err := a.Perform(); err != nil {
			return fmt.Errorf("error performing \"%s\": %w", a.UnixCommand(), err)
		}
		*applied++
		fmte.PrintfV("Performed: %s\n", a.UnixCommand())
		return nil
	}
}

// startArchiveScanProgress reports the archive scan counters until the returned stop
// function is called.
func startArchiveScanProgress(progress *service.ArchiveScanProgress,
	progressFrequency time.Duration,
) (stop func()) {
	if progressFrequency <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(progressFrequency)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				// Archives are always on the destination side; the orphan digests running
				// alongside are on the source side. Label both so the two interleaved
				// progress lines are telling apart at a glance.
				fmte.Printf("DST: Scanning archives: %d files found, %d checked, %d / %d hashed, %d matched...\n",
					atomic.LoadInt32(&progress.FilesFound),
					atomic.LoadInt32(&progress.FilesChecked),
					atomic.LoadInt32(&progress.FilesHashed),
					atomic.LoadInt32(&progress.FilesToHash),
					atomic.LoadInt32(&progress.Matches))
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
}

// withDigestProgress runs compute, reporting how many of count files are hashed so far.
// The counter it hands to compute is what drives that output.
func withDigestProgress(count int, progressFrequency time.Duration,
	compute func(counter *int32) (map[string]entity.FileDigest, error),
) (map[string]entity.FileDigest, error) {
	fmte.Printf("SRC: Computing digests of %d orphan candidate(s)...\n", count)
	var counter int32
	done := make(chan struct{})
	if progressFrequency > 0 {
		go func() {
			ticker := time.NewTicker(progressFrequency)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					fmte.Printf("SRC: Computing orphan digests: %d / %d...\n",
						atomic.LoadInt32(&counter), count)
				}
			}
		}()
	}
	digests, err := compute(&counter)
	close(done)
	return digests, err
}

// batchDigestLocal hashes the local side of a remote-exec run. It mirrors what the agent
// does for the remote side, so neither direction of a sync is slower than the other.
func batchDigestLocal(basePath string, files []string, counter *int32) (map[string]entity.FileDigest, error) {
	return service.BatchDigestsParallel(nil, basePath, files, counter), nil
}

// walkArchivesViaAgent walks archive paths on the remote destination, the counterpart of
// service.WalkArchives for a remote destination.
//
// counter reflects the path currently being walked, since the agent reports each walk's
// own count — with several archive paths it restarts per path.
func walkArchivesViaAgent(agentClient *remote.AgentClient, archivePaths []string,
	excludedNames []string, counter *int32, intervalMs int64,
) ([]service.ArchiveWalk, error) {
	walks := make([]service.ArchiveWalk, 0, len(archivePaths))
	for _, archivePath := range archivePaths {
		files, _, _, err := agentClient.Walk(archivePath, excludedNames, counter, intervalMs,
			rsfs.DefaultArchiveOneFileSystem)
		if err != nil {
			return nil, fmt.Errorf("error scanning archive %s via agent: %w", archivePath, err)
		}
		if len(files) == 0 {
			// Most likely a mistyped or unmounted path on the remote: worth saying out loud
			// rather than quietly finding no matches.
			fmte.PrintfErr("warning: archive path \"%s\" on remote holds no files - is the path correct?\n",
				archivePath)
			continue
		}
		walks = append(walks, service.ArchiveWalk{Path: archivePath, Files: files})
	}
	return walks, nil
}

// scanArchivesViaAgent scans archive paths on the remote destination via the agent,
// matching archive files against unmatched orphans by digest.
func scanArchivesViaAgent(agentClient *remote.AgentClient, archiveWalks []service.ArchiveWalk,
	unmatchedOrphans []string, knownOrphanDigests map[string]entity.FileDigest,
	digestFn service.OrphanDigestFunc,
	sourceFiles map[string]entity.FileMeta, destDirPath string, useReflink bool,
	progressFrequency time.Duration,
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

	// Local copy plus a cache of lazily computed digests; see ScanArchivesForCopiesWithDigests.
	orphanDigests := make(map[string]entity.FileDigest, len(knownOrphanDigests))
	for orphan, digest := range knownOrphanDigests {
		orphanDigests[orphan] = digest
	}
	requestedDigests := set.NewSet[string]()

	matchedOrphans := set.NewSet[string]()
	var actions []action.SyncAction
	uniqueness := set.NewSet[string]()

	// Progress counters: walk and hashing happen on the remote via the agent, while the
	// ext+size check and the orphan digests are local.
	var archiveWalkCounter, archiveCheckCounter, archiveDigestCounter int32
	intervalMs := progressFrequency.Milliseconds()
	archiveScanDone := make(chan struct{})
	if progressFrequency > 0 {
		go func() {
			ticker := time.NewTicker(progressFrequency)
			defer ticker.Stop()
			for {
				select {
				case <-archiveScanDone:
					return
				case <-ticker.C:
					fmte.Printf("DST: Scanning archives (remote): %d files found, %d checked, %d hashed...\n",
						atomic.LoadInt32(&archiveWalkCounter),
						atomic.LoadInt32(&archiveCheckCounter),
						atomic.LoadInt32(&archiveDigestCounter))
				}
			}
		}()
	}
	defer close(archiveScanDone)

	// This function only runs when the destination is the remote side, so digestFn hashes
	// the local source and can run alongside the agent requests below.

	for _, archiveWalk := range archiveWalks {
		archivePath, archiveFiles := archiveWalk.Path, archiveWalk.Files
		atomic.StoreInt32(&archiveWalkCounter, int32(len(archiveFiles)))

		// An archive file is only worth hashing if its extension and size match an orphan
		// that is still unmatched, and only those orphans need a digest.
		var candidates []string
		var neededOrphans []string
		for relPath, meta := range archiveFiles {
			atomic.AddInt32(&archiveCheckCounter, 1)
			k := orphanKey{ext: lib.GetFileExt(relPath), size: meta.Size}
			orphans, ok := orphansByKey[k]
			if !ok {
				continue
			}
			stillPending := false
			for _, orphan := range orphans {
				if matchedOrphans.Contains(orphan) {
					continue
				}
				stillPending = true
				if digestFn == nil || requestedDigests.Contains(orphan) {
					continue
				}
				if _, known := orphanDigests[orphan]; known {
					continue
				}
				requestedDigests.Add(orphan)
				neededOrphans = append(neededOrphans, orphan)
			}
			if stillPending {
				candidates = append(candidates, relPath)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		sort.Strings(candidates)
		sort.Strings(neededOrphans)

		// Both sides are independent, so hash them at the same time: the archive
		// candidates on the remote via the agent, the orphans wherever they live.
		var archiveDigests, freshOrphanDigests map[string]entity.FileDigest
		var archiveDigestErr, orphanDigestErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			archiveDigests, archiveDigestErr = agentClient.BatchDigest(archivePath, candidates,
				&archiveDigestCounter, intervalMs)
		}()
		go func() {
			defer wg.Done()
			if len(neededOrphans) > 0 {
				freshOrphanDigests, orphanDigestErr = digestFn(neededOrphans)
			}
		}()
		wg.Wait()
		if archiveDigestErr != nil {
			return nil, fmt.Errorf("error computing archive digests via agent: %w", archiveDigestErr)
		}
		if orphanDigestErr != nil {
			return nil, fmt.Errorf("error computing digests of %d orphan candidate(s): %w",
				len(neededOrphans), orphanDigestErr)
		}
		for orphan, digest := range freshOrphanDigests {
			orphanDigests[orphan] = digest
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
		candidateAtDestination := service.PickBestCandidate(candidates, orphanAtSource, sourceFiles)
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
				Type:        "move",
				BasePath:    act.BasePath,
				FromRelPath: act.RelativeFromPath,
				ToRelPath:   act.RelativeToPath,
			})
		case action.PropagateTimestampAction:
			specs = append(specs, remote.ActionSpec{
				Type:         "timestamp",
				DestBasePath: act.DestinationBaseDirPath,
				DestRelPath:  act.DestinationFileRelativePath,
				ModTimestamp: act.SourceModTime.Unix(),
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
				ModTimestamp: act.SourceModTime.Unix(),
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

// actionResult is sent from the I/O goroutine to the printer goroutine.
// Only lightweight data — no formatting, no string allocation on the hot path.
type actionResult struct {
	index  int
	action action.SyncAction
	err    error
	dryRun bool
}

func performActions(actions []action.SyncAction, destinationDirPath string, dryRun bool) error {
	if dryRun {
		fmte.Printf("Simulating sync actions at destination (dry run)...\n")
	} else {
		fmte.Printf("Applying sync actions at destination...\n")
	}
	successCount := 0
	movedPaths := make(map[string]string)

	action.SortByDestinationDir(actions)

	total := len(actions)
	prefixStrip := destinationDirPath + "/"

	// Printer goroutine — does all formatting and stdout I/O
	printCh := make(chan actionResult, 256)
	var printerDone sync.WaitGroup
	printerDone.Add(1)
	go func() {
		defer printerDone.Done()
		for r := range printCh {
			header := strings.Replace(
				fmt.Sprintf("%4d/%d %s: ", r.index+1, total, r.action),
				prefixStrip, "", -1,
			)
			if r.dryRun {
				fmt.Print(header, "skipping (dry run)\n")
			} else if r.err == nil {
				fmt.Print(header, "done\n")
			} else {
				fmt.Printf("%sfailed due to: %+v\n", header, r.err)
			}
		}
	}()

	// I/O loop — performs actions, sends lightweight results to printer
	start := time.Now()
	for i, syncAction := range actions {
		if copyAct, ok := syncAction.(action.CopyFileAction); ok {
			if newPath, wasMoved := movedPaths[copyAct.AbsSourcePath]; wasMoved {
				copyAct.AbsSourcePath = newPath
				syncAction = copyAct
			}
		}

		var aErr error
		if !dryRun {
			aErr = syncAction.Perform()
		}

		printCh <- actionResult{index: i, action: syncAction, err: aErr, dryRun: dryRun}

		if aErr == nil {
			successCount++
			if !dryRun {
				if moveAct, ok := syncAction.(action.MoveFileAction); ok {
					oldAbs := filepath.Join(moveAct.BasePath, moveAct.RelativeFromPath)
					newAbs := filepath.Join(moveAct.BasePath, moveAct.RelativeToPath)
					movedPaths[oldAbs] = newAbs
				}
			}
		}
	}
	close(printCh)
	printerDone.Wait()
	end := time.Now()

	if dryRun {
		fmte.Printf("Dry run completed in %.1fs: %d actions would be performed\n",
			end.Sub(start).Seconds(), successCount)
	} else {
		fmte.Printf("Sync completed in %.1fs: %d out of %d actions succeeded\n",
			end.Sub(start).Seconds(), successCount, total)
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
