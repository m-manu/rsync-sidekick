//go:build linux

package action

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// otherDeviceDir returns a temporary directory on another filesystem than dir, or skips
// the test when there is none.
func otherDeviceDir(t *testing.T, dir string) string {
	t.Helper()
	other, err := os.MkdirTemp("/dev/shm", "move-test-")
	if err != nil {
		t.Skipf("no /dev/shm: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(other) })
	var a, b syscall.Stat_t
	if syscall.Stat(dir, &a) != nil || syscall.Stat(other, &b) != nil || a.Dev == b.Dev {
		t.Skip("/dev/shm is on the same device as the temp dir")
	}
	return other
}

func TestMoveFileAction_RenameAcrossDevicesCopiesAndKeepsTheOriginal(t *testing.T) {
	base := t.TempDir()
	other := otherDeviceDir(t, base)
	link := filepath.Join(base, "other")
	require.NoError(t, os.Symlink(other, link))
	src := filepath.Join(base, "a.bin")
	require.NoError(t, os.WriteFile(src, []byte("content"), 0o640))
	mtime := time.Unix(1_700_000_000, 0)
	require.NoError(t, os.Chtimes(src, mtime, mtime))
	before := MovesAsCopies()

	err := MoveFileAction{BasePath: base, RelativeFromPath: "a.bin", RelativeToPath: "other/b.bin"}.Perform()

	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(other, "b.bin"))
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))
	info, err := os.Stat(filepath.Join(other, "b.bin"))
	require.NoError(t, err)
	assert.Equal(t, mtime.Unix(), info.ModTime().Unix(), "the copy keeps the mtime, as a move would")
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	assert.FileExists(t, src, "the original is kept: rsync-sidekick never deletes")
	assert.Equal(t, before+1, MovesAsCopies())
}

func TestMoveFileAction_RenameOnOneDeviceMoves(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "a.bin"), []byte("content"), 0o644))
	before := MovesAsCopies()

	err := MoveFileAction{BasePath: base, RelativeFromPath: "a.bin", RelativeToPath: "b.bin"}.Perform()

	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(base, "a.bin"))
	assert.FileExists(t, filepath.Join(base, "b.bin"))
	assert.Equal(t, before, MovesAsCopies(), "a plain rename is not counted as a copy")
}
