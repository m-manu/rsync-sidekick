package service

import (
	"encoding/csv"
	"fmt"
	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/action"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/m-manu/rsync-sidekick/lib"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
)

const (
	indexBuildErrorCountTolerance = 20
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
	filesToDigests lib.SafeMap[string, entity.FileDigest], digestsToFiles lib.MultiMap[entity.FileDigest, string],
) error {
	errCount := 0
	for _, relativePath := range filesToScan {
		newValue := atomic.AddInt32(counter, 1)
		path := filepath.Join(baseDirPath, relativePath)
		fmte.PrintfV("Evaluating file (#%d): %s\n", newValue, path)
		digest, err := getDigest(path)
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
	orphanFilesToDigests := lib.NewSafeMap[string, entity.FileDigest]()
	candidateFilesToDigests := lib.NewSafeMap[string, entity.FileDigest]()
	orphanDigestsToFiles := lib.NewMultiMap[entity.FileDigest, string]()
	candidateDigestsToFiles := lib.NewMultiMap[entity.FileDigest, string]()
	var sourceIndexErrs, destinationIndexErrs []error
	parallelismForSource, parallelismForDestination := getParallelism(runtime.NumCPU())
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
	uniqueness := set.NewSetWithSize[string](orphanFilesToDigests.Len())
	for orphanAtSource, orphanDigest := range orphanFilesToDigests.Data {
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
			if !uniqueness.Contains(timestampAction.Uniqueness()) {
				actions = append(actions, timestampAction)
				uniqueness.Add(timestampAction.Uniqueness())
				savings += sourceFiles[orphanAtSource].Size
			}
		}
		if _, existsAtSource := sourceFiles[candidateAtDestination]; !existsAtSource &&
			candidateAtDestination != orphanAtSource {
			parentDir := filepath.Dir(filepath.Join(destinationDirPath, orphanAtSource))
			if !lib.IsReadableDirectory(parentDir) {
				directoryAction := action.MakeDirectoryAction{
					AbsoluteDirPath: parentDir,
				}
				if !uniqueness.Contains(directoryAction.Uniqueness()) {
					actions = append(actions, directoryAction)
					uniqueness.Add(directoryAction.Uniqueness())
				}
			}
			moveFileAction := action.MoveFileAction{
				BasePath:         destinationDirPath,
				RelativeFromPath: candidateAtDestination,
				RelativeToPath:   orphanAtSource,
			}
			if !uniqueness.Contains(moveFileAction.Uniqueness()) {
				actions = append(actions, moveFileAction)
				uniqueness.Add(moveFileAction.Uniqueness())
				savings += sourceFiles[orphanAtSource].Size
			}
		}
	}
	return
}

func getParallelism(n int) (int, int) {
	if n > 3 {
		if n%2 == 0 {
			return n/2 - 1, n / 2
		} else {
			return n / 2, n / 2
		}
	}
	return 1, 1
}

func FindDirectoryResultToCsv(dirPath string, excludedFiles set.Set[string], file *os.File) error {
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
