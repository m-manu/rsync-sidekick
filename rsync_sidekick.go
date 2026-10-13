package main

import (
	"errors"
	"fmt"
	"os"
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
		copyDuplicates, useReflink, archivePaths, onArchiveAction, nil, nil)
}

// getSyncActionsWithProgressFS computes the sync actions. With an applier, the moves and
// copies found within the destination are applied chunk by chunk while hashing goes on —
// long before the archive scan — and are not returned again. moved is where the applier
// records its moves; the archive file lists, taken before, are updated from it.
func getSyncActionsWithProgressFS(runID string, sourceDirPath string, sourceFS rsfs.FileSystem,
	exclusions set.Set[string], destinationDirPath string, destFS rsfs.FileSystem,
	verbose bool, progressFrequency time.Duration,
	copyDuplicates bool, useReflink bool, archivePaths []string,
	onArchiveAction service.ArchiveActionFunc, applier destApplier, moved movedFiles,
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
	sourceWhere, destWhere := sideLocations(sourceFS != nil, destFS != nil)
	var scanSourceCounter, scanDestCounter int32
	var sourceScanDone, destScanDone int32
	var wgDirScan sync.WaitGroup
	wgDirScan.Add(2)
	go func() {
		defer wgDirScan.Done()
		sourceFiles, _, sourceSize, sourceFilesErr = walkIncluded(sourceDirPath, nil,
			filesWalk(sourceFS, exclusions, &scanSourceCounter, false))
		atomic.StoreInt32(&sourceScanDone, 1)
	}()
	destWalkDone := make(chan struct{})
	go func() {
		defer wgDirScan.Done()
		defer close(destWalkDone)
		destinationFiles, _, destinationSize, destinationFilesErr = walkIncluded(destinationDirPath,
			missingLocally(destFS), filesWalk(destFS, exclusions, &scanDestCounter, false))
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
				archiveWalks, archiveWalkErr = service.WalkArchivesKnowing(archivePaths, exclusions, destFS,
					&scanArchiveCounter, knownDestinationTree(destinationDirPath, destinationFiles))
			}
			atomic.StoreInt32(&archiveScanDone, 1)
		}()
	}
	stopScanProgress := progressBoard.Track(progressFrequency, "Scanning", func() []lib.ProgressPart {
		parts := []lib.ProgressPart{
			{Count: int64(atomic.LoadInt32(&scanSourceCounter)), Label: "src", Where: sourceWhere,
				Done: atomic.LoadInt32(&sourceScanDone) == 1},
			{Count: int64(atomic.LoadInt32(&scanDestCounter)), Label: "dst", Where: destWhere,
				Done: atomic.LoadInt32(&destScanDone) == 1},
		}
		if len(archivePaths) > 0 {
			parts = append(parts, lib.ProgressPart{Count: int64(atomic.LoadInt32(&scanArchiveCounter)),
				Label: "arch", Where: destWhere, Done: atomic.LoadInt32(&archiveScanDone) == 1})
		}
		return parts
	})
	// Reporting outlives the source and destination scans: the archive walk deliberately
	// keeps running into the next phase.
	go func() {
		wgDirScan.Wait()
		wgArchiveWalk.Wait()
		stopScanProgress()
	}()
	wgDirScan.Wait()
	end = time.Now()
	if sourceFilesErr != nil {
		return nil, fmt.Errorf("error scanning source directory: %+v", sourceFilesErr)
	}
	if destinationFilesErr != nil {
		return nil, fmt.Errorf("error scanning destination directory: %+v", destinationFilesErr)
	}
	fmte.Printf("Found %s files (total size %s) at source and %s files (total size %s) at destination in %.1fs\n",
		lib.GroupThousands(len(sourceFiles)), bytesutil.BinaryFormat(sourceSize), lib.GroupThousands(len(destinationFiles)),
		bytesutil.BinaryFormat(destinationSize), end.Sub(start).Seconds())
	fmte.Printf("Finding files at source that don't have counterparts at destination...\n")
	orphansAtSource := service.FindOrphans(sourceFiles, destinationFiles)
	if len(orphansAtSource) == 0 {
		fmte.Printf("All files at source directory have counterparts. So, no action needed 🙂!\n")
		return []action.SyncAction{}, nil
	}
	sort.Strings(orphansAtSource)
	fmte.Printf("Found %s files\n", lib.GroupThousands(len(orphansAtSource)))
	if verbose {
		lib.WriteSliceToFile(orphansAtSource, fmt.Sprintf("./info_%s_orphans_at_source.txt", runID))
	}
	fmte.Printf("Finding candidates at destination...\n")
	hashable := hashableOrphans(orphansAtSource, sourceFiles)
	candidatesAtDestination := findCandidatesAtDestination(sourceFiles, destinationFiles, hashable)
	var actions []action.SyncAction
	// Digests computed while matching moves; reused by the archive scan below. Empty when
	// there were no candidates at destination, in which case that phase never ran.
	var knownOrphanDigests map[string]entity.FileDigest
	if len(candidatesAtDestination) == 0 {
		fmte.Printf("No move candidates at destination: all %s files are new.\n", lib.GroupThousands(len(orphansAtSource)))
	} else {
		sort.Strings(candidatesAtDestination)
		if verbose {
			lib.WriteSliceToFile(candidatesAtDestination,
				fmt.Sprintf("./info_%s_candidates_at_destination.txt", runID),
			)
		}
		fmte.Printf("Found %s candidates.\n", lib.GroupThousands(len(candidatesAtDestination)))
		fmte.Printf("Identifying file renames/movements and timestamp changes...\n")
		start = time.Now()
		var sourceCounter, destinationCounter int32
		sourceWorkers, destinationWorkers := service.SideParallelism()
		matcher := service.NewDestMatcher(sourceDirPath, sourceFiles, destinationDirPath, destinationFiles, destFS,
			copyDuplicates, useReflink)
		var syncErr error
		actions, syncErr = streamDestinationPhase(matcher, hashable, candidatesAtDestination,
			func(chunk []string) (map[string]entity.FileDigest, error) {
				return service.BatchDigestsWorkers(sourceFS, sourceDirPath, chunk, &sourceCounter, sourceWorkers), nil
			},
			func(chunk []string) (map[string]entity.FileDigest, error) {
				return service.BatchDigestsWorkers(destFS, destinationDirPath, chunk, &destinationCounter, destinationWorkers), nil
			},
			&sourceCounter, &destinationCounter, applier, sourceWhere, destWhere, progressFrequency)
		end = time.Now()
		if syncErr != nil {
			return nil, fmt.Errorf("error while computing sync actions: %+v", syncErr)
		}
		knownOrphanDigests = matcher.OrphanDigests()
		fmte.Printf("Completed in %.1fs\n", end.Sub(start).Seconds())
		if len(actions) > 0 {
			fmte.Printf("Found %s actions that can save you %s of files transfer!\n",
				lib.GroupThousands(len(actions)), bytesutil.BinaryFormat(matcher.Savings()))
		}
	}

	// With an applier the destination actions already ran while hashing.
	destActions := actions
	if applier != nil {
		actions = nil
	}

	// Archive scanning (independent of --copy-duplicates)
	if len(archivePaths) > 0 {
		// Determine which orphans are still unmatched
		resolvedOrphans := set.NewSet[string]()
		for _, a := range destActions {
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
		for _, o := range hashable {
			if !resolvedOrphans.Contains(o) {
				unmatchedOrphans = append(unmatchedOrphans, o)
			}
		}
		if len(unmatchedOrphans) > 0 {
			fmte.Printf("Scanning %s archive path(s) for %s unmatched orphans%s...\n",
				lib.GroupThousands(len(archivePaths)), lib.GroupThousands(len(unmatchedOrphans)),
				skippedSmallNote(orphansAtSource, hashable))
			// Digests of unmatched orphans at source, computed on demand: only orphans
			// that some archive file matches on extension and size are ever hashed.
			digestFn := func(orphans []string) (map[string]entity.FileDigest, error) {
				return withDigestProgress(len(orphans), sourceWhere, progressFrequency,
					func(counter *int32) (map[string]entity.FileDigest, error) {
						return service.BatchDigestsParallel(sourceFS, sourceDirPath, orphans, counter), nil
					})
			}
			// The walk started back during the destination scan; collect it now.
			wgArchiveWalk.Wait()
			if archiveWalkErr != nil {
				return nil, fmt.Errorf("error scanning archive paths: %+v", archiveWalkErr)
			}
			archiveWalks = moved.rebaseWalks(archiveWalks)
			var archiveProgress service.ArchiveScanProgress
			atomic.StoreInt32(&archiveProgress.FilesFound, atomic.LoadInt32(&scanArchiveCounter))
			stopArchiveProgress := startArchiveScanProgress(&archiveProgress, destWhere, progressFrequency)
			archiveActions, archiveErr := service.ScanArchivesForCopiesWithDigests(
				archiveWalks, unmatchedOrphans, knownOrphanDigests, digestFn, onArchiveAction,
				sourceFiles, destinationDirPath, useReflink, destFS,
				&archiveProgress)
			stopArchiveProgress()
			if archiveErr != nil {
				return nil, fmt.Errorf("error scanning archives: %+v", archiveErr)
			}
			if len(archiveActions) > 0 {
				fmte.Printf("Found %s additional actions from archive paths\n", lib.GroupThousands(len(archiveActions)))
				actions = append(actions, archiveActions...)
			}
		}
	}

	if len(actions) == 0 {
		if len(destActions) == 0 {
			fmte.Printf("No sync actions found. You may run rsync.\n")
		}
		return []action.SyncAction{}, nil
	}
	return actions, nil
}

