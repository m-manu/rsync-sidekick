package service

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archiveScanFixture lays out a source directory whose files are all orphans, and an
// archive directory holding a byte-identical copy of exactly one of them.
type archiveScanFixture struct {
	sourceDir   string
	archiveDir  string
	destDir     string
	sourceFiles map[string]entity.FileMeta
	orphans     []string
}

func newArchiveScanFixture(t *testing.T) archiveScanFixture {
	t.Helper()
	base := t.TempDir()
	f := archiveScanFixture{
		sourceDir:   filepath.Join(base, "source"),
		archiveDir:  filepath.Join(base, "archive"),
		destDir:     filepath.Join(base, "dest"),
		sourceFiles: map[string]entity.FileMeta{},
	}
	for _, dir := range []string{f.sourceDir, f.archiveDir, f.destDir} {
		require.NoError(t, os.MkdirAll(dir, 0o755), "creating %s", dir)
	}

	// "match.txt" has a byte-identical copy in the archive, so extension and size line up.
	// "other.txt" shares the extension but not the size; "same-size.bin" shares the size
	// but not the extension. Neither can ever match, so neither may be hashed.
	files := map[string]string{
		"match.txt":     "hello world",
		"other.txt":     "hello world, but noticeably longer",
		"same-size.bin": "hello world",
	}
	for name, content := range files {
		path := filepath.Join(f.sourceDir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644), "writing %s", path)
		info, err := os.Lstat(path)
		require.NoError(t, err, "stat %s", path)
		f.sourceFiles[name] = entity.FileMeta{Size: info.Size(), ModifiedTimestamp: info.ModTime().Unix()}
		f.orphans = append(f.orphans, name)
	}
	sort.Strings(f.orphans)

	archiveCopy := filepath.Join(f.archiveDir, "copy-of-match.txt")
	require.NoError(t, os.WriteFile(archiveCopy, []byte(files["match.txt"]), 0o644), "writing %s", archiveCopy)
	return f
}

// recordingDigestFn returns a digest function that records every orphan it was asked
// about, so a test can assert on which files were hashed at all.
func recordingDigestFn(sourceDir string, requested *[]string) OrphanDigestFunc {
	return func(orphans []string) (map[string]entity.FileDigest, error) {
		*requested = append(*requested, orphans...)
		return BatchDigestsParallel(nil, sourceDir, orphans, nil), nil
	}
}

// walkFixtureArchives pre-walks archive paths the way a caller does before scanning.
func walkFixtureArchives(t *testing.T, archivePaths ...string) []ArchiveWalk {
	t.Helper()
	walks, err := WalkArchives(archivePaths, set.NewSet[string](), nil, nil)
	require.NoError(t, err, "walking %v", archivePaths)
	return walks
}

func copyActions(actions []action.SyncAction) []action.CopyFileAction {
	var copies []action.CopyFileAction
	for _, a := range actions {
		if cfa, ok := a.(action.CopyFileAction); ok {
			copies = append(copies, cfa)
		}
	}
	return copies
}

func TestScanArchives_HashesOnlyOrphansAnArchiveCanMatch(t *testing.T) {
	f := newArchiveScanFixture(t)
	var requested []string
	var progress ArchiveScanProgress

	actions, err := ScanArchivesForCopiesWithDigests(
		walkFixtureArchives(t, f.archiveDir),
		f.orphans, nil, recordingDigestFn(f.sourceDir, &requested), nil,
		f.sourceFiles, f.destDir, false, nil,
		&progress,
	)
	require.NoError(t, err)

	assert.Equal(t, []string{"match.txt"}, requested,
		"only the orphan matching an archive file on extension and size may be hashed")

	copies := copyActions(actions)
	require.Len(t, copies, 1, "expected exactly one copy action, got actions: %+v", actions)
	assert.Equal(t, filepath.Join(f.archiveDir, "copy-of-match.txt"), copies[0].AbsSourcePath)
	assert.Equal(t, filepath.Join(f.destDir, "match.txt"), copies[0].AbsDestPath)
	assert.EqualValues(t, 1, progress.Matches, "one orphan was matched")
	assert.EqualValues(t, 1, progress.FilesChecked, "the archive file was checked against the orphan index")
	assert.EqualValues(t, 1, progress.FilesToHash,
		"the candidate count must be known before hashing starts, to serve as a denominator")
	assert.EqualValues(t, 1, progress.FilesHashed, "and it was a candidate, so it was hashed")
}

