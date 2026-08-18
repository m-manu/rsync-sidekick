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
	var walkCounter, scanCounter, matchCounter int32

	actions, err := ScanArchivesForCopiesWithDigests(
		[]string{f.archiveDir}, set.NewSet[string](),
		f.orphans, nil, recordingDigestFn(f.sourceDir, &requested),
		f.sourceFiles, f.destDir, false, nil,
		&walkCounter, &scanCounter, &matchCounter,
	)
	require.NoError(t, err)

	assert.Equal(t, []string{"match.txt"}, requested,
		"only the orphan matching an archive file on extension and size may be hashed")

	copies := copyActions(actions)
	require.Len(t, copies, 1, "expected exactly one copy action, got actions: %+v", actions)
	assert.Equal(t, filepath.Join(f.archiveDir, "copy-of-match.txt"), copies[0].AbsSourcePath)
	assert.Equal(t, filepath.Join(f.destDir, "match.txt"), copies[0].AbsDestPath)
	assert.EqualValues(t, 1, matchCounter, "match counter should count the single match")
	assert.EqualValues(t, 1, walkCounter, "walk counter should count the single archive file")
}

func TestScanArchives_ReusesKnownDigestsWithoutHashing(t *testing.T) {
	f := newArchiveScanFixture(t)
	knownDigest, err := GetDigest(filepath.Join(f.sourceDir, "match.txt"))
	require.NoError(t, err)
	known := map[string]entity.FileDigest{"match.txt": knownDigest}

	var requested []string
	var walkCounter, scanCounter, matchCounter int32
	actions, err := ScanArchivesForCopiesWithDigests(
		[]string{f.archiveDir}, set.NewSet[string](),
		f.orphans, known, recordingDigestFn(f.sourceDir, &requested),
		f.sourceFiles, f.destDir, false, nil,
		&walkCounter, &scanCounter, &matchCounter,
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
	var walkCounter, scanCounter, matchCounter int32

	_, err := ScanArchivesForCopiesWithDigests(
		[]string{f.archiveDir}, set.NewSet[string](),
		f.orphans, known, recordingDigestFn(f.sourceDir, &requested),
		f.sourceFiles, f.destDir, false, nil,
		&walkCounter, &scanCounter, &matchCounter,
	)
	require.NoError(t, err)
	assert.Empty(t, known, "lazily computed digests must not leak into the caller's map")
}

func TestScanArchives_WithoutDigestFnFallsBackToKnownDigests(t *testing.T) {
	f := newArchiveScanFixture(t)
	var walkCounter, scanCounter, matchCounter int32

	// No digest function and no known digests: nothing can be compared, so no copies.
	actions, err := ScanArchivesForCopiesWithDigests(
		[]string{f.archiveDir}, set.NewSet[string](),
		f.orphans, nil, nil,
		f.sourceFiles, f.destDir, false, nil,
		&walkCounter, &scanCounter, &matchCounter,
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
