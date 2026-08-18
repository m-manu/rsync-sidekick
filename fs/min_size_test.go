package fs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withMinSize sets the global threshold for one test and restores it afterwards.
func withMinSize(t *testing.T, minSize int64) {
	t.Helper()
	previous := DefaultMinSize
	DefaultMinSize = minSize
	t.Cleanup(func() { DefaultMinSize = previous })
}

func minSizeTestTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "small.txt"), make([]byte, 100), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "big.bin"), make([]byte, 4096), 0o644))
	return dir
}

func relPaths(entries []DirEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			paths = append(paths, e.RelativePath)
		}
	}
	return paths
}

func TestLocalWalk_MinSizeFiltersDuringTheWalk(t *testing.T) {
	dir := minSizeTestTree(t)
	withMinSize(t, 1024)

	var counter int32
	entries, err := NewLocalFS().Walk(dir, map[string]struct{}{}, &counter)
	require.NoError(t, err)

	assert.Equal(t, []string{filepath.Join("sub", "big.bin")}, relPaths(entries),
		"a file below the threshold must not reach the caller")
	assert.EqualValues(t, 1, counter,
		"the counter must report what will be worked on, not what was seen")
}

func TestLocalWalk_MinSizeKeepsDirectories(t *testing.T) {
	dir := minSizeTestTree(t)
	withMinSize(t, 1<<30) // larger than every file in the tree

	entries, err := NewLocalFS().Walk(dir, map[string]struct{}{}, nil)
	require.NoError(t, err)

	assert.Empty(t, relPaths(entries), "every file is below the threshold")
	var dirs []string
	for _, e := range entries {
		if e.IsDir {
			dirs = append(dirs, e.RelativePath)
		}
	}
	assert.Equal(t, []string{"sub"}, dirs,
		"directories must survive filtering, or timestamp propagation would break")
}

func TestLocalWalk_MinSizeZeroKeepsEverything(t *testing.T) {
	dir := minSizeTestTree(t)
	withMinSize(t, 0)

	entries, err := NewLocalFS().Walk(dir, map[string]struct{}{}, nil)
	require.NoError(t, err)

	assert.Len(t, relPaths(entries), 2, "with the threshold off nothing may be dropped")
}

func TestSkipBySize(t *testing.T) {
	withMinSize(t, 1024)
	assert.True(t, SkipBySize(false, 1023), "below the threshold")
	assert.False(t, SkipBySize(false, 1024), "exactly at the threshold is kept")
	assert.False(t, SkipBySize(false, 1025), "above the threshold")
	assert.False(t, SkipBySize(true, 0), "directories are never filtered")

	withMinSize(t, 0)
	assert.False(t, SkipBySize(false, 1), "threshold off keeps even a 1-byte file")
}