func TestScanArchives_SkipsArchiveFilesWhoseOrphansAreServed(t *testing.T) {
	f := newArchiveScanFixture(t)
	// A second archive file with the same extension and size as the first. Once the only
	// orphan of that size is matched, the second file must not be hashed at all.
	require.NoError(t, os.WriteFile(filepath.Join(f.archiveDir, "another-copy.txt"),
		[]byte("hello world"), 0o644))

	var requested []string
	var progress ArchiveScanProgress
	actions, err := ScanArchivesForCopiesWithDigests(
		walkFixtureArchives(t, f.archiveDir),
		f.orphans, nil, recordingDigestFn(f.sourceDir, &requested), nil,
		f.sourceFiles, f.destDir, false, nil,
		&progress,
	)
	require.NoError(t, err)

	require.Len(t, copyActions(actions), 1,
		"one orphan can only be served once, got actions: %+v", actions)
	assert.EqualValues(t, 2, progress.FilesChecked, "both archive files were checked")
	assert.EqualValues(t, 1, progress.Matches, "the orphan can only be served once")
	// Both are hashed: within one archive path the candidate list is built before hashing,
	// so at that point the orphan is still unmatched and both files qualify. Hashing them
	// as one parallel batch is worth more than serialising to save the second hash.
	assert.EqualValues(t, 2, progress.FilesHashed)
}

func TestScanArchives_StreamsActionsInsteadOfReturningThem(t *testing.T) {
	f := newArchiveScanFixture(t)
	var requested, streamed []string
	var progress ArchiveScanProgress

	onAction := func(a action.SyncAction) error {
		streamed = append(streamed, a.Uniqueness())
		return nil
	}
	actions, err := ScanArchivesForCopiesWithDigests(
		walkFixtureArchives(t, f.archiveDir),
		f.orphans, nil, recordingDigestFn(f.sourceDir, &requested), onAction,
		f.sourceFiles, f.destDir, false, nil,
		&progress,
	)
	require.NoError(t, err)

	assert.Len(t, streamed, 1, "the match must be handed over as it is found, got %v", streamed)
	assert.Empty(t, actions,
		"a streamed action must not also be returned, or it would be applied twice")
	assert.EqualValues(t, 1, progress.Matches)
}

func TestScanArchives_StreamErrorAbortsTheScan(t *testing.T) {
	f := newArchiveScanFixture(t)
	var requested []string
	var progress ArchiveScanProgress

	failing := func(action.SyncAction) error { return assert.AnError }
	_, err := ScanArchivesForCopiesWithDigests(
		walkFixtureArchives(t, f.archiveDir),
		f.orphans, nil, recordingDigestFn(f.sourceDir, &requested), failing,
		f.sourceFiles, f.destDir, false, nil,
		&progress,
	)

	require.ErrorIs(t, err, assert.AnError,
		"a failing copy must stop the scan rather than be silently dropped")
}

