package action

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyFileAction_StringNamesReflink(t *testing.T) {
	// A reflink and a full copy are the same action but not the same cost, so the log has
	// to tell them apart.
	plain := CopyFileAction{AbsSourcePath: "/a/x.bin", AbsDestPath: "/b/x.bin", SourceModTime: time.Unix(1, 0)}
	reflinked := CopyFileAction{AbsSourcePath: "/a/x.bin", AbsDestPath: "/b/x.bin", SourceModTime: time.Unix(1, 0), UseReflink: true}

	assert.Equal(t, `copy file "/a/x.bin" to "/b/x.bin"`, plain.String())
	assert.Equal(t, `reflink file "/a/x.bin" to "/b/x.bin"`, reflinked.String())
}

// onPlatform makes Perform take the path of another platform, with clone standing in for
// clonefile.
func onPlatform(t *testing.T, goos string, clone func(src, dst string) error) {
	previousPlatform, previousClone := platform, cloneFile
	platform, cloneFile = goos, clone
	t.Cleanup(func() { platform, cloneFile = previousPlatform, previousClone })
}

// performReflink reflinks a fresh file and checks the result is a full, correct copy.
func performReflink(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	require.NoError(t, os.WriteFile(src, []byte("content"), 0o640))
	mtime := time.Unix(1_700_000_000, 0)
	fallbacksBefore := ReflinkFallbacks()

	err := CopyFileAction{AbsSourcePath: src, AbsDestPath: dst, SourceModTime: mtime, UseReflink: true}.Perform()

	require.NoError(t, err)
	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))
	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, mtime.Unix(), info.ModTime().Unix())
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	}
	assert.Equal(t, fallbacksBefore+1, ReflinkFallbacks(), "the full copy is counted as a reflink fallback")
}

func TestCopyFileAction_ReflinkFallsBackToCopyWhenMacOSCannotClone(t *testing.T) {
	cloned := 0
	onPlatform(t, "darwin", func(src, dst string) error {
		cloned++
		return errors.New("clonefile: Operation not supported")
	})
	performReflink(t)
	assert.Equal(t, 1, cloned, "clonefile is tried first")
}

func TestCopyFileAction_DarwinClonefileReflink(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("clonefile is only used on macOS")
	}
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	require.NoError(t, os.WriteFile(src, []byte("content"), 0o640))
	mtime := time.Unix(1_700_000_000, 0)
	fallbacksBefore := ReflinkFallbacks()

	err := CopyFileAction{AbsSourcePath: src, AbsDestPath: dst, SourceModTime: mtime, UseReflink: true}.Perform()

	require.NoError(t, err)
	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))
	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, mtime.Unix(), info.ModTime().Unix())
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	assert.Equal(t, fallbacksBefore, ReflinkFallbacks(), "clonefile succeeded, so this is not a full-copy fallback")
	assertNoSidekickTemps(t, dir)
}

func TestCopyFileAction_ReflinkIsAPlainCopyWithoutCloneSupport(t *testing.T) {
	for _, goos := range []string{"windows", "freebsd"} {
		t.Run(goos, func(t *testing.T) {
			onPlatform(t, goos, func(src, dst string) error {
				t.Errorf("clonefile must not be called on %s", goos)
				return nil
			})
			performReflink(t)
		})
	}
}

func TestCopyFileAction_UnixReflinkCommandCopiesOnThisHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	require.NoError(t, os.WriteFile(src, []byte("content"), 0o644))
	command := CopyFileAction{AbsSourcePath: src, AbsDestPath: dst, SourceModTime: time.Unix(1_700_000_000, 0),
		UseReflink: true}.UnixCommand()

	out, err := exec.Command("sh", "-c", command).CombinedOutput()

	require.NoError(t, err, "command: %s\noutput: %s", command, out)
	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))
}

func TestCopyFileAction_PerformLeavesNeighborAndRefusesExistingDest(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	neighbor := dst + ".tmp"
	require.NoError(t, os.WriteFile(src, []byte("content"), 0o640))
	require.NoError(t, os.WriteFile(neighbor, []byte("neighbor"), 0o600))
	mtime := time.Unix(1_700_000_000, 0)

	err := CopyFileAction{AbsSourcePath: src, AbsDestPath: dst, SourceModTime: mtime}.Perform()

	require.NoError(t, err)
	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))
	kept, err := os.ReadFile(neighbor)
	require.NoError(t, err)
	assert.Equal(t, "neighbor", string(kept))
	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, mtime.Unix(), info.ModTime().Unix())
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	}
	assertNoSidekickTemps(t, dir)

	require.NoError(t, os.WriteFile(dst, []byte("original"), 0o644))
	err = CopyFileAction{AbsSourcePath: src, AbsDestPath: dst, SourceModTime: mtime}.Perform()
	require.ErrorIs(t, err, os.ErrExist)
	data, err = os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "original", string(data))
	assertNoSidekickTemps(t, dir)
}

