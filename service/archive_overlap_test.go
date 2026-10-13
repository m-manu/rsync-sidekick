package service

import (
	"os"
	"path/filepath"
	"testing"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type overlapTree struct {
	root, dest string
	known      *KnownTree
}

// newOverlapTree builds root/{a/x.bin, b/y.bin, top.bin, dest/{d1.bin, sub/d2.bin, sub/unlisted.bin}}.
// The destination's known list holds a ghost file that is not on disk and lacks
// unlisted.bin, so a result shows whether dest was taken from the list or walked.
func newOverlapTree(t *testing.T) overlapTree {
	t.Helper()
	root := t.TempDir()
	for _, f := range []string{"a/x.bin", "b/y.bin", "top.bin", "dest/d1.bin", "dest/sub/d2.bin", "dest/sub/unlisted.bin"} {
		path := filepath.Join(root, f)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(f), 0o644))
	}
	dest := filepath.Join(root, "dest")
	known := &KnownTree{Root: dest, Files: map[string]entity.FileMeta{
		"d1.bin":        {Size: 11},
		"sub/d2.bin":    {Size: 15},
		"sub/ghost.bin": {Size: 1},
	}}
	return overlapTree{root: root, dest: dest, known: known}
}

func walkKeys(walk ArchiveWalk) []string {
	keys := make([]string, 0, len(walk.Files))
	for k := range walk.Files {
		keys = append(keys, k)
	}
	return keys
}

func TestWalkArchivesKnowing_destinationInsideArchiveIsNotWalkedAgain(t *testing.T) {
	tr := newOverlapTree(t)
	var counter int32
	walks, err := WalkArchivesKnowing([]string{tr.root}, set.NewSet[string](), nil, &counter, tr.known)
	require.NoError(t, err)
	require.Len(t, walks, 1)

	assert.ElementsMatch(t, []string{"a/x.bin", "b/y.bin", "top.bin", "dest/d1.bin", "dest/sub/d2.bin", "dest/sub/ghost.bin"},
		walkKeys(walks[0]), "dest must come from the known list (ghost present, unlisted absent)")
	assert.Equal(t, int32(6), counter)
}

func TestWalkArchivesKnowing_archiveInsideDestinationComesFromKnownList(t *testing.T) {
	tr := newOverlapTree(t)
	walks, err := WalkArchivesKnowing([]string{filepath.Join(tr.dest, "sub")}, set.NewSet[string](), nil, nil, tr.known)
	require.NoError(t, err)
	require.Len(t, walks, 1)
	assert.ElementsMatch(t, []string{"d2.bin", "ghost.bin"}, walkKeys(walks[0]))
}

func TestWalkArchivesKnowing_nestedArchivePathsKeepOrderAndReuseOuterWalk(t *testing.T) {
	tr := newOverlapTree(t)
	inner := filepath.Join(tr.root, "a")
	walks, err := WalkArchivesKnowing([]string{inner, tr.root}, set.NewSet[string](), nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, walks, 2)
	assert.Equal(t, inner, walks[0].Path, "walks keep the order the paths were given in")
	assert.ElementsMatch(t, []string{"x.bin"}, walkKeys(walks[0]))
	assert.Equal(t, tr.root, walks[1].Path)
	assert.Contains(t, walkKeys(walks[1]), "dest/sub/unlisted.bin", "without a known list dest is walked")
}

func TestWalkArchivesKnowing_destinationBelowExcludedDirectoryIsNotAdded(t *testing.T) {
	tr := newOverlapTree(t)
	walks, err := WalkArchivesKnowing([]string{tr.root}, set.NewSet("dest"), nil, nil, tr.known)
	require.NoError(t, err)
	require.Len(t, walks, 1)
	assert.ElementsMatch(t, []string{"a/x.bin", "b/y.bin", "top.bin"}, walkKeys(walks[0]))
}

func TestWalkArchivesKnowing_archiveOneFileSystemWalksEverything(t *testing.T) {
	tr := newOverlapTree(t)
	rsfs.DefaultArchiveOneFileSystem = true
	defer func() { rsfs.DefaultArchiveOneFileSystem = false }()
	walks, err := WalkArchivesKnowing([]string{tr.root}, set.NewSet[string](), nil, nil, tr.known)
	require.NoError(t, err)
	require.Len(t, walks, 1)
	keys := walkKeys(walks[0])
	assert.Contains(t, keys, "dest/sub/unlisted.bin", "different boundary rules: no reuse, dest is walked")
	assert.NotContains(t, keys, "dest/sub/ghost.bin")
}

func TestWalkArchivesKnowing_sameResultAsPlainWalk(t *testing.T) {
	tr := newOverlapTree(t)
	destFiles, _, err := FindFilesFromDirectory(tr.dest, set.NewSet[string](), nil)
	require.NoError(t, err)
	reused, err := WalkArchivesKnowing([]string{tr.root}, set.NewSet[string](), nil, nil,
		&KnownTree{Root: tr.dest, Files: destFiles})
	require.NoError(t, err)
	plain, err := WalkArchivesKnowing([]string{tr.root}, set.NewSet[string](), nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, plain[0].Files, reused[0].Files)
}
