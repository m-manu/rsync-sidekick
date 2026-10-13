package service

import (
	"cmp"
	"encoding/csv"
	"errors"
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

// archiveMatchChunkSize is how many archive candidates are hashed and matched before the
// next batch starts. It exists so matches show up, and copies land on disk, soon rather
// than after the last hash of the whole path. The candidates are sorted by inode before
// they are cut into chunks, so smaller chunks keep the disk order; what a chunk costs is
// the workers waiting for its slowest file, about 1% at this size. A variable only so
// tests can shrink it.
var archiveMatchChunkSize = 1_000

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
		fmte.PrintfV("Evaluating file (#%s): %s\n", lib.GroupThousands(newValue), path)
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
		fmte.PrintfV("Evaluating file (#%s): %s\n", lib.GroupThousands(newValue), path)
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
	matcher := NewDestMatcher(sourceDirPath, sourceFiles, destinationDirPath, destinationFiles, destFS,
		copyDuplicates, useReflink)
	candidateDigests := make(map[string]entity.FileDigest, candidateFilesToDigests.Len())
	for candidate, digest := range candidateFilesToDigests.ForEach() {
		candidateDigests[candidate] = digest
	}
	actions = append(matcher.AddCandidates(candidateDigests), matcher.AddOrphans(orphanDigests)...)
	if actions == nil {
		actions = []action.SyncAction{}
	}
	return actions, matcher.Savings(), orphanDigests, nil
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
	return WalkArchivesKnowing(archivePaths, exclusions, destFS, counter, nil)
}

// WalkArchivesKnowing is WalkArchives with the destination's file list at hand: archive
// paths that overlap the destination or each other are then not read twice.
func WalkArchivesKnowing(archivePaths []string, exclusions set.Set[string], destFS rsfs.FileSystem,
	counter *int32, known *KnownTree,
) ([]ArchiveWalk, error) {
	readable := func(archivePath string) bool {
		ok := false
		if destFS != nil {
			ok = destFS.IsReadableDirectory(archivePath)
		} else {
			ok = lib.IsReadableDirectory(archivePath)
		}
		if !ok {
			// A single mistyped or unmounted --archive-path must not abort the run, but it
			// has to be loud: silently contributing nothing looks like "no matches found".
			fmte.PrintfErr("warning: archive path \"%s\" is not a readable directory - skipping it\n",
				archivePath)
		}
		return ok
	}
	if canReuseWalks(destFS) {
		return walkArchivesReusing(archivePaths, exclusions, counter, known, readable)
	}
	walks := make([]ArchiveWalk, 0, len(archivePaths))
	for _, archivePath := range archivePaths {
		if !readable(archivePath) {
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
			return nil, archiveWalkError(archivePath, err)
		}
		walks = append(walks, ArchiveWalk{Path: archivePath, Files: files})
	}
	return walks, nil
}

func archiveWalkError(archivePath string, err error) error {
	return fmt.Errorf("error scanning archive %s: %w", archivePath, err)
}

