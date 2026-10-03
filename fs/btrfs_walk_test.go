//go:build linux

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

func TestBtrfsWalk_SameEntriesWithAnyNumberOfThreads(t *testing.T) {
	root := t.TempDir()
	if !IsBtrfs(root) {
		t.Skipf("%s is not on BTRFS", root)
	}
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
	excluded := map[string]struct{}{"excluded.txt": {}}

	walk := func(threads int) ([]string, int32) {
		previous := DefaultWalkThreads
		DefaultWalkThreads = threads
		defer func() { DefaultWalkThreads = previous }()
		var counter int32
		entries, err := BtrfsWalk(root, excluded, &counter)
		require.NoError(t, err)
		paths := make([]string, len(entries))
		for i, e := range entries {
			paths[i] = fmt.Sprintf("%s dir=%v size=%d", e.RelativePath, e.IsDir, e.Size)
		}
		sort.Strings(paths)
		return paths, counter
	}

	serial, serialCount := walk(1)
	assert.Len(t, serial, 20+20*5+20*5*10, "20 dirs, 100 subdirs and 1000 files, nothing excluded")
	assert.Equal(t, int32(1000), serialCount, "the counter counts files only")
	for _, threads := range []int{2, 4, 16} {
		parallel, count := walk(threads)
		assert.Equal(t, serial, parallel, "threads=%d", threads)
		assert.Equal(t, serialCount, count, "threads=%d", threads)
	}
}
