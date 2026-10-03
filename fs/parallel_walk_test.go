package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeWalkTree creates 20 dirs with 5 subdirs of 10 files each, plus one file named
// excluded.txt at the top.
func writeWalkTree(t *testing.T) string {
	root := t.TempDir()
	for d := 0; d < 20; d++ {
		for s := 0; s < 5; s++ {
			dir := filepath.Join(root, fmt.Sprintf("d%02d", d), fmt.Sprintf("s%d", s))
			require.NoError(t, os.MkdirAll(dir, 0o755))
			for f := 0; f < 10; f++ {
				require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.txt", f)),
					[]byte(fmt.Sprintf("%d-%d-%d", d, s, f)), 0o644))
			}
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "excluded.txt"), []byte("x"), 0o644))
	return root
}

// walkWithThreads walks root with the given number of threads and returns the entries in
// a stable order plus the file counter.
func walkWithThreads(t *testing.T, threads int,
	walk func(excluded map[string]struct{}, counter *int32) ([]DirEntry, error),
) ([]string, int32) {
	previous := DefaultWalkThreads
	DefaultWalkThreads = threads
	defer func() { DefaultWalkThreads = previous }()
	var counter int32
	entries, err := walk(map[string]struct{}{"excluded.txt": {}}, &counter)
	require.NoError(t, err)
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = fmt.Sprintf("%s dir=%v size=%d", filepath.ToSlash(e.RelativePath), e.IsDir, e.Size)
	}
	sort.Strings(paths)
	return paths, counter
}

func assertSameWalkWithAnyNumberOfThreads(t *testing.T,
	walk func(excluded map[string]struct{}, counter *int32) ([]DirEntry, error),
) {
	serial, serialCount := walkWithThreads(t, 1, walk)
	assert.Len(t, serial, 20+20*5+20*5*10, "20 dirs, 100 subdirs and 1000 files, nothing excluded")
	assert.Contains(t, serial, "d07/s3/f2.txt dir=false size=5")
	assert.Equal(t, int32(1000), serialCount, "the counter counts files only")
	for _, threads := range []int{2, 4, 16} {
		parallel, count := walkWithThreads(t, threads, walk)
		assert.Equal(t, serial, parallel, "threads=%d", threads)
		assert.Equal(t, serialCount, count, "threads=%d", threads)
	}
}

func TestLocalWalk_SameEntriesWithAnyNumberOfThreads(t *testing.T) {
	root := writeWalkTree(t)
	// OneFileSystem keeps the walk off the BTRFS shortcut, so the standard walk runs.
	local := &LocalFS{OneFileSystem: true}
	assertSameWalkWithAnyNumberOfThreads(t, func(excluded map[string]struct{}, counter *int32) ([]DirEntry, error) {
		return local.Walk(root, excluded, counter)
	})
}

func TestLocalWalk_MissingRootGivesNoEntries(t *testing.T) {
	entries, err := (&LocalFS{OneFileSystem: true}).Walk(filepath.Join(t.TempDir(), "missing"), nil, nil)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
