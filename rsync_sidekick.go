package main

import (
	"fmt"
	"github.com/m-manu/rsync-sidekick/action"
	"github.com/m-manu/rsync-sidekick/bytesutil"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/m-manu/rsync-sidekick/service"
	"github.com/m-manu/rsync-sidekick/utils"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const unixCommandLengthGuess = 200

func rsyncSidekick(sourceDirPath string, exclusions entity.StringSet, destinationDirPath string,
	scriptGen bool, extraInfo bool) error {
	runID := time.Now().Format("150405")
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
	if extraInfo {
		utils.WriteSliceToFile(orphansAtSource, fmt.Sprintf("./info_%s_orphans_at_source.txt", runID))
	}
	fmte.Printf("Finding candidates at destination...\n")
	candidatesAtDestination := findCandidatesAtDestination(sourceFiles, destinationFiles, orphansAtSource)
	if len(candidatesAtDestination) == 0 {
		fmte.Printf("No candidates found. Looks like all %d files are new. rsync will do the rest.\n", len(orphansAtSource))
		return nil
	}
	sort.Strings(candidatesAtDestination)
	if extraInfo {
		utils.WriteSliceToFile(candidatesAtDestination,
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
		return fmt.Errorf("error while computing sync actions: %+v", syncErr)
	}
	fmte.Printf("Completed in %.1fs\n", end.Sub(start).Seconds())
	if len(actions) == 0 {
		fmte.Printf("No sync actions found. You may run rsync.\n")
		return nil
	}
	fmte.Printf("Found %d actions that can save you %s of files transfer!\n",
		len(actions), bytesutil.BinaryFormat(savings))
	if len(actions) == 0 {
		return nil
	}
	shellScriptFileName := fmt.Sprintf("sync_actions_%s.sh", runID)
	if scriptGen {
		fmte.Printf("Writing sync actions to shell script \"%s\"...\n", shellScriptFileName)
		shellScriptFile, shellScriptCreateErr := os.Create(shellScriptFileName)
		if shellScriptCreateErr != nil {
			return fmt.Errorf("couldn't create: %v", shellScriptCreateErr)
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
	} else {
		fmte.Printf("Applying sync actions at destination...\n")
		successCount := 0
		start = time.Now()
		for i, syncAction := range actions {
			fmte.Printf("%4d/%d %s: ", i+1, len(actions), syncAction)
			aErr := syncAction.Perform()
			if aErr == nil {
				fmte.Printf("done\n")
				successCount++
			} else {
				fmte.Printf("failed due to: %+v\n", aErr)
			}
		}
		end = time.Now()
		fmte.Printf("Sync completed in %.1fs: %d out of %d actions succeeded\n",
			end.Sub(start).Seconds(), successCount, len(actions))
	}
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
	orphansFileExtAndSizeMap := make(map[entity.FileExtAndSize]struct{}, len(orphansAtSource))
	for _, path := range orphansAtSource {
		fileMeta := sourceFiles[path]
		key := entity.FileExtAndSize{FileExtension: utils.GetFileExt(path), FileSize: fileMeta.Size}
		orphansFileExtAndSizeMap[key] = struct{}{}
	}
	candidatesAtDestination := make([]string, 0, len(orphansAtSource))
	for path, fileMeta := range destinationFiles {
		key := entity.FileExtAndSize{FileExtension: utils.GetFileExt(path), FileSize: fileMeta.Size}
		if _, exists := orphansFileExtAndSizeMap[key]; exists {
			candidatesAtDestination = append(candidatesAtDestination, path)
		}
	}
	return candidatesAtDestination
}
