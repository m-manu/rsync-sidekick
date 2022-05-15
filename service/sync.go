package service

import (
	"encoding/csv"
	"fmt"
	"github.com/m-manu/rsync-sidekick/action"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/m-manu/rsync-sidekick/utils"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
)

const (
	indexBuildErrorCountTolerance = 20
	parallelismForSource          = 6
	parallelismForDestination     = 6
)

// FindOrphans finds files at source that do not have corresponding files at destination.
// File at destination must exist and have same size and same modified timestamp.
func FindOrphans(sourceFiles, destinationFiles map[string]entity.FileMeta) []string {
	orphansAtSource := make([]string, 0, len(sourceFiles)/10)
	for sourcePath, sourceFileMeta := range sourceFiles {
		destinationFileMeta, existsAtDestination := destinationFiles[sourcePath]
		if !existsAtDestination || sourceFileMeta != destinationFileMeta {
			orphansAtSource = append(orphansAtSource, sourcePath)
		}
	}
	return orphansAtSource
}

func buildIndex(baseDirPath string, filesToScan []string, counter *int32,
	filesToDigests *entity.StringFileDigestMap, digestsToFiles *entity.FileDigestStringMultiMap,
) error {
	errCount := 0
	for _, relativePath := range filesToScan {
		atomic.AddInt32(counter, 1)
		path := filepath.Join(baseDirPath, relativePath)
		digest, err := GetDigest(path)
		if err != nil {
			errCount++
			fmte.PrintfErr("couldn't index file \"%s\" (skipping): %+v\n", path, err)
		}
		if errCount > indexBuildErrorCountTolerance {
			return fmt.Errorf("too many errors while building index")
		}
		filesToDigests.Set(relativePath, digest)
		digestsToFiles.Set(digest, relativePath)
	}
	return nil
}