// ArchiveScanProgress carries the counters an archive scan advances while it runs.
// Callers read them atomically to print progress; a nil *ArchiveScanProgress is fine.
type ArchiveScanProgress struct {
	FilesFound    int32 // archive files seen while walking
	FilesChecked  int32 // archive files compared against the orphan index
	DigestsNeeded int32 // candidates found so far; grows by one archive path at a time
	DigestsDone   int32 // candidate digests computed
	Matches       int32 // orphans matched to an archive file
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

// ArchiveActionFunc receives each action an archive scan produces, the moment it is
// known. It lets callers apply a match right away instead of collecting everything for
// the end: an interrupted run then keeps every copy it already made. Actions arrive in
// dependency order — a directory before the file that goes into it. Returning an error
// aborts the scan, except for one wrapping ErrActionSkipped.
type ArchiveActionFunc func(action.SyncAction) error

// ErrActionSkipped, returned (wrapped) by an ArchiveActionFunc, means this one action
// could not be applied — say its archive file was deleted after it was hashed. The scan
// goes on, and the orphan stays unmatched so a later archive file or rsync can serve it.
var ErrActionSkipped = errors.New("action skipped")

// emit hands an action to onAction, or collects it when there is none.
func emit(a action.SyncAction, onAction ArchiveActionFunc, collected *[]action.SyncAction) error {
	if onAction == nil {
		*collected = append(*collected, a)
		return nil
	}
	return onAction(a)
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
	a, b := getParallelism(runtime.NumCPU())
	return BatchDigestsWorkers(fsys, baseDirPath, relPaths, counter, a+b)
}

// SideParallelism splits the hashing workers between source and destination, for when
// both sides are hashed at the same time.
func SideParallelism() (source, destination int) {
	return getParallelism(runtime.NumCPU())
}

// BatchDigestsWorkers is BatchDigestsParallel with a given number of workers.
func BatchDigestsWorkers(fsys rsfs.FileSystem, baseDirPath string, relPaths []string, counter *int32,
	workers int,
) map[string]entity.FileDigest {
	if len(relPaths) == 0 {
		return map[string]entity.FileDigest{}
	}
	workers = max(1, min(workers, len(relPaths)))
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
//
// With onAction set, actions are handed over as they are found and the returned slice
// stays empty — so an action can never be applied twice.
func ScanArchivesForCopiesWithDigests(archiveWalks []ArchiveWalk,
	unmatchedOrphans []string, knownOrphanDigests map[string]entity.FileDigest,
	digestFn OrphanDigestFunc, onAction ArchiveActionFunc,
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
		candidateOrphans := make(map[string][]string)
		var candidatePaths []string
		for archiveRelPath, archiveMeta := range archiveFiles {
			atomic.AddInt32(&progress.FilesChecked, 1)
			k := orphanKey{ext: lib.GetFileExt(archiveRelPath), size: archiveMeta.Size}
			orphans, ok := orphansByKey[k]
			if !ok || len(orphans) == 0 {
				continue
			}
			candidateOrphans[archiveRelPath] = orphans
			candidatePaths = append(candidatePaths, archiveRelPath)
		}
		if len(candidatePaths) == 0 {
			continue
		}

		// The candidate list is complete before any hashing starts, so it can serve as the
		// denominator for progress. Adding per archive path keeps it truthful when a later
		// path contributes more candidates.
		atomic.AddInt32(&progress.DigestsNeeded, int32(len(candidatePaths)))

		// Sort the whole list first, then cut it into chunks: each chunk is a contiguous,
		// ascending inode range, so working chunk by chunk still reads the disk in one
		// direction.
		sortForLocality(archivePath, candidatePaths, destFS)

		for start := 0; start < len(candidatePaths); start += archiveMatchChunkSize {
			end := min(start+archiveMatchChunkSize, len(candidatePaths))
			chunk := candidatePaths[start:end]

			// Which orphans can this chunk still serve, and which of those need a digest?
			// Re-checked per chunk, because earlier chunks have matched orphans since.
			pendingForChunk := make(map[string][]string, len(chunk))
			var neededOrphans []string
			for _, archiveRelPath := range chunk {
				pending := make([]string, 0, len(candidateOrphans[archiveRelPath]))
				for _, orphan := range candidateOrphans[archiveRelPath] {
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
				if len(pending) > 0 {
					pendingForChunk[archiveRelPath] = pending
				}
			}
			toHash := make([]string, 0, len(pendingForChunk))
			for _, archiveRelPath := range chunk {
				if _, ok := pendingForChunk[archiveRelPath]; ok {
					toHash = append(toHash, archiveRelPath)
				}
			}
			if skipped := len(chunk) - len(toHash); skipped > 0 {
				// Those candidates need no digest because every orphan they could serve is
				// already served. Take them off the total instead of counting them as
				// hashed, so the two numbers stay truthful and still meet at the end.
				atomic.AddInt32(&progress.DigestsNeeded, int32(-skipped))
			}
			if len(toHash) == 0 {
				continue
			}
			slices.Sort(neededOrphans) // deterministic batches for digestFn

			// Both sides are independent, so hash them at the same time.
			var archiveDigests, freshOrphanDigests map[string]entity.FileDigest
			var orphanDigestErr error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				archiveDigests = BatchDigestsParallel(destFS, archivePath, toHash, &progress.DigestsDone)
			}()
			go func() {
				defer wg.Done()
				if len(neededOrphans) > 0 {
					freshOrphanDigests, orphanDigestErr = digestFn(neededOrphans)
				}
			}()
			wg.Wait()
			if orphanDigestErr != nil {
				return nil, fmt.Errorf("error computing digests of %s orphan candidate(s): %w",
					lib.GroupThousands(len(neededOrphans)), orphanDigestErr)
			}
			for orphan, digest := range freshOrphanDigests {
				orphanDigests[orphan] = digest
			}

			if err := matchChunk(chunk, pendingForChunk, archiveDigests, orphanDigests,
				matchedOrphans, uniqueness, onAction, &actions, progress,
				archivePath, destDirPath, useReflink, destFS, sourceFiles); err != nil {
				return nil, err
			}
		}
	}

	return actions, nil
}

// matchChunk compares one chunk of hashed archive files against the orphan digests and
// emits the actions for every match. Kept separate so the scan loop above stays readable.
func matchChunk(chunk []string, pendingForChunk map[string][]string,
	archiveDigests, orphanDigests map[string]entity.FileDigest,
	matchedOrphans, uniqueness set.Set[string], onAction ArchiveActionFunc,
	actions *[]action.SyncAction, progress *ArchiveScanProgress,
	archivePath, destDirPath string, useReflink bool, destFS rsfs.FileSystem,
	sourceFiles map[string]entity.FileMeta,
) error {
	for _, archiveRelPath := range chunk {
		archiveDigest, haveDigest := archiveDigests[archiveRelPath]
		if !haveDigest {
			continue
		}
		archiveAbsPath := filepath.Join(archivePath, archiveRelPath)
		for _, orphan := range pendingForChunk[archiveRelPath] {
			if matchedOrphans.Contains(orphan) {
				continue
			}
			orphanDigest, ok := orphanDigests[orphan]
			if !ok || orphanDigest != archiveDigest {
				continue
			}
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
					if err := emit(mkdirAction, onAction, actions); err != nil {
						if errors.Is(err, ErrActionSkipped) {
							continue
						}
						return err
					}
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
				if err := emit(copyAction, onAction, actions); err != nil {
					if errors.Is(err, ErrActionSkipped) {
						continue
					}
					return err
				}
				uniqueness.Add(copyAction.Uniqueness())
				matchedOrphans.Add(orphan)
				atomic.AddInt32(&progress.Matches, 1)
			}
		}
	}

	return nil
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
