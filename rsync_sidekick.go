package main

import (
	"fmt"
	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/action"
	"github.com/m-manu/rsync-sidekick/bytesutil"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/m-manu/rsync-sidekick/lib"
	"github.com/m-manu/rsync-sidekick/service"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const unixCommandLengthGuess = 200

func getSyncActionsWithProgress(runID string, sourceDirPath string, exclusions set.Set[string],
	destinationDirPath string, verbose bool) ([]action.SyncAction, error) {
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
		sourceFiles, sourceSize, sourceFilesErr = service.FindFilesFromDirectory(sourceDirPath, exclusions)
	}()
	go func() {
		defer wgDirScan.Done()
		destinationFiles, destinationSize, destinationFilesErr = service.FindFilesFromDirectory(destinationDirPath, exclusions)
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
		actions, savings, syncErr = service.ComputeSyncActions(sourceDirPath, sourceFiles, orphansAtSource,
			destinationDirPath, destinationFiles, candidatesAtDestination, &sourceCounter, &destinationCounter)
	}()
	go func() {
		defer wg.Done()
		reportProgress(&sourceCounter, int32(len(orphansAtSource)),
			&destinationCounter, int32(len(candidatesAtDestination)),
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
	outputScriptPath string, verbose bool, dryRun bool) error {
	actions, err := getSyncActionsWithProgress(runID, sourceDirPath, exclusions, destinationDirPath, verbose)
	if err != nil {
		return err // no extra info needed
	}
	if len(actions) == 0 {
		return nil
	}
	if outputScriptPath != "" {
		return generateScript(actions, outputScriptPath)
	} else {
		return performActions(actions, destinationDirPath, dryRun)
	}
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

func generateScript(actions []action.SyncAction, shellScriptFileName string) error {
	fmte.Printf("Writing sync actions to shell script \"%s\"...\n", shellScriptFileName)
	shellScriptFile, shellScriptCreateErr := os.Create(shellScriptFileName)
	if shellScriptCreateErr != nil {
		return fmt.Errorf("couldn't create file '%s': %+v", shellScriptFileName, shellScriptCreateErr)
	}
	permsErr := os.Chmod(shellScriptFileName, 0700)
	if permsErr != nil {
		return fmt.Errorf("couldn't change permissions on file '%s': %+v", shellScriptFileName, permsErr)
	}
	defer shellScriptFile.Close()
	var sb strings.Builder
	sb.Grow(unixCommandLengthGuess * len(actions))
	for _, a := range actions {
		sb.WriteString(a.UnixCommand())
		sb.WriteString("\n")
	}
	shellScriptFile.WriteString(sb.String())
	fmte.Printf("Done. You may run it now.\n")
	return nil
}

func reportProgress(sourceActual *int32, sourceExpected int32, destinationActual *int32, destinationExpected int32) {
	var sourceProgress, destinationProgress float64
	time.Sleep(100 * time.Millisecond)
	for atomic.LoadInt32(sourceActual) < sourceExpected || atomic.LoadInt32(destinationActual) < destinationExpected {
		time.Sleep(2 * time.Second)
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