// ComputeSyncActions identifies the diff between source and destination directories that
// do not require actual file transfer. This is the core function of this tool.
func ComputeSyncActions(sourceDirPath string, sourceFiles map[string]entity.FileMeta, orphansAtSource []string,
	destinationDirPath string, destinationFiles map[string]entity.FileMeta, candidatesAtDestination []string,
	sourceCounter *int32, destinationCounter *int32,
) (actions []action.SyncAction, savings int64, err error) {
	orphanFilesToDigests := entity.NewStringFileDigestMap()
	candidateFilesToDigests := entity.NewStringFileDigestMap()
	orphanDigestsToFiles := entity.NewFileDigestStringMultiMap()
	candidateDigestsToFiles := entity.NewFileDigestStringMultiMap()
	var sourceIndexErrs, destinationIndexErrs []error
	var wg sync.WaitGroup
	wg.Add(parallelismForSource + parallelismForDestination)
	for i := 0; i < parallelismForSource; i++ {
		go func(index int) {
			defer wg.Done()
			low := index * len(orphansAtSource) / parallelismForSource
			high := (index + 1) * len(orphansAtSource) / parallelismForSource
			sourceIndexErr := buildIndex(sourceDirPath, orphansAtSource[low:high], sourceCounter,
				orphanFilesToDigests, orphanDigestsToFiles,
			)
			if sourceIndexErr != nil {
				sourceIndexErrs = append(sourceIndexErrs, sourceIndexErr)
			}
		}(i)
	}
	for i := 0; i < parallelismForDestination; i++ {
		go func(index int) {
			defer wg.Done()
			low := index * len(candidatesAtDestination) / parallelismForDestination
			high := (index + 1) * len(candidatesAtDestination) / parallelismForDestination
			destinationIndexErr := buildIndex(destinationDirPath, candidatesAtDestination[low:high], destinationCounter,
				candidateFilesToDigests, candidateDigestsToFiles,
			)
			if destinationIndexErr != nil {
				destinationIndexErrs = append(destinationIndexErrs, destinationIndexErr)
			}
		}(i)
	}
	wg.Wait()
	if len(sourceIndexErrs) > 0 {
		return nil, 0, fmte.Errors("error(s) while building index on source directory: ",
			sourceIndexErrs)
	}
	if len(destinationIndexErrs) > 0 {
		return nil, 0, fmte.Errors("error(s) while building index on destination directory: ",
			destinationIndexErrs)
	}
	actions = make([]action.SyncAction, 0, orphanFilesToDigests.Len())
	uniquenessMap := entity.NewStringSet(orphanFilesToDigests.Len())
	for orphanAtSource, orphanDigest := range orphanFilesToDigests.Map() {
		if len(orphanDigestsToFiles.Get(orphanDigest)) > 1 {
			// many orphans at source have the same digest
			continue
		}
		if !candidateDigestsToFiles.Exists(orphanDigest) {
			// let rsync handle this
			continue
		}
		matchesAtDestination := candidateDigestsToFiles.Get(orphanDigest)
		var candidateAtDestination string
		if len(matchesAtDestination) == 1 {
			candidateAtDestination = matchesAtDestination[0]
		} else {
			// If multiple files with same digest exist at destination,
			// choose a random one that does *not* exist at source
			for _, destinationPath := range matchesAtDestination {
				if _, existsAtSource := sourceFiles[destinationPath]; !existsAtSource {
					candidateAtDestination = destinationPath
					break
				}
			}
		}
		if candidateAtDestination == "" {
			continue
		}
		if destinationFiles[candidateAtDestination].ModifiedTimestamp != sourceFiles[orphanAtSource].ModifiedTimestamp {
			timestampAction := action.PropagateTimestampAction{
				SourceBaseDirPath:           sourceDirPath,
				DestinationBaseDirPath:      destinationDirPath,
				SourceFileRelativePath:      orphanAtSource,
				DestinationFileRelativePath: candidateAtDestination,
			}
			if _, exists := uniquenessMap[timestampAction.Uniqueness()]; !exists {
				actions = append(actions, timestampAction)
				uniquenessMap.Add(timestampAction.Uniqueness())
				savings += sourceFiles[orphanAtSource].Size
			}
		}
		if _, existsAtSource := sourceFiles[candidateAtDestination]; !existsAtSource &&
			candidateAtDestination != orphanAtSource {
			parentDir := filepath.Dir(filepath.Join(destinationDirPath, orphanAtSource))
			if !utils.IsReadableDirectory(parentDir) {
				directoryAction := action.MakeDirectoryAction{
					AbsoluteDirPath: parentDir,
				}
				if _, exists := uniquenessMap[directoryAction.Uniqueness()]; !exists {
					actions = append(actions, directoryAction)
					uniquenessMap.Add(directoryAction.Uniqueness())
				}
			}
			moveFileAction := action.MoveFileAction{
				BasePath:         destinationDirPath,
				RelativeFromPath: candidateAtDestination,
				RelativeToPath:   orphanAtSource,
			}
			if _, exists := uniquenessMap[moveFileAction.Uniqueness()]; !exists {
				actions = append(actions, moveFileAction)
				uniquenessMap.Add(moveFileAction.Uniqueness())
				savings += sourceFiles[orphanAtSource].Size
			}
		}
	}
	return
}

func FindDirectoryResultToCsv(dirPath string, excludedFiles entity.StringSet, file *os.File) error {
	files, _, fErr := FindFilesFromDirectory(dirPath, excludedFiles)
	if fErr != nil {
		return fErr
	}
	cw := csv.NewWriter(file)
	for f, fileMeta := range files {
		record := []string{f, strconv.FormatInt(fileMeta.Size, 10),
			strconv.FormatInt(fileMeta.ModifiedTimestamp, 10)}
		wErr := cw.Write(record)
		if wErr != nil {
			return fmt.Errorf("error while writing record %+v: %+v", record, wErr)
		}
	}
	cw.Flush()
	return nil
}