func TestCopyFileAction_ReflinkPublishesATempAndRefusesExistingDest(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	neighbor := dst + ".tmp"
	require.NoError(t, os.WriteFile(src, []byte("content"), 0o640))
	require.NoError(t, os.WriteFile(neighbor, []byte("neighbor"), 0o600))

	onPlatform(t, "darwin", func(_, got string) error {
		assert.NotEqual(t, dst, got, "the clone must not open the final path")
		_, statErr := os.Lstat(got)
		assert.ErrorIs(t, statErr, os.ErrNotExist, "clonefile needs the temporary name to be absent")
		return os.WriteFile(got, []byte("cloned"), 0o640)
	})
	err := CopyFileAction{AbsSourcePath: src, AbsDestPath: dst, SourceModTime: time.Unix(1_700_000_000, 0), UseReflink: true}.Perform()

	require.NoError(t, err)
	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "cloned", string(data))
	kept, err := os.ReadFile(neighbor)
	require.NoError(t, err)
	assert.Equal(t, "neighbor", string(kept))
	assertNoSidekickTemps(t, dir)

	require.NoError(t, os.WriteFile(dst, []byte("original"), 0o644))
	called := false
	onPlatform(t, "darwin", func(_, _ string) error {
		called = true
		return nil
	})
	err = CopyFileAction{AbsSourcePath: src, AbsDestPath: dst, SourceModTime: time.Unix(1_700_000_000, 0), UseReflink: true}.Perform()
	require.ErrorIs(t, err, os.ErrExist)
	assert.False(t, called, "an existing destination is refused before the clone runs")
	data, err = os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "original", string(data))
	assertNoSidekickTemps(t, dir)
}

func TestRegularCopy_RemovesTempWhenTheCopyFails(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "srcdir")
	dst := filepath.Join(dir, "dst.bin")
	require.NoError(t, os.Mkdir(src, 0o755))

	err := regularCopy(src, dst)

	require.Error(t, err)
	_, statErr := os.Stat(dst)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	assertNoSidekickTemps(t, dir)
}

func TestCopyFileAction_SFTPDoesNotTouchLocalPaths(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst.bin")
	neighbor := dst + ".tmp"
	require.NoError(t, os.WriteFile(neighbor, []byte("neighbor"), 0o644))

	err := CopyFileAction{
		AbsSourcePath: filepath.Join(dir, "missing-src.bin"),
		AbsDestPath:   dst,
		FS:            remoteFS{},
	}.Perform()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SFTP")
	_, statErr := os.Stat(dst)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	kept, err := os.ReadFile(neighbor)
	require.NoError(t, err)
	assert.Equal(t, "neighbor", string(kept))
	assertNoSidekickTemps(t, dir)
}

func assertNoSidekickTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.NotContains(t, entry.Name(), ".rsync-sidekick-")
	}
}

// remoteFS stands in for an SFTP destination. Perform must return before using it.
type remoteFS struct{}

func (remoteFS) Walk(string, map[string]struct{}, *int32) ([]rsfs.DirEntry, error) {
	return nil, errors.New("not used")
}
func (remoteFS) Lstat(string) (rsfs.FileInfo, error) { return rsfs.FileInfo{}, errors.New("not used") }
func (remoteFS) Stat(string) (rsfs.FileInfo, error)  { return rsfs.FileInfo{}, errors.New("not used") }
func (remoteFS) ReadFile(string) ([]byte, error)     { return nil, errors.New("not used") }
func (remoteFS) ReadAt(string, []byte, int64) (int, error) {
	return 0, errors.New("not used")
}
func (remoteFS) Rename(string, string) error { return errors.New("not used") }
func (remoteFS) Chtimes(string, time.Time, time.Time) error {
	return errors.New("not used")
}
func (remoteFS) MkdirAll(string) error           { return errors.New("not used") }
func (remoteFS) IsReadableDirectory(string) bool { return true }
func (remoteFS) Close() error                    { return nil }
