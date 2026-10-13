package action

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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
// cp -c.
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
	assert.Equal(t, 1, cloned, "cp -c is tried first")
}

func TestCopyFileAction_ReflinkIsAPlainCopyWithoutCloneSupport(t *testing.T) {
	for _, goos := range []string{"windows", "freebsd"} {
		t.Run(goos, func(t *testing.T) {
			onPlatform(t, goos, func(src, dst string) error {
				t.Errorf("cp -c must not be called on %s", goos)
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