func TestWalkArchives_KeepsPathOrderAndCounts(t *testing.T) {
	f := newArchiveScanFixture(t)
	second := filepath.Join(t.TempDir(), "archive2")
	require.NoError(t, os.MkdirAll(second, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(second, "a.txt"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(second, "b.txt"), []byte("b"), 0o644))

	var counter int32
	walks, err := WalkArchives([]string{f.archiveDir, second}, set.NewSet[string](), nil, &counter)
	require.NoError(t, err)

	require.Len(t, walks, 2)
	assert.Equal(t, f.archiveDir, walks[0].Path,
		"archive paths must stay in the given order — earlier paths serve an orphan first")
	assert.Equal(t, second, walks[1].Path)
	assert.Len(t, walks[0].Files, 1)
	assert.Len(t, walks[1].Files, 2)
	assert.EqualValues(t, 3, counter, "counter must cover files from all archive paths")
}

func TestWalkArchives_MissingPathIsWarnedAndSkipped(t *testing.T) {
	f := newArchiveScanFixture(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	// A typo in one --archive-path must not abort the run: the path is dropped with a
	// warning and the remaining paths are still walked.
	walks, err := WalkArchives([]string{missing, f.archiveDir}, set.NewSet[string](), nil, nil)

	require.NoError(t, err)
	require.Len(t, walks, 1, "the unreadable path must be dropped, got %+v", walks)
	assert.Equal(t, f.archiveDir, walks[0].Path)
	assert.Len(t, walks[0].Files, 1, "the readable path must still be walked")
}

func TestScanArchives_SkipsSecondArchivePathOnceOrphansAreServed(t *testing.T) {
	f := newArchiveScanFixture(t)
	// A second archive path that could serve the same orphan.
	secondArchive := filepath.Join(t.TempDir(), "archive2")
	require.NoError(t, os.MkdirAll(secondArchive, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(secondArchive, "yet-another.txt"),
		[]byte("hello world"), 0o644))

	var requested []string
	var progress ArchiveScanProgress
	actions, err := ScanArchivesForCopiesWithDigests(
		walkFixtureArchives(t, f.archiveDir, secondArchive),
		f.orphans, nil, recordingDigestFn(f.sourceDir, &requested), nil,
		f.sourceFiles, f.destDir, false, nil,
		&progress,
	)
	require.NoError(t, err)

	require.Len(t, copyActions(actions), 1, "got actions: %+v", actions)
	assert.EqualValues(t, 1, progress.FilesHashed,
		"the second archive path holds nothing unserved, so nothing more may be hashed")
	assert.Equal(t, []string{"match.txt"}, requested,
		"the orphan digest must be requested once, not once per archive path")
}

func TestScanArchives_ReusesKnownDigestsWithoutHashing(t *testing.T) {
	f := newArchiveScanFixture(t)
	knownDigest, err := GetDigest(filepath.Join(f.sourceDir, "match.txt"))
	require.NoError(t, err)
	known := map[string]entity.FileDigest{"match.txt": knownDigest}

	var requested []string
	var progress ArchiveScanProgress
	actions, err := ScanArchivesForCopiesWithDigests(
		walkFixtureArchives(t, f.archiveDir),
		f.orphans, known, recordingDigestFn(f.sourceDir, &requested), nil,
		f.sourceFiles, f.destDir, false, nil,
		&progress,
	)
	require.NoError(t, err)

	assert.Empty(t, requested, "digest was already known, so nothing may be hashed again")
	require.Len(t, copyActions(actions), 1,
		"the known digest must still produce a match, got actions: %+v", actions)
}

func TestScanArchives_DoesNotMutateCallersDigestMap(t *testing.T) {
	f := newArchiveScanFixture(t)
	known := map[string]entity.FileDigest{}
	var requested []string
	var progress ArchiveScanProgress

	_, err := ScanArchivesForCopiesWithDigests(
		walkFixtureArchives(t, f.archiveDir),
		f.orphans, known, recordingDigestFn(f.sourceDir, &requested), nil,
		f.sourceFiles, f.destDir, false, nil,
		&progress,
	)
	require.NoError(t, err)
	assert.Empty(t, known, "lazily computed digests must not leak into the caller's map")
}

func TestScanArchives_WithoutDigestFnFallsBackToKnownDigests(t *testing.T) {
	f := newArchiveScanFixture(t)
	var progress ArchiveScanProgress

	// No digest function and no known digests: nothing can be compared, so no copies.
	actions, err := ScanArchivesForCopiesWithDigests(
		walkFixtureArchives(t, f.archiveDir),
		f.orphans, nil, nil, nil,
		f.sourceFiles, f.destDir, false, nil,
		&progress,
	)
	require.NoError(t, err)
	assert.Empty(t, copyActions(actions), "without any digests there is nothing to match")
}

func TestBatchDigestsParallel_MatchesSequentialDigests(t *testing.T) {
	dir := t.TempDir()
	var relPaths []string
	expected := map[string]entity.FileDigest{}
	// More files than workers, so every worker slice is exercised.
	for i := 0; i < 64; i++ {
		name := filepath.Join("sub", "file-"+string(rune('a'+i%26))+"-"+strconv.Itoa(i)+".txt")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte("content of file "+strconv.Itoa(i)), 0o644))
		digest, err := GetDigest(path)
		require.NoError(t, err, "sequential digest of %s", path)
		expected[name] = digest
		relPaths = append(relPaths, name)
	}
	// A file that doesn't exist must be skipped rather than abort the batch.
	relPaths = append(relPaths, filepath.Join("sub", "missing.txt"))

	var counter int32
	actual := BatchDigestsParallel(nil, dir, relPaths, &counter)

	assert.Equal(t, expected, actual, "parallel digests must equal sequentially computed ones")
	assert.EqualValues(t, len(relPaths), counter,
		"counter must advance once per file, including the one that failed")
}

