package main

import (
	"fmt"
	"github.com/m-manu/rsync-sidekick/bytesutil"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/filesutil"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/m-manu/rsync-sidekick/service"
	"os"
	"strings"
	"time"
)

func rsyncSidekick(sourceDirPath string, exclusions map[string]struct{}, destinationDirPath string, scriptGen bool) error {
	var start, end time.Time
	fmte.Printf("Scanning source directory \"%s\"...\n", sourceDirPath)
	start = time.Now()
	sourceFiles, sourceSize, sourceFilesErr := service.FindFilesFromDirectories(sourceDirPath, exclusions)
	end = time.Now()
	if sourceFilesErr != nil {
		return fmt.Errorf("error scanning source directory: %+v", sourceFilesErr)
	}
	fmte.Printf("Found %d files (total size %s) in %.1fs\n",
		len(sourceFiles), bytesutil.BinaryFormat(sourceSize), end.Sub(start).Seconds())
	fmte.Printf("Scanning destination directory \"%s\"...\n", destinationDirPath)
	start = time.Now()
	destinationFiles, destinationSize, destinationFilesErr := service.FindFilesFromDirectories(destinationDirPath, exclusions)
	end = time.Now()
	if destinationFilesErr != nil {
		return fmt.Errorf("error scanning destination directory: %+v", destinationFilesErr)
	}
	fmte.Printf("Found %d files (total size %s) in %.1fs\n",
		len(destinationFiles), bytesutil.BinaryFormat(destinationSize), end.Sub(start).Seconds())
	fmte.Printf("Finding files at source that don't have counterparts at destination...\n")
	start = time.Now()
	orphansAtSource := service.FindOrphans(sourceFiles, destinationFiles)
	end = time.Now()
	if len(orphansAtSource) == 0 {
		fmte.Printf("All files at source directory have counterparts. So, no action needed 🙂!\n")
		return nil
	}
	fmte.Printf("Found %d files in %.1fs.\n", len(orphansAtSource), end.Sub(start).Seconds())
	fmte.Printf("Finding candidates at destination...\n")
	candidatesAtDestination := findCandidatesAtDestination(sourceFiles, destinationFiles, orphansAtSource)
	if len(candidatesAtDestination) == 0 {
		fmte.Printf("No candidates found. Looks like all %d files are new. rsync will do the rest.\n", len(orphansAtSource))
		return nil
	}
	fmte.Printf("Found %d candidates.\n", len(candidatesAtDestination))
	fmte.Printf("Computing sync actions...\n")
	start = time.Now()
	actions, savings, syncErr := service.ComputeSyncActions(sourceDirPath, sourceFiles, orphansAtSource,
		destinationDirPath, destinationFiles, candidatesAtDestination)
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
	shellScriptFileName := fmte.Sprintf("sync_actions_%s.sh", time.Now().Format("150405"))
	if scriptGen {
		fmte.Printf("Writing sync actions to shell script \"%s\"...\n", shellScriptFileName)
		shellScriptFile, shellScriptCreateErr := os.Create(shellScriptFileName)
		if shellScriptCreateErr != nil {
			return fmt.Errorf("couldn't create: %v", shellScriptCreateErr)
		}
		defer shellScriptFile.Close()
		var sb strings.Builder
		sb.Grow(150 * len(actions))
		for _, action := range actions {
			sb.WriteString(action.UnixCommand())
			sb.WriteString("\n")
		}
		shellScriptFile.WriteString(sb.String())
		fmte.Printf("Done. You may run it now.\n")
	} else {
		fmte.Printf("Applying sync actions...\n")
		successCount := 0
		start = time.Now()
		for i, action := range actions {
			fmte.Printf("%4d/%d %s: ", i+1, len(actions), action)
			aErr := action.Perform()
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

func findCandidatesAtDestination(sourceFiles, destinationFiles map[string]entity.FileMeta, orphansAtSource []string) []string {
	orphansFileExtAndSizeMap := map[entity.FileExtAndSize]struct{}{}
	for _, path := range orphansAtSource {
		fileMeta := sourceFiles[path]
		key := entity.FileExtAndSize{FileExtension: filesutil.GetFileExt(path), FileSize: fileMeta.Size}
		orphansFileExtAndSizeMap[key] = struct{}{}
	}
	candidatesAtDestination := make([]string, 0, len(orphansAtSource))
	for path, fileMeta := range destinationFiles {
		key := entity.FileExtAndSize{FileExtension: filesutil.GetFileExt(path), FileSize: fileMeta.Size}
		if _, exists := orphansFileExtAndSizeMap[key]; exists {
			candidatesAtDestination = append(candidatesAtDestination, path)
		}
	}
	return candidatesAtDestination
}
