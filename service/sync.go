package service

import (
	"cmp"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/m-manu/rsync-sidekick/v2/fmte"
	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
	"github.com/m-manu/rsync-sidekick/v2/lib"
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
			fmte.PrintfErrV("couldn't index file \"%s\" (skipping): %+v\n", path, err)
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

// ComputeSyncActionsWithFS is like ComputeSyncActions but uses the given FileSystems.
// If sourceFS or destFS is nil, local OS calls are used for the respective side.
// The returned orphanDigests map holds the digests computed for orphansAtSource, so a
// later archive scan can reuse them instead of hashing the same files a second time.
func ComputeSyncActionsWithFS(sourceFS, destFS rsfs.FileSystem,
	sourceDirPath string, sourceFiles map[string]entity.FileMeta, orphansAtSource []string,
	destinationDirPath string, destinationFiles map[string]entity.FileMeta, candidatesAtDestination []string,
	sourceCounter *int32, destinationCounter *int32,
	copyDuplicates bool, useReflink bool,
) (actions []action.SyncAction, savings int64, orphanDigests map[string]entity.FileDigest, err error) {
	orphanFilesToDigests := lib.NewSafeMap[string, entity.FileDigest]()
	candidateFilesToDigests := lib.NewSafeMap[string, entity.FileDigest]()
	orphanDigestsToFiles := lib.NewMultiMap[entity.FileDigest, string]()
	candidateDigestsToFiles := lib.NewMultiMap[entity.FileDigest, string]()
	var sourceIndexErrs, destinationIndexErrs []error
	var sourceIndexErrsMutex, destinationIndexErrsMutex sync.Mutex
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
				sourceIndexErrsMutex.Lock()
				sourceIndexErrs = append(sourceIndexErrs, sourceIndexErr)
				sourceIndexErrsMutex.Unlock()
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
				destinationIndexErrsMutex.Lock()
				destinationIndexErrs = append(destinationIndexErrs, destinationIndexErr)
				destinationIndexErrsMutex.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(sourceIndexErrs) > 0 {
		return nil, 0, nil, fmte.Errors("error(s) while building index on source directory: ",
			sourceIndexErrs)
	}
	if len(destinationIndexErrs) > 0 {
		return nil, 0, nil, fmte.Errors("error(s) while building index on destination directory: ",
			destinationIndexErrs)
	}
	// Hand the orphan digests back for reuse. Files that failed to hash are stored as a
	// zero digest by buildIndex — leave those out so a later phase can retry them.
	orphanDigests = make(map[string]entity.FileDigest, orphanFilesToDigests.Len())
	for orphanAtSource, orphanDigest := range orphanFilesToDigests.ForEach() {
		if orphanDigest != (entity.FileDigest{}) {
			orphanDigests[orphanAtSource] = orphanDigest
		}
	}
	actions = make([]action.SyncAction, 0, orphanFilesToDigests.Len())
	uniqueness := set.NewSetWithSize[string](orphanFilesToDigests.Len())
	movedCandidates := set.NewSet[string]() // prevents double-moves; copies still allowed
	for orphanAtSource, orphanDigest := range orphanFilesToDigests.ForEach() {
		if !candidateDigestsToFiles.Exists(orphanDigest) {
			// let rsync handle this
			continue
		}
		matchesAtDestination := candidateDigestsToFiles.Get(orphanDigest)
		candidateAtDestination := PickBestCandidate(matchesAtDestination, orphanAtSource, sourceFiles)
		if candidateAtDestination == "" {
			continue
		}
		_, candidateExistsAtSource := sourceFiles[candidateAtDestination]
		// Timestamp propagation — skip for already-moved candidates (file no longer at original path)
		if !movedCandidates.Contains(candidateAtDestination) {
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
		}
		shouldMove := !candidateExistsAtSource && !movedCandidates.Contains(candidateAtDestination) && candidateAtDestination != orphanAtSource
		if shouldMove {
			// Move: candidate doesn't exist at source and hasn't been moved yet
			movedCandidates.Add(candidateAtDestination)
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
		} else if copyDuplicates && candidateAtDestination != orphanAtSource {
			// Copy: candidate exists at source (can't move) or was already moved.
			// Uses original candidate path; performActions redirect handles ordering.
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
// It prefers candidates with the same basename as the orphan, and prefers candidates
// that don't exist at source (better move targets). Already-moved candidates are NOT
// filtered out — the caller decides whether to move or copy based on movedCandidates.
func PickBestCandidate(candidates []string, orphanPath string, sourceFiles map[string]entity.FileMeta) string {
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	// Multiple candidates: prefer one with same basename that doesn't exist at source
	orphanBase := filepath.Base(orphanPath)
	for _, c := range candidates {
		if filepath.Base(c) == orphanBase {
			if _, existsAtSource := sourceFiles[c]; !existsAtSource {
				return c
			}
		}
	}
	// Fall back: any that doesn't exist at source
	for _, c := range candidates {
		if _, existsAtSource := sourceFiles[c]; !existsAtSource {
			return c
		}
	}
	// All candidates exist at source too — return one anyway; caller decides
	// whether to copy (--copy-duplicates) or skip.
	return candidates[0]
}

// ArchiveWalk holds the files found below one archive path. Archive paths are kept in
// the order the user gave them, since earlier paths get to serve an orphan first.
type ArchiveWalk struct {
	Path  string
	Files map[string]entity.FileMeta
}

// WalkArchives scans the archive paths without hashing anything. It is separate from
// ScanArchivesForCopiesWithDigests so callers can start it as early as they like — the
// walk only needs the destination side to be free, not the source scan to be finished.
func WalkArchives(archivePaths []string, exclusions set.Set[string], destFS rsfs.FileSystem,
	counter *int32,
) ([]ArchiveWalk, error) {
	walks := make([]ArchiveWalk, 0, len(archivePaths))
	for _, archivePath := range archivePaths {
		readable := false
		if destFS != nil {
			readable = destFS.IsReadableDirectory(archivePath)
		} else {
			readable = lib.IsReadableDirectory(archivePath)
		}
		if !readable {
			// A single mistyped or unmounted --archive-path must not abort the run, but it
			// has to be loud: silently contributing nothing looks like "no matches found".
			fmte.PrintfErr("warning: archive path \"%s\" is not a readable directory - skipping it\n",
				archivePath)
			continue
		}

		var files map[string]entity.FileMeta
		var err error
		if destFS != nil {
			files, _, err = FindFilesFromDirectoryWithFS(destFS, archivePath, exclusions, counter)
		} else {
			fsys := rsfs.NewLocalFSForArchive()
			files, _, err = FindFilesFromDirectoryWithFS(fsys, archivePath, exclusions, counter)
			fsys.Close()
		}
		if err != nil {
			return nil, fmt.Errorf("error scanning archive %s: %w", archivePath, err)
		}
		walks = append(walks, ArchiveWalk{Path: archivePath, Files: files})
	}
	return walks, nil
}

// ArchiveScanProgress carries the counters an archive scan advances while it runs.
// Callers read them atomically to print progress; a nil *ArchiveScanProgress is fine.
type ArchiveScanProgress struct {
	FilesFound   int32 // archive files seen while walking
	FilesChecked int32 // archive files compared against the orphan index
	FilesHashed  int32 // archive files that turned out to be candidates and were hashed
	Matches      int32 // orphans matched to an archive file
}

// sortForLocality orders relPaths so hashing reads the disk in one direction: by inode
// where that is available, by path otherwise. Both beat the caller's starting point,
// since iterating a Go map yields a random order — the worst case for a spinning disk.
// fsys non-nil means the files are remote, where inodes aren't worth the round-trips.
func sortForLocality(basePath string, relPaths []string, fsys rsfs.FileSystem) {
	slices.Sort(relPaths)
	if fsys != nil {
		return
	}
	inodes := make(map[string]uint64, len(relPaths))
	for _, relPath := range relPaths {
		inode, ok := fileInode(filepath.Join(basePath, relPath))
		if !ok {
			return // keep the path order already applied
		}
		inodes[relPath] = inode
	}
	slices.SortFunc(relPaths, func(a, b string) int {
		return cmp.Compare(inodes[a], inodes[b])
	})
}

// OrphanDigestFunc computes digests for the given orphan relative paths on demand.
// ScanArchivesForCopiesWithDigests calls it only for orphans that an archive path can
// actually match, so callers must not assume it is invoked for every orphan.
type OrphanDigestFunc func(orphans []string) (map[string]entity.FileDigest, error)

// BatchDigestsParallel computes digests for relPaths below baseDirPath, spreading the
// work over the same number of workers as the index build. fsys is used when non-nil,
// otherwise local OS calls are made. Files that cannot be hashed are skipped (they
// simply end up absent from the returned map), mirroring index building.
func BatchDigestsParallel(fsys rsfs.FileSystem, baseDirPath string, relPaths []string, counter *int32) map[string]entity.FileDigest {
	if len(relPaths) == 0 {
		return map[string]entity.FileDigest{}
	}
	a, b := getParallelism(runtime.NumCPU())
	workers := min(a+b, len(relPaths))
	digests := lib.NewSafeMap[string, entity.FileDigest]()
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wg.Done()
			low := index * len(relPaths) / workers
			high := (index + 1) * len(relPaths) / workers
			for _, relativePath := range relPaths[low:high] {
				path := filepath.Join(baseDirPath, relativePath)
				var digest entity.FileDigest
				var err error
				if fsys != nil {
					digest, err = getDigestWithFS(fsys, path)
				} else {
					digest, err = getDigest(path)
				}
				if counter != nil {
					atomic.AddInt32(counter, 1)
				}
				if err != nil {
					fmte.PrintfErrV("couldn't hash file \"%s\" (skipping): %+v\n", path, err)
					continue
				}
				digests.Set(relativePath, digest)
			}
		}(i)
	}
	wg.Wait()
	result := make(map[string]entity.FileDigest, digests.Len())
	for relativePath, digest := range digests.ForEach() {
		result[relativePath] = digest
	}
	return result
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
// unmatched orphans. Archive paths are on the destination side; if destFS is non-nil it
// is used to scan archives and check directories (e.g. SFTP mode).
//
// Matching is a two-stage filter: an orphan is only a possible match if some archive
// file shares its extension and size, and only then do the digests decide. Digests are
// therefore obtained lazily — knownOrphanDigests is consulted first, and digestFn is
// called only for the orphans an archive path can actually match. Hashing every orphan
// up front is what made this phase dominate the runtime on large trees.
//
// Per archive path the two sides are hashed concurrently: the archive candidates and the
// orphan digests still missing don't depend on each other.
//
// archiveWalks comes from WalkArchives, which callers typically run early and in parallel
// with the source scan.
func ScanArchivesForCopiesWithDigests(archiveWalks []ArchiveWalk,
	unmatchedOrphans []string, knownOrphanDigests map[string]entity.FileDigest,
	digestFn OrphanDigestFunc,
	sourceFiles map[string]entity.FileMeta,
	destDirPath string, useReflink bool, destFS rsfs.FileSystem,
	progress *ArchiveScanProgress,
) ([]action.SyncAction, error) {
	if len(unmatchedOrphans) == 0 || len(archiveWalks) == 0 {
		return nil, nil
	}
	if progress == nil {
		progress = &ArchiveScanProgress{}
	}

	// Local copy: the caller's map must not be mutated, and lazily computed digests are
	// cached here so a later archive path doesn't hash the same orphan again.
	orphanDigests := make(map[string]entity.FileDigest, len(knownOrphanDigests))
	for orphan, digest := range knownOrphanDigests {
		orphanDigests[orphan] = digest
	}
	// Orphans already handed to digestFn — a second attempt would fail the same way.
	requestedDigests := set.NewSet[string]()

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

	for _, archiveWalk := range archiveWalks {
		archivePath, archiveFiles := archiveWalk.Path, archiveWalk.Files

		// An archive file is worth hashing only if its extension and size match an orphan
		// that is still unmatched. Everything else never reaches a digest comparison.
		type archiveCandidate struct {
			relPath string
			orphans []string
		}
		var candidates []archiveCandidate
		var neededOrphans []string
		for archiveRelPath, archiveMeta := range archiveFiles {
			atomic.AddInt32(&progress.FilesChecked, 1)
			k := orphanKey{ext: lib.GetFileExt(archiveRelPath), size: archiveMeta.Size}
			orphans, ok := orphansByKey[k]
			if !ok {
				continue
			}
			pending := make([]string, 0, len(orphans))
			for _, orphan := range orphans {
				if matchedOrphans.Contains(orphan) {
					continue
				}
				pending = append(pending, orphan)
				if digestFn == nil || requestedDigests.Contains(orphan) {
					continue
				}
				if _, known := orphanDigests[orphan]; known {
					continue
				}
				requestedDigests.Add(orphan)
				neededOrphans = append(neededOrphans, orphan)
			}
			if len(pending) == 0 {
				// Every orphan of this size and extension is already served.
				continue
			}
			candidates = append(candidates, archiveCandidate{relPath: archiveRelPath, orphans: pending})
		}
		if len(candidates) == 0 {
			continue
		}

		candidatePaths := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			candidatePaths = append(candidatePaths, candidate.relPath)
		}
		sortForLocality(archivePath, candidatePaths, destFS)
		slices.Sort(neededOrphans) // deterministic batches for digestFn

		// Both sides are independent, so hash them at the same time.
		var archiveDigests, freshOrphanDigests map[string]entity.FileDigest
		var orphanDigestErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			archiveDigests = BatchDigestsParallel(destFS, archivePath, candidatePaths, &progress.FilesHashed)
		}()
		go func() {
			defer wg.Done()
			if len(neededOrphans) > 0 {
				freshOrphanDigests, orphanDigestErr = digestFn(neededOrphans)
			}
		}()
		wg.Wait()
		if orphanDigestErr != nil {
			return nil, fmt.Errorf("error computing digests of %d orphan candidate(s): %w",
				len(neededOrphans), orphanDigestErr)
		}
		for orphan, digest := range freshOrphanDigests {
			orphanDigests[orphan] = digest
		}

		for _, candidate := range candidates {
			archiveRelPath := candidate.relPath
			archiveDigest, hashed := archiveDigests[archiveRelPath]
			if !hashed {
				continue
			}
			archiveAbsPath := filepath.Join(archivePath, archiveRelPath)
			for _, orphan := range candidate.orphans {
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
						atomic.AddInt32(&progress.Matches, 1)
					}
				}
			}
		}
	}

	return actions, nil
}

func FindDirectoryResultToCsv(dirPath string, excludedFiles set.Set[string], file *os.File) error {
	files, _, fErr := FindFilesFromDirectory(dirPath, excludedFiles, nil)
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
