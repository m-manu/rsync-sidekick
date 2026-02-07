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
	destinationDirPath string, verbose bool, progressFrequency time.Duration) ([]action.SyncAction, error) {
	return getSyncActionsWithProgressFS(runID, sourceDirPath, nil, exclusions,
		destinationDirPath, nil, verbose, progressFrequency)
}

func getSyncActionsWithProgressFS(runID string, sourceDirPath string, sourceFS rsfs.FileSystem,
	exclusions set.Set[string], destinationDirPath string, destFS rsfs.FileSystem,
	verbose bool, progressFrequency time.Duration) ([]action.SyncAction, error) {
	if verbose {
		fmte.VerboseOn()
	}
	var start, end time.Time
	fmte.Printf("Scanning source (%s) and destination (%s) directories...\n", sourceDirPath, destinationDirPath)
	start = time.Now()
	var sourceFiles, destinationFiles map[string]entity.FileMeta
	var sourceSize, destinationSize int64
	var sourceFilesErr, destinationFilesErr error
	var wgDirScan sync.WaitGroup
	wgDirScan.Add(2)
	go func() {
		defer wgDirScan.Done()
		if sourceFS != nil {
			sourceFiles, sourceSize, sourceFilesErr = service.FindFilesFromDirectoryWithFS(sourceFS, sourceDirPath, exclusions)
		} else {
			sourceFiles, sourceSize, sourceFilesErr = service.FindFilesFromDirectory(sourceDirPath, exclusions)
		}
	}()
	go func() {
		defer wgDirScan.Done()
		if destFS != nil {
			destinationFiles, destinationSize, destinationFilesErr = service.FindFilesFromDirectoryWithFS(destFS, destinationDirPath, exclusions)
		} else {
			destinationFiles, destinationSize, destinationFilesErr = service.FindFilesFromDirectory(destinationDirPath, exclusions)
		}
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
	if len(candidatesAtDestination) == 0 {
		fmte.Printf("No candidates found. Looks like all %d files are new. rsync will do the rest.\n", len(orphansAtSource))
		return []action.SyncAction{}, nil
	}
	sort.Strings(candidatesAtDestination)
	if verbose {
		lib.WriteSliceToFile(candidatesAtDestination,
			fmt.Sprintf("./info_%s_candidates_at_destination.txt", runID),
		)
	}
	fmte.Printf("Found %d candidates.\n", len(candidatesAtDestination))
	fmte.Printf("Identifying file renames/movements and timestamp changes...\n")
	start = time.Now()
	var actions []action.SyncAction
	var savings int64
	var syncErr error
	var sourceCounter, destinationCounter int32
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		actions, savings, syncErr = service.ComputeSyncActionsWithFS(sourceFS, destFS,
			sourceDirPath, sourceFiles, orphansAtSource,
			destinationDirPath, destinationFiles, candidatesAtDestination, &sourceCounter, &destinationCounter)
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
	if len(actions) == 0 {
		fmte.Printf("No sync actions found. You may run rsync.\n")
		return []action.SyncAction{}, nil
	}
	fmte.Printf("Found %d actions that can save you %s of files transfer!\n",
		len(actions), bytesutil.BinaryFormat(savings))
	return actions, nil
}

func rsyncSidekick(runID string, sourceDirPath string, exclusions set.Set[string], destinationDirPath string,
	outputScriptPath string, verbose bool, dryRun bool, progressFrequency time.Duration) error {
	actions, err := getSyncActionsWithProgress(runID, sourceDirPath, exclusions, destinationDirPath, verbose, progressFrequency)
	if err != nil {
		return err // no extra info needed
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
	verbose bool, dryRun bool, progressFrequency time.Duration,
) error {
	remotePath := remoteLoc.Path

	if agentClient != nil {
		return rsyncSidekickRemoteExec(runID, remoteLoc, remotePath, localPath,
			sourceIsRemote, agentClient, exclusions, outputScriptPath,
			verbose, dryRun, progressFrequency)
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
		exclusions, destDirPath, destFS, verbose, progressFrequency)
	if actionsErr != nil {
		return actionsErr
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
	progressFrequency time.Duration,
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
	var sourceSize, destinationSize int64
	var sourceFilesErr, destinationFilesErr error
	var wgDirScan sync.WaitGroup
	wgDirScan.Add(2)

	go func() {
		defer wgDirScan.Done()
		if sourceIsRemote {
			sourceFiles, sourceSize, sourceFilesErr = agentClient.Walk(sourceDirPath, excludedNames)
		} else {
			sourceFiles, sourceSize, sourceFilesErr = service.FindFilesFromDirectory(sourceDirPath, exclusions)
		}
	}()
	go func() {
		defer wgDirScan.Done()
		if sourceIsRemote {
			destinationFiles, destinationSize, destinationFilesErr = service.FindFilesFromDirectory(destDirPath, exclusions)
		} else {
			destinationFiles, destinationSize, destinationFilesErr = agentClient.Walk(destDirPath, excludedNames)
		}
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
	if len(orphansAtSource) == 0 {
		fmte.Printf("All files at source directory have counterparts. So, no action needed 🙂!\n")
		return nil
	}
	sort.Strings(orphansAtSource)
	fmte.Printf("Found %d files\n", len(orphansAtSource))

	fmte.Printf("Finding candidates at destination...\n")
	candidatesAtDestination := findCandidatesAtDestination(sourceFiles, destinationFiles, orphansAtSource)
	if len(candidatesAtDestination) == 0 {
		fmte.Printf("No candidates found. Looks like all %d files are new. rsync will do the rest.\n", len(orphansAtSource))
		return nil
	}
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
	var wgDigest sync.WaitGroup
	wgDigest.Add(2)

	go func() {
		defer wgDigest.Done()
		if sourceIsRemote && len(remoteOrphans) > 0 {
			remoteDigests, remoteDigestErr = agentClient.BatchDigest(sourceDirPath, remoteOrphans)
		} else if !sourceIsRemote && len(remoteCandiates) > 0 {
			remoteDigests, remoteDigestErr = agentClient.BatchDigest(destDirPath, remoteCandiates)
		}
	}()
	go func() {
		defer wgDigest.Done()
		if sourceIsRemote && len(localCandidates) > 0 {
			localDigests, localDigestErr = batchDigestLocal(destDirPath, localCandidates)
		} else if !sourceIsRemote && len(localOrphans) > 0 {
			localDigests, localDigestErr = batchDigestLocal(sourceDirPath, localOrphans)
		}
	}()
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
	actions := matchAndBuildActions(sourceDirPath, sourceFiles, orphansAtSource, orphanDigests,
		destDirPath, destinationFiles, candidatesAtDestination, candidateDigests)

	end = time.Now()
	fmte.Printf("Completed in %.1fs\n", end.Sub(start).Seconds())

	if len(actions) == 0 {
		fmte.Printf("No sync actions found. You may run rsync.\n")
		return nil
	}

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
	}
	fmte.Printf("Found %d actions that can save you %s of files transfer!\n",
		len(actions), bytesutil.BinaryFormat(savings))

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

func batchDigestLocal(basePath string, files []string) (map[string]entity.FileDigest, error) {
	digests := make(map[string]entity.FileDigest, len(files))
	for _, relPath := range files {
		absPath := fmt.Sprintf("%s/%s", basePath, relPath)
		digest, err := service.GetDigest(absPath)
		if err != nil {
			continue
		}
		digests[relPath] = digest
	}
	return digests, nil
}

func matchAndBuildActions(
	sourceDirPath string, sourceFiles map[string]entity.FileMeta,
	orphansAtSource []string, orphanDigests map[string]entity.FileDigest,
	destDirPath string, destinationFiles map[string]entity.FileMeta,
	candidatesAtDestination []string, candidateDigests map[string]entity.FileDigest,
) []action.SyncAction {
	// Build reverse maps: digest → files
	orphanDigestToFiles := make(map[entity.FileDigest][]string)
	for _, f := range orphansAtSource {
		if d, ok := orphanDigests[f]; ok {
			orphanDigestToFiles[d] = append(orphanDigestToFiles[d], f)
		}
	}
	candidateDigestToFiles := make(map[entity.FileDigest][]string)
	for _, f := range candidatesAtDestination {
		if d, ok := candidateDigests[f]; ok {
			candidateDigestToFiles[d] = append(candidateDigestToFiles[d], f)
		}
	}

	actions := make([]action.SyncAction, 0)
	uniqueness := set.NewSet[string]()

	for _, orphanAtSource := range orphansAtSource {
		orphanDigest, ok := orphanDigests[orphanAtSource]
		if !ok {
			continue
		}
		if len(orphanDigestToFiles[orphanDigest]) > 1 {
			continue
		}
		candidates, hasCandidates := candidateDigestToFiles[orphanDigest]
		if !hasCandidates {
			continue
		}
		var candidateAtDestination string
		if len(candidates) == 1 {
			candidateAtDestination = candidates[0]
		} else {
			for _, dp := range candidates {
				if _, existsAtSource := sourceFiles[dp]; !existsAtSource {
					candidateAtDestination = dp
					break
				}
			}
		}
		if candidateAtDestination == "" {
			continue
		}
		if destinationFiles[candidateAtDestination].ModifiedTimestamp != sourceFiles[orphanAtSource].ModifiedTimestamp {
			if srcMetaForCandidate, existsAtSourceForCandidate := sourceFiles[candidateAtDestination]; !(existsAtSourceForCandidate && srcMetaForCandidate == destinationFiles[candidateAtDestination]) {
				timestampAction := action.PropagateTimestampAction{
					SourceBaseDirPath:           sourceDirPath,
					DestinationBaseDirPath:      destDirPath,
					SourceFileRelativePath:      orphanAtSource,
					DestinationFileRelativePath: candidateAtDestination,
				}
				if !uniqueness.Contains(timestampAction.Uniqueness()) {
					actions = append(actions, timestampAction)
					uniqueness.Add(timestampAction.Uniqueness())
				}
			}
		}
		if _, existsAtSource := sourceFiles[candidateAtDestination]; !existsAtSource &&
			candidateAtDestination != orphanAtSource {
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
		}
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
			// Read the source file's modification time locally
			srcPath := fmt.Sprintf("%s/%s", act.SourceBaseDirPath, act.SourceFileRelativePath)
			srcInfo, err := os.Lstat(srcPath)
			if err != nil {
				return fmt.Errorf("cannot read source file %q for timestamp: %w", srcPath, err)
			}
			specs = append(specs, remote.ActionSpec{
				Type:         "timestamp",
				DestBasePath: act.DestinationBaseDirPath,
				DestRelPath:  act.DestinationFileRelativePath,
				ModTimestamp:  srcInfo.ModTime().Unix(),
			})
		case action.MakeDirectoryAction:
			specs = append(specs, remote.ActionSpec{
				Type:    "mkdir",
				DirPath: act.AbsoluteDirPath,
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