func TestSortForLocality_OrdersByInodeAndKeepsTheSameSet(t *testing.T) {
	dir := t.TempDir()
	var relPaths []string
	for i := 0; i < 16; i++ {
		rel := "file-" + strconv.Itoa(i) + ".txt"
		require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte(strconv.Itoa(i)), 0o644))
		relPaths = append(relPaths, rel)
	}
	// Start from an order that is neither sorted by name nor by inode.
	sort.Sort(sort.Reverse(sort.StringSlice(relPaths)))
	before := append([]string(nil), relPaths...)

	sortForLocality(dir, relPaths, nil)

	assert.ElementsMatch(t, before, relPaths, "sorting must not add or drop paths")
	inodes := make([]uint64, 0, len(relPaths))
	for _, rel := range relPaths {
		inode, ok := fileInode(filepath.Join(dir, rel))
		require.True(t, ok, "inode of %s", rel)
		inodes = append(inodes, inode)
	}
	assert.IsIncreasing(t, inodes, "files must be ordered by inode, got %v", inodes)
}

func TestSortForLocality_FallsBackToPathOrderForRemoteFS(t *testing.T) {
	relPaths := []string{"c.txt", "a.txt", "b.txt"}
	// A non-nil FileSystem means remote, where inodes aren't fetched.
	sortForLocality("/base", relPaths, rsfs.NewLocalFS())
	assert.Equal(t, []string{"a.txt", "b.txt", "c.txt"}, relPaths)
}

func TestSortForLocality_KeepsPathOrderWhenInodeUnavailable(t *testing.T) {
	// Paths that don't exist: no inode can be read, so the path order must survive.
	relPaths := []string{"c.txt", "a.txt", "b.txt"}
	sortForLocality(t.TempDir(), relPaths, nil)
	assert.Equal(t, []string{"a.txt", "b.txt", "c.txt"}, relPaths)
}

func TestComputeSyncActions_ReturnsOrphanDigestsForReuse(t *testing.T) {
	f := newArchiveScanFixture(t)
	// One candidate at destination, so the move-matching phase actually runs.
	candidate := "moved.txt"
	require.NoError(t, os.WriteFile(filepath.Join(f.destDir, candidate), []byte("hello world"), 0o644))
	destFiles := map[string]entity.FileMeta{candidate: {Size: 11, ModifiedTimestamp: 1}}

	var srcCounter, dstCounter int32
	_, _, orphanDigests, err := ComputeSyncActionsWithFS(nil, nil,
		f.sourceDir, f.sourceFiles, f.orphans,
		f.destDir, destFiles, []string{candidate},
		&srcCounter, &dstCounter, false, false,
	)
	require.NoError(t, err)

	for _, orphan := range f.orphans {
		expected, digestErr := GetDigest(filepath.Join(f.sourceDir, orphan))
		require.NoError(t, digestErr, "digest of %s", orphan)
		assert.Equal(t, expected, orphanDigests[orphan],
			"digest of orphan %q must be handed back so the archive scan can reuse it", orphan)
	}
}

func TestBatchDigestsParallel_EmptyInput(t *testing.T) {
	assert.Empty(t, BatchDigestsParallel(nil, t.TempDir(), nil, nil))
}