// streamDestinationPhase hashes orphans and candidates chunk by chunk, both sides at once,
// and hands each chunk's actions to applier right away (when there is one).
func streamDestinationPhase(matcher *service.DestMatcher, orphans, candidates []string,
	hashOrphans, hashCandidates service.ChunkDigestFunc, sourceDone, destinationDone *int32,
	applier destApplier, sourceWhere, destWhere string, progressFrequency time.Duration,
) ([]action.SyncAction, error) {
	stop := trackHashProgress(
		hashSide{done: sourceDone, total: len(orphans), label: "src", where: sourceWhere},
		hashSide{done: destinationDone, total: len(candidates), label: "dst", where: destWhere},
		progressFrequency)
	var onActions func([]action.SyncAction) error
	if applier != nil {
		onActions = applier.apply
	}
	actions, err := service.StreamSyncActions(matcher, orphans, candidates, hashOrphans, hashCandidates, onActions)
	stop()
	if applier != nil {
		applier.report()
	}
	return actions, err
}

func rsyncSidekick(runID string, sourceDirPath string, exclusions set.Set[string], destinationDirPath string,
	outputScriptPath string, verbose bool, dryRun bool, syncDirTimestamps bool, progressFrequency time.Duration,
	copyDuplicates bool, useReflink bool, archivePaths []string,
) error {
	// Archive matches are applied as they are found, so an interrupted run keeps them.
	// Not in script mode, which needs the complete list, and not for a dry run.
	moved := movedFiles{}
	var appliedArchiveActions int
	var onArchiveAction service.ArchiveActionFunc
	if outputScriptPath == "" && !dryRun {
		onArchiveAction = newLocalArchiveActionStreamer(&appliedArchiveActions, moved)
	}
	actions, err := getSyncActionsWithProgressFS(runID, sourceDirPath, nil, exclusions, destinationDirPath, nil,
		verbose, progressFrequency, copyDuplicates, useReflink, archivePaths, onArchiveAction,
		newDestApplier(outputScriptPath, destinationDirPath, dryRun, verbose, moved), moved)
	if appliedArchiveActions > 0 {
		fmte.Printf("Applied %s actions from archive paths while scanning\n", lib.GroupThousands(appliedArchiveActions))
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
		return performActionsTracked(actions, destinationDirPath, dryRun, verbose, progressFrequency, moved)
	}
}

