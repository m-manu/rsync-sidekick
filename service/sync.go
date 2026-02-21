package service

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/action"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/fmte"
	rsfs "github.com/m-manu/rsync-sidekick/fs"
	"github.com/m-manu/rsync-sidekick/lib"
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

func buildIndexWithFS(fsys rsfs.FileSystem, baseDirPath string, filesToScan []string, counter *int32,
	filesToDigests lib.SafeMap[string, entity.FileDigest], digestsToFiles lib.MultiMap[entity.FileDigest, string],
) error {
	errCount := 0
	for _, relativePath := range filesToScan {
		newValue := atomic.AddInt32(counter, 1)
		path := filepath.Join(baseDirPath, relativePath)
		fmte.PrintfV("Evaluating file (#%d): %s\n", newValue, path)
		digest, err := getDigestWithFS(fsys, path)
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
	copyDuplicates bool, useReflink bool,
) (actions []action.SyncAction, savings int64, err error) {
	return ComputeSyncActionsWithFS(nil, nil, sourceDirPath, sourceFiles, orphansAtSource,
		destinationDirPath, destinationFiles, candidatesAtDestination,
		sourceCounter, destinationCounter, copyDuplicates, useReflink)
}

// ComputeSyncActionsWithFS is like ComputeSyncActions but uses the given FileSystems.
// If sourceFS or destFS is nil, local OS calls are used for the respective side.
func ComputeSyncActionsWithFS(sourceFS, destFS rsfs.FileSystem,
	sourceDirPath string, sourceFiles map[string]entity.FileMeta, orphansAtSource []string,
	destinationDirPath string, destinationFiles map[string]entity.FileMeta, candidatesAtDestination []string,
	sourceCounter *int32, destinationCounter *int32,
	copyDuplicates bool, useReflink bool,
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
			var sourceIndexErr error
			if sourceFS != nil {
				sourceIndexErr = buildIndexWithFS(sourceFS, sourceDirPath, orphansAtSource[low:high], sourceCounter,
					orphanFilesToDigests, orphanDigestsToFiles)
			} else {
				sourceIndexErr = buildIndex(sourceDirPath, orphansAtSource[low:high], sourceCounter,
					orphanFilesToDigests, orphanDigestsToFiles)
			}
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
			var destinationIndexErr error
			if destFS != nil {
				destinationIndexErr = buildIndexWithFS(destFS, destinationDirPath, candidatesAtDestination[low:high], destinationCounter,
					candidateFilesToDigests, candidateDigestsToFiles)
			} else {
				destinationIndexErr = buildIndex(destinationDirPath, candidatesAtDestination[low:high], destinationCounter,
					candidateFilesToDigests, candidateDigestsToFiles)
			}
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
	usedCandidates := set.NewSet[string]()
	for orphanAtSource, orphanDigest := range orphanFilesToDigests.ForEach() {
		if !candidateDigestsToFiles.Exists(orphanDigest) {
			// let rsync handle this
			continue
		}
		matchesAtDestination := candidateDigestsToFiles.Get(orphanDigest)
		candidateAtDestination := PickBestCandidate(matchesAtDestination, orphanAtSource, sourceFiles, usedCandidates)
		if candidateAtDestination == "" {
			continue
		}
		_, candidateExistsAtSource := sourceFiles[candidateAtDestination]
		if destinationFiles[candidateAtDestination].ModifiedTimestamp != sourceFiles[orphanAtSource].ModifiedTimestamp {
			// Avoid propagating timestamp to a destination file that already matches its counterpart at source
			if srcMetaForCandidate, existsAtSourceForCandidate := sourceFiles[candidateAtDestination]; !(existsAtSourceForCandidate && srcMetaForCandidate == destinationFiles[candidateAtDestination]) {
				timestampAction := action.PropagateTimestampAction{
					SourceBaseDirPath:           sourceDirPath,
					DestinationBaseDirPath:      destinationDirPath,
					SourceFileRelativePath:      orphanAtSource,
					DestinationFileRelativePath: candidateAtDestination,
					SourceModTime:               time.Unix(sourceFiles[orphanAtSource].ModifiedTimestamp, 0),
					FS:                          destFS,
				}
				if !uniqueness.Contains(timestampAction.Uniqueness()) {
					actions = append(actions, timestampAction)
					uniqueness.Add(timestampAction.Uniqueness())
					savings += sourceFiles[orphanAtSource].Size
				}
			}
		}
		if !candidateExistsAtSource && candidateAtDestination != orphanAtSource {
			// Move: candidate doesn't exist at source, safe to move
			usedCandidates.Add(candidateAtDestination)
			parentDir := filepath.Dir(filepath.Join(destinationDirPath, orphanAtSource))
			isReadable := false
			if destFS != nil {
				isReadable = destFS.IsReadableDirectory(parentDir)
			} else {
				isReadable = lib.IsReadableDirectory(parentDir)
			}
			if !isReadable {
				directoryAction := action.MakeDirectoryAction{
					AbsoluteDirPath: parentDir,
					FS:              destFS,
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
				FS:               destFS,
			}
			if !uniqueness.Contains(moveFileAction.Uniqueness()) {
				actions = append(actions, moveFileAction)
				uniqueness.Add(moveFileAction.Uniqueness())
				savings += sourceFiles[orphanAtSource].Size
			}
		} else if copyDuplicates && candidateExistsAtSource && candidateAtDestination != orphanAtSource {
			// Copy: candidate exists at source too, so we can't move it — copy instead
			absSource := filepath.Join(destinationDirPath, candidateAtDestination)
			absDest := filepath.Join(destinationDirPath, orphanAtSource)
			parentDir := filepath.Dir(absDest)
			isReadable := false
			if destFS != nil {
				isReadable = destFS.IsReadableDirectory(parentDir)
			} else {
				isReadable = lib.IsReadableDirectory(parentDir)
			}
			if !isReadable {
				directoryAction := action.MakeDirectoryAction{
					AbsoluteDirPath: parentDir,
					FS:              destFS,
				}
				if !uniqueness.Contains(directoryAction.Uniqueness()) {
					actions = append(actions, directoryAction)
					uniqueness.Add(directoryAction.Uniqueness())
				}
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
				savings += sourceFiles[orphanAtSource].Size
			}
		}
	}
	return
}

// PickBestCandidate selects the best candidate from a list of destination paths.
// It skips already-used candidates, prefers candidates with the same basename as
// the orphan, and avoids candidates that already exist at source (unless only one
// candidate remains).
func PickBestCandidate(candidates []string, orphanPath string, sourceFiles map[string]entity.FileMeta, usedCandidates set.Set[string]) string {
	// Filter out already-used candidates
	available := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if !usedCandidates.Contains(c) {
			available = append(available, c)
		}
	}
	if len(available) == 0 {
		return ""
	}
	if len(available) == 1 {
		return available[0]
	}
	// Multiple available: prefer one with same basename that doesn't exist at source
	orphanBase := filepath.Base(orphanPath)
	for _, c := range available {
		if filepath.Base(c) == orphanBase {
			if _, existsAtSource := sourceFiles[c]; !existsAtSource {
				return c
			}
		}
	}
	// Fall back: any that doesn't exist at source
	for _, c := range available {
		if _, existsAtSource := sourceFiles[c]; !existsAtSource {
			return c
		}
	}
	// All candidates exist at source too — return one anyway; caller decides
	// whether to copy (--copy-duplicates) or skip.
	return available[0]
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

// ScanArchivesForCopiesWithDigests scans archive directories for files matching
// unmatched orphans whose digests are already known. Archive paths are on the
// destination side; if destFS is non-nil it is used to scan archives and check
// directories (e.g. SFTP mode).
func ScanArchivesForCopiesWithDigests(archivePaths []string, exclusions set.Set[string],
	unmatchedOrphans []string, orphanDigests map[string]entity.FileDigest,
	sourceFiles map[string]entity.FileMeta,
	destDirPath string, useReflink bool, destFS rsfs.FileSystem,
) ([]action.SyncAction, error) {
	if len(unmatchedOrphans) == 0 || len(archivePaths) == 0 {
		return nil, nil
	}

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
		var archiveFiles map[string]entity.FileMeta
		var err error
		if destFS != nil {
			archiveFiles, _, err = FindFilesFromDirectoryWithFS(destFS, archivePath, exclusions)
		} else {
			archiveFiles, _, err = FindFilesFromDirectory(archivePath, exclusions)
		}
		if err != nil {
			return nil, fmt.Errorf("error scanning archive %s: %w", archivePath, err)
		}

		for archiveRelPath, archiveMeta := range archiveFiles {
			k := orphanKey{ext: lib.GetFileExt(archiveRelPath), size: archiveMeta.Size}
			orphans, ok := orphansByKey[k]
			if !ok {
				continue
			}

			archiveAbsPath := filepath.Join(archivePath, archiveRelPath)
			var archiveDigest entity.FileDigest
			if destFS != nil {
				archiveDigest, err = GetDigestWithFS(destFS, archiveAbsPath)
			} else {
				archiveDigest, err = GetDigest(archiveAbsPath)
			}
			if err != nil {
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
					absDest := filepath.Join(destDirPath, orphan)
					parentDir := filepath.Dir(absDest)
					isReadable := false
					if destFS != nil {
						isReadable = destFS.IsReadableDirectory(parentDir)
					} else {
						isReadable = lib.IsReadableDirectory(parentDir)
					}
					if !isReadable {
						mkdirAction := action.MakeDirectoryAction{AbsoluteDirPath: parentDir, FS: destFS}
						if !uniqueness.Contains(mkdirAction.Uniqueness()) {
							actions = append(actions, mkdirAction)
							uniqueness.Add(mkdirAction.Uniqueness())
						}
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