// rsyncSidekickRemote handles the remote sync flow.
// sourceIsRemote: true if source is on the remote host, false if destination is remote.
func rsyncSidekickRemote(runID string, remoteLoc remote.Location, localPath string,
	sourceIsRemote bool, sshKeyPath string, agentClient *remote.AgentClient,
	exclusions set.Set[string], outputScriptPath string,
	verbose bool, dryRun bool, syncDirTimestamps bool, progressFrequency time.Duration,
	copyDuplicates bool, useReflink bool, archivePaths []string, plan copyPlanOutput,
) error {
	remotePath := remoteLoc.Path

	if agentClient != nil {
		return rsyncSidekickRemoteExec(remoteLoc, remotePath, localPath, sourceIsRemote, agentClient, exclusions, outputScriptPath, verbose, dryRun, syncDirTimestamps, progressFrequency, copyDuplicates, useReflink, archivePaths, plan)
	}
	if plan.enabled() {
		return fmt.Errorf("--copy-list and --plan-out need remote-execution mode (not SFTP)")
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
	moved := movedFiles{}
	var appliedArchiveActions int
	var onArchiveAction service.ArchiveActionFunc
	var applier destApplier
	if destFS == nil {
		if outputScriptPath == "" && !dryRun {
			onArchiveAction = newLocalArchiveActionStreamer(&appliedArchiveActions, moved)
		}
		applier = newDestApplier(outputScriptPath, destDirPath, dryRun, verbose, moved)
	}
	actions, actionsErr := getSyncActionsWithProgressFS(runID, sourceDirPath, sourceFS,
		exclusions, destDirPath, destFS, verbose, progressFrequency,
		copyDuplicates, useReflink, archivePaths, onArchiveAction, applier, moved)
	if appliedArchiveActions > 0 {
		fmte.Printf("Applied %s actions from archive paths while scanning\n", lib.GroupThousands(appliedArchiveActions))
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
	return performActionsTracked(actions, destDirPath, dryRun, verbose, progressFrequency, moved)
}

// rsyncSidekickRemoteExec handles the remote-execution mode where the agent
// runs on the remote side.
func rsyncSidekickRemoteExec(remoteLoc remote.Location, remotePath, localPath string, sourceIsRemote bool, agentClient *remote.AgentClient, exclusions set.Set[string], outputScriptPath string, verbose, dryRun, syncDirTimestamps bool, progressFrequency time.Duration, copyDuplicates, useReflink bool, archivePaths []string, plan copyPlanOutput) error {
	if verbose {
		fmte.VerboseOn()
	}
	if plan.enabled() && !sourceIsRemote {
		return fmt.Errorf("--copy-list and --plan-out need the source to be the remote side")
	}
	// Orphans that archive matches applied while scanning; those never come back as actions.
	archiveResolved := set.NewSet[string]()

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
	sourceWhere, destWhere := sideLocations(sourceIsRemote, !sourceIsRemote)
	var localScanCounter, remoteScanCounter int32
	var localScanDone, remoteScanDone int32
	intervalMs := progressFrequency.Milliseconds()
	var wgDirScan sync.WaitGroup
	wgDirScan.Add(2)

	go func() {
		defer wgDirScan.Done()
		if sourceIsRemote {
			sourceFiles, sourceDirs, sourceSize, sourceFilesErr = walkIncluded(sourceDirPath, nil,
				agentWalk(agentClient, excludedNames, &remoteScanCounter, intervalMs))
			atomic.StoreInt32(&remoteScanDone, 1)
		} else {
			sourceFiles, sourceDirs, sourceSize, sourceFilesErr = walkIncluded(sourceDirPath, nil,
				filesWalk(nil, exclusions, &localScanCounter, syncDirTimestamps))
			atomic.StoreInt32(&localScanDone, 1)
		}
	}()
	destWalkDone := make(chan struct{})
	go func() {
		defer wgDirScan.Done()
		defer close(destWalkDone)
		if sourceIsRemote {
			destinationFiles, destDirs, destinationSize, destinationFilesErr = walkIncluded(destDirPath,
				localDirMissing, filesWalk(nil, exclusions, &localScanCounter, syncDirTimestamps))
			atomic.StoreInt32(&localScanDone, 1)
		} else {
			destinationFiles, destDirs, destinationSize, destinationFilesErr = walkIncluded(destDirPath, nil,
				agentWalk(agentClient, excludedNames, &remoteScanCounter, intervalMs))
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
					archiveWalks, archiveWalkErr = service.WalkArchivesKnowing(archivePaths, exclusions, nil,
						&scanArchiveCounter, knownDestinationTree(destDirPath, destinationFiles))
				} else {
					archiveWalks, archiveWalkErr = walkArchivesViaAgent(agentClient, archivePaths,
						excludedNames, &scanArchiveCounter, intervalMs)
				}
			}
			atomic.StoreInt32(&archiveScanDone, 1)
		}()
	}
	stopScanProgress := progressBoard.Track(progressFrequency, "Scanning", func() []lib.ProgressPart {
		local := lib.ProgressPart{Count: int64(atomic.LoadInt32(&localScanCounter)), Label: "src",
			Where: "local", Done: atomic.LoadInt32(&localScanDone) == 1}
		remote := lib.ProgressPart{Count: int64(atomic.LoadInt32(&remoteScanCounter)), Label: "dst",
			Where: "remote", Done: atomic.LoadInt32(&remoteScanDone) == 1}
		parts := []lib.ProgressPart{local, remote}
		if sourceIsRemote {
			local.Label, remote.Label = "dst", "src"
			parts = []lib.ProgressPart{remote, local}
		}
		if prewalkArchives {
			parts = append(parts, lib.ProgressPart{Count: int64(atomic.LoadInt32(&scanArchiveCounter)),
				Label: "arch", Where: parts[1].Where, Done: atomic.LoadInt32(&archiveScanDone) == 1})
		}
		return parts
	})
	// Reporting outlives the source and destination scans: the archive walk deliberately
	// keeps running into the next phase.
	go func() {
		wgDirScan.Wait()
		wgArchiveWalk.Wait()
		stopScanProgress()
	}()
	wgDirScan.Wait()
	end = time.Now()

	if sourceFilesErr != nil {
		return fmt.Errorf("error scanning source directory: %+v", sourceFilesErr)
	}
	if destinationFilesErr != nil {
		return fmt.Errorf("error scanning destination directory: %+v", destinationFilesErr)
	}

	fmte.Printf("Found %s files (total size %s) at source and %s files (total size %s) at destination in %.1fs\n",
		lib.GroupThousands(len(sourceFiles)), bytesutil.BinaryFormat(sourceSize), lib.GroupThousands(len(destinationFiles)),
		bytesutil.BinaryFormat(destinationSize), end.Sub(start).Seconds())

	fmte.Printf("Finding files at source that don't have counterparts at destination...\n")
	orphansAtSource := service.FindOrphans(sourceFiles, destinationFiles)
	hashable := hashableOrphans(orphansAtSource, sourceFiles)

	var actions []action.SyncAction
	// Digests computed while matching moves; reused by the archive scan below. Empty when
	// there were no candidates at destination, in which case that phase never ran.
	var knownOrphanDigests map[string]entity.FileDigest
	moved := movedFiles{}
	destActionsApplied := false
	if len(orphansAtSource) == 0 {
		fmte.Printf("All files at source directory have counterparts.\n")
	} else {
		sort.Strings(orphansAtSource)
		fmte.Printf("Found %s files\n", lib.GroupThousands(len(orphansAtSource)))

		fmte.Printf("Finding candidates at destination...\n")
		candidatesAtDestination := findCandidatesAtDestination(sourceFiles, destinationFiles, hashable)
		if len(candidatesAtDestination) == 0 {
			fmte.Printf("No move candidates at destination: all %s files are new.\n", lib.GroupThousands(len(orphansAtSource)))
		} else {
			sort.Strings(candidatesAtDestination)
			fmte.Printf("Found %s candidates.\n", lib.GroupThousands(len(candidatesAtDestination)))

			fmte.Printf("Identifying file renames/movements and timestamp changes...\n")
			start = time.Now()

			// Remote files are hashed by the agent, one request per chunk; local files here.
			var sourceDone, destinationDone int32
			remoteHash := func(basePath string, done *int32) service.ChunkDigestFunc {
				return func(chunk []string) (map[string]entity.FileDigest, error) {
					digests, err := agentClient.BatchDigest(basePath, chunk, nil, intervalMs)
					atomic.AddInt32(done, int32(len(chunk)))
					return digests, err
				}
			}
			localHash := func(basePath string, done *int32) service.ChunkDigestFunc {
				return func(chunk []string) (map[string]entity.FileDigest, error) {
					return service.BatchDigestsParallel(nil, basePath, chunk, done), nil
				}
			}
			hashOrphans, hashCandidates := remoteHash(sourceDirPath, &sourceDone), localHash(destDirPath, &destinationDone)
			if !sourceIsRemote {
				hashOrphans, hashCandidates = localHash(sourceDirPath, &sourceDone), remoteHash(destDirPath, &destinationDone)
			}
			matcher := service.NewDestMatcher(sourceDirPath, sourceFiles, destDirPath, destinationFiles, nil,
				copyDuplicates, useReflink)
			var applier destApplier
			if sourceIsRemote {
				applier = newDestApplier(outputScriptPath, destDirPath, dryRun, verbose, moved)
			} else {
				// The destination's directories can't be checked from here.
				matcher.AlwaysCreateParentDirs()
			}
			var streamErr error
			actions, streamErr = streamDestinationPhase(matcher, hashable, candidatesAtDestination,
				hashOrphans, hashCandidates, &sourceDone, &destinationDone, applier,
				sourceWhere, destWhere, progressFrequency)
			if streamErr != nil {
				return streamErr
			}
			knownOrphanDigests = matcher.OrphanDigests()
			if applier != nil {
				destActionsApplied = true
			}

			end = time.Now()
			fmte.Printf("Completed in %.1fs\n", end.Sub(start).Seconds())
			if len(actions) == 0 {
				fmte.Printf("No sync actions found. You may run rsync.\n")
			} else {
				fmte.Printf("Found %s actions that can save you %s of files transfer!\n",
					lib.GroupThousands(len(actions)), bytesutil.BinaryFormat(matcher.Savings()))
			}
		}
	}

	// With a local destination the moves and copies already ran while hashing; they are
	// still needed to know which orphans are taken care of.
	destActions := actions
	if destActionsApplied {
		actions = nil
	}

	// Archive scanning — archives are on the destination side
	if len(archivePaths) > 0 && len(orphansAtSource) > 0 {
		resolvedOrphans := set.NewSet[string]()
		for _, a := range destActions {
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
		for _, o := range hashable {
			if !resolvedOrphans.Contains(o) {
				unmatchedOrphans = append(unmatchedOrphans, o)
			}
		}
		if len(unmatchedOrphans) > 0 {
			fmte.Printf("Scanning %s archive path(s) for %s unmatched orphans%s...\n",
				lib.GroupThousands(len(archivePaths)), lib.GroupThousands(len(unmatchedOrphans)),
				skippedSmallNote(orphansAtSource, hashable))
			// Digests of unmatched orphans at source, computed on demand: only orphans
			// that some archive file matches on extension and size are ever hashed.
			digestFn := func(orphans []string) (map[string]entity.FileDigest, error) {
				return withDigestProgress(len(orphans), sourceWhere, progressFrequency,
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
				archiveWalks = moved.rebaseWalks(archiveWalks)
				// The destination is local, so matches can be applied as they are found.
				var appliedArchiveActions int
				var onArchiveAction service.ArchiveActionFunc
				if outputScriptPath == "" && !dryRun {
					onArchiveAction = recordResolvedOrphans(newLocalArchiveActionStreamer(&appliedArchiveActions, moved),
						destDirPath, archiveResolved)
				}
				var archiveProgress service.ArchiveScanProgress
				atomic.StoreInt32(&archiveProgress.FilesFound, atomic.LoadInt32(&scanArchiveCounter))
				stopArchiveProgress := startArchiveScanProgress(&archiveProgress, destWhere, progressFrequency)
				archiveActions, archiveErr := service.ScanArchivesForCopiesWithDigests(
					archiveWalks, unmatchedOrphans, knownOrphanDigests, digestFn, onArchiveAction,
					sourceFiles, destDirPath, useReflink, nil,
					&archiveProgress)
				stopArchiveProgress()
				if appliedArchiveActions > 0 {
					fmte.Printf("Applied %s actions from archive paths while scanning\n", lib.GroupThousands(appliedArchiveActions))
				}
				if archiveErr != nil {
					return fmt.Errorf("error scanning archives: %+v", archiveErr)
				}
				if len(archiveActions) > 0 {
					fmte.Printf("Found %s additional actions from archive paths\n", lib.GroupThousands(len(archiveActions)))
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
				// Matches can be applied while the scan continues, as long as the connection
				// can carry the perform requests alongside the hashing ones.
				var appliedArchiveActions int
				var onArchiveAction service.ArchiveActionFunc
				flushArchiveActions := func() error { return nil }
				if outputScriptPath == "" && !dryRun && agentClient.IsConcurrent() {
					onArchiveAction, flushArchiveActions = newRemoteArchiveActionStreamer(
						agentClient.Perform, dryRun, &appliedArchiveActions)
				}
				archiveActions, archiveErr := scanArchivesViaAgent(agentClient, archiveWalks,
					unmatchedOrphans, knownOrphanDigests, digestFn, onArchiveAction,
					sourceFiles, destDirPath, useReflink, progressFrequency)
				if archiveErr == nil {
					archiveErr = flushArchiveActions()
				}
				if appliedArchiveActions > 0 {
					fmte.Printf("Applied %s actions from archive paths while scanning\n", lib.GroupThousands(appliedArchiveActions))
				}
				if archiveErr != nil {
					return fmt.Errorf("error scanning archives via agent: %+v", archiveErr)
				}
				if len(archiveActions) > 0 {
					fmte.Printf("Found %s additional actions from archive paths\n", lib.GroupThousands(len(archiveActions)))
					actions = append(actions, archiveActions...)
				}
			}
		}
	}

	if plan.enabled() {
		digestFn := func(orphans []string) (map[string]entity.FileDigest, error) {
			return withDigestProgress(len(orphans), sourceWhere, progressFrequency,
				func(counter *int32) (map[string]entity.FileDigest, error) {
					return agentClient.BatchDigest(sourceDirPath, orphans, counter, progressFrequency.Milliseconds())
				})
		}
		resolvingActions := append(append([]action.SyncAction{}, destActions...), actions...)
		if err := writeCopyPlan(plan, orphansAtSource, resolvingActions, archiveResolved, destDirPath,
			sourceFiles, knownOrphanDigests, digestFn); err != nil {
			return err
		}
	}

	// Directory timestamps go last: every file written below a directory changes its mtime.
	if syncDirTimestamps && sourceDirs != nil && destDirs != nil {
		dirActions := computeDirTimestampActionsFromMaps(sourceDirPath, sourceDirs, destDirPath, destDirs)
		if len(dirActions) > 0 {
			fmte.Printf("Found %s directory timestamp actions\n", lib.GroupThousands(len(dirActions)))
			actions = append(actions, dirActions...)
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
		return performActionsViaAgent(agentClient, actions, destDirPath, dryRun, verbose)
	}

	// Source is remote, destination is local: perform locally
	return performActionsTracked(actions, destDirPath, dryRun, verbose, progressFrequency, moved)
}

// newLocalArchiveActionStreamer applies each archive match immediately instead of
// collecting it for the end, so an interrupted run keeps every copy it already made. The
// count of applied actions is reported through applied.
//
// Only for a local destination: there a copy is a reflink or a plain local write, cheap
// enough to do one at a time. With a remote destination each action would be its own
// round-trip over the agent connection, where batching at the end is the better trade.
func newLocalArchiveActionStreamer(applied *int, moved movedFiles) service.ArchiveActionFunc {
	return func(a action.SyncAction) error {
		a = moved.redirect(a)
		if err := a.Perform(); err != nil {
			fmte.PrintfErr("skipping %s: %+v\n", a, err)
			return fmt.Errorf("%w: %v", service.ErrActionSkipped, err)
		}
		*applied++
		fmte.PrintfV("Performed: %s\n", a.UnixCommand())
		return nil
	}
}

// sideLocations names where source and destination are for progress lines; both stay
// empty when everything is local.
func sideLocations(sourceRemote, destRemote bool) (sourceWhere, destWhere string) {
	switch {
	case sourceRemote:
		return "remote", "local"
	case destRemote:
		return "local", "remote"
	}
	return "", ""
}

// startArchiveScanProgress reports the archive scan counters until the returned stop
// function is called.
func startArchiveScanProgress(progress *service.ArchiveScanProgress, where string,
	progressFrequency time.Duration,
) (stop func()) {
	return progressBoard.Track(progressFrequency, "Archives", func() []lib.ProgressPart {
		return []lib.ProgressPart{
			{Count: int64(atomic.LoadInt32(&progress.FilesChecked)), Total: int64(atomic.LoadInt32(&progress.FilesFound)),
				Label: "checked", Where: where},
			{Count: int64(atomic.LoadInt32(&progress.DigestsDone)),
				Total: int64(atomic.LoadInt32(&progress.DigestsNeeded)), Label: "hashed", Where: where},
			{Count: int64(atomic.LoadInt32(&progress.Matches)), Label: "matched"},
		}
	})
}

// withDigestProgress runs compute, reporting how many of count digests are done so far.
// The counter it hands to compute is what drives that output.
func withDigestProgress(count int, where string, progressFrequency time.Duration,
	compute func(counter *int32) (map[string]entity.FileDigest, error),
) (map[string]entity.FileDigest, error) {
	fmte.Printf("Hashing %s orphan candidate(s) at source...\n", lib.GroupThousands(count))
	var counter int32
	stop := progressBoard.Track(progressFrequency, "Hashing orphans", func() []lib.ProgressPart {
		return []lib.ProgressPart{{Count: int64(atomic.LoadInt32(&counter)), Total: int64(count), Label: "src",
			Where: where}}
	})
	digests, err := compute(&counter)
	stop()
	return digests, err
}

// walkArchivesViaAgent walks archive paths on the remote destination, the counterpart of
// service.WalkArchives for a remote destination.
//
// counter keeps growing across the archive paths: the client adds each walk's count to
// what the counter held before.
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
// emitArchiveAction hands an action to onAction, or collects it when there is none.
// Mirrors what the local archive scan does, so a streamed action is never also returned.
func emitArchiveAction(a action.SyncAction, onAction service.ArchiveActionFunc,
	collected *[]action.SyncAction,
) error {
	if onAction == nil {
		*collected = append(*collected, a)
		return nil
	}
	return onAction(a)
}

func scanArchivesViaAgent(agentClient *remote.AgentClient, archiveWalks []service.ArchiveWalk,
	unmatchedOrphans []string, knownOrphanDigests map[string]entity.FileDigest,
	digestFn service.OrphanDigestFunc, onAction service.ArchiveActionFunc,
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
	stopProgress := progressBoard.Track(progressFrequency, "Archives", func() []lib.ProgressPart {
		return []lib.ProgressPart{
			{Count: int64(atomic.LoadInt32(&archiveCheckCounter)),
				Total: int64(atomic.LoadInt32(&archiveWalkCounter)), Label: "checked", Where: "local"},
			{Count: int64(atomic.LoadInt32(&archiveDigestCounter)), Label: "hashed", Where: "remote"},
		}
	})
	defer stopProgress()

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
			return nil, fmt.Errorf("error computing digests of %s orphan candidate(s): %w",
				lib.GroupThousands(len(neededOrphans)), orphanDigestErr)
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
						if err := emitArchiveAction(mkdirAction, onAction, &actions); err != nil {
							return nil, err
						}
						uniqueness.Add(mkdirAction.Uniqueness())
					}
					copyAction := action.CopyFileAction{
						AbsSourcePath: archiveAbsPath,
						AbsDestPath:   absDest,
						SourceModTime: time.Unix(sourceFiles[orphan].ModifiedTimestamp, 0),
						UseReflink:    useReflink,
					}
					if !uniqueness.Contains(copyAction.Uniqueness()) {
						if err := emitArchiveAction(copyAction, onAction, &actions); err != nil {
							return nil, err
						}
						uniqueness.Add(copyAction.Uniqueness())
						matchedOrphans.Add(orphan)
					}
				}
			}
		}
	}

	return actions, nil
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
		_, sourceDirs, _, srcErr = walkIncluded(sourceDirPath, nil, dirsWalk(sourceFS, exclusions))
	}()
	go func() {
		defer wg.Done()
		_, destDirs, _, dstErr = walkIncluded(destDirPath, missingLocally(destFS), dirsWalk(destFS, exclusions))
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

// actionSpecs translates actions into what the agent protocol carries. Order is kept:
// the agent performs them in this order, and a copy may depend on a directory before it.
func actionSpecs(actions []action.SyncAction) []remote.ActionSpec {
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
	return specs
}

// performRemoteFunc executes a batch of actions on the remote side, in the given order.
type performRemoteFunc func(specs []remote.ActionSpec, dryRun bool) ([]remote.ActionResult, error)

// newRemoteArchiveActionStreamer applies archive matches on a remote destination while the
// scan is still running, so an interrupted run keeps what it already copied.
//
// Unlike the local streamer this batches: every action on its own would be a round-trip
// over the ssh connection. A batch is sent once it reaches remoteActionBatchSize, and the
// returned flush sends the remainder. Needs a connection that correlates by request ID,
// since the hashing requests are in flight at the same time.
func newRemoteArchiveActionStreamer(perform performRemoteFunc, dryRun bool,
	applied *int,
) (onAction service.ArchiveActionFunc, flush func() error) {
	const remoteActionBatchSize = 64
	var batch []action.SyncAction

	send := func() error {
		if len(batch) == 0 {
			return nil
		}
		results, err := perform(actionSpecs(batch), dryRun)
		if err != nil {
			return fmt.Errorf("remote perform failed: %w", err)
		}
		for i, result := range results {
			if !result.Success {
				return fmt.Errorf("error performing \"%s\": %s", batch[i].UnixCommand(), result.Error)
			}
		}
		*applied += len(batch)
		batch = batch[:0]
		return nil
	}

	return func(a action.SyncAction) error {
			batch = append(batch, a)
			if len(batch) < remoteActionBatchSize {
				return nil
			}
			return send()
		}, func() error {
			return send()
		}
}

// performActionsViaAgent hands the whole action list to the remote agent at once.
//
// The reflink count here is by intent: a reflink that the remote filesystem couldn't do
// fell back to a full copy on that host, and the agent protocol doesn't report it back.
func performActionsViaAgent(agentClient *remote.AgentClient, actions []action.SyncAction,
	destDirPath string, dryRun, verbose bool,
) error {
	if dryRun {
		fmte.Printf("Simulating sync actions at destination (dry run)...\n")
	} else {
		fmte.Printf("Applying sync actions at destination via remote agent...\n")
	}

	// The agent runs the actions in the order given and knows nothing of earlier moves.
	redirectInOrder(actions)
	specs := actionSpecs(actions)

	start := time.Now()
	results, err := agentClient.Perform(specs, dryRun)
	end := time.Now()
	if err != nil {
		return fmt.Errorf("remote perform failed: %w", err)
	}

	// The agent performs the whole batch before answering, so there is nothing to report
	// while it runs: what is left is the breakdown, plus every failure.
	var stats actionStats
	successCount := 0
	for i, r := range results {
		header := strings.Replace(
			fmt.Sprintf("%4s/%s %s: ", lib.GroupThousands(i+1), lib.GroupThousands(len(actions)), actions[i]),
			destDirPath+"/", "", -1,
		)
		if r.Success {
			stats.record(actions[i], nil)
			if verbose {
				if dryRun {
					fmte.Printf("%sskipping (dry run)\n", header)
				} else {
					fmte.Printf("%sdone\n", header)
				}
			}
			successCount++
		} else {
			stats.record(actions[i], errors.New(r.Error))
			fmte.PrintfErr("%sfailed due to: %s\n", header, r.Error)
		}
	}

	if dryRun {
		fmte.Printf("Dry run completed in %.1fs: %s actions would be performed (%s)\n",
			end.Sub(start).Seconds(), lib.GroupThousands(successCount), stats.summary())
	} else {
		fmte.Printf("Sync completed in %.1fs: %s out of %s actions succeeded (%s)\n",
			end.Sub(start).Seconds(), lib.GroupThousands(successCount), lib.GroupThousands(len(actions)), stats.summary())
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

func performActions(actions []action.SyncAction, destinationDirPath string, dryRun, verbose bool,
	progressFrequency time.Duration,
) error {
	return performActionsTracked(actions, destinationDirPath, dryRun, verbose, progressFrequency, movedFiles{})
}

// performActionsTracked is performActions sharing movedPaths with other phases of the run,
// so copies from a file moved by an earlier batch find it at its new path.
func performActionsTracked(actions []action.SyncAction, destinationDirPath string, dryRun, verbose bool,
	progressFrequency time.Duration, movedPaths movedFiles,
) error {
	if dryRun {
		fmte.Printf("Simulating sync actions at destination (dry run)...\n")
	} else {
		fmte.Printf("Applying sync actions at destination...\n")
	}
	successCount := 0

	action.SortByDestinationDir(actions)

	total := len(actions)
	prefixStrip := destinationDirPath + "/"

	var stats actionStats
	var done atomic.Int64

	// Printer goroutine — does all formatting and stdout I/O. It only sees what is worth
	// printing: every action under --verbose, and failures always.
	printCh := make(chan actionResult, 256)
	var printerDone sync.WaitGroup
	printerDone.Add(1)
	go func() {
		defer printerDone.Done()
		for r := range printCh {
			header := strings.Replace(
				fmt.Sprintf("%4s/%s %s: ", lib.GroupThousands(r.index+1), lib.GroupThousands(total), r.action),
				prefixStrip, "", -1,
			)
			if r.err != nil {
				fmte.PrintfErr("%sfailed due to: %+v\n", header, r.err)
			} else if r.dryRun {
				fmt.Print(header, "skipping (dry run)\n")
			} else {
				fmt.Print(header, "done\n")
			}
		}
	}()

	stopProgress := func() {}
	if !verbose {
		stopProgress = startActionStatsProgress(&stats, &done, total, progressFrequency)
	}

	// I/O loop — performs actions, sends lightweight results to printer
	start := time.Now()
	for i, syncAction := range actions {
		syncAction = movedPaths.redirect(syncAction)

		var aErr error
		if !dryRun {
			aErr = syncAction.Perform()
		}

		stats.record(syncAction, aErr)
		done.Store(int64(i + 1))
		if verbose || aErr != nil {
			printCh <- actionResult{index: i, action: syncAction, err: aErr, dryRun: dryRun}
		}

		if aErr == nil {
			successCount++
			if !dryRun {
				movedPaths.record(syncAction)
			}
		}
	}
	stopProgress()
	close(printCh)
	printerDone.Wait()
	end := time.Now()

	if dryRun {
		fmte.Printf("Dry run completed in %.1fs: %s actions would be performed (%s)\n",
			end.Sub(start).Seconds(), lib.GroupThousands(successCount), stats.summary())
	} else {
		fmte.Printf("Sync completed in %.1fs: %s out of %s actions succeeded (%s)\n",
			end.Sub(start).Seconds(), lib.GroupThousands(successCount), lib.GroupThousands(total), stats.summary())
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
