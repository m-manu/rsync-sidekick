package fs

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPipeSFTPFS serves the local file system through an in-process SFTP server.
func newPipeSFTPFS(t *testing.T) *SFTPFS {
	if runtime.GOOS == "windows" {
		t.Skip("the in-process SFTP server serves POSIX paths")
	}
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	server, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{serverReader, serverWriter})
	require.NoError(t, err)
	go func() { _ = server.Serve() }()
	client, err := sftp.NewClientPipe(clientReader, clientWriter)
	require.NoError(t, err)
	t.Cleanup(func() {
		// The client waits for the server's end of the pipe, so the server closes first.
		_ = server.Close()
		_ = client.Close()
	})
	return NewSFTPFS(client)
}

func TestSFTPWalk_SameEntriesWithAnyNumberOfThreads(t *testing.T) {
	root := writeWalkTree(t)
	sftpFS := newPipeSFTPFS(t)
	assertSameWalkWithAnyNumberOfThreads(t, func(excluded map[string]struct{}, counter *int32) ([]DirEntry, error) {
		return sftpFS.Walk(root, excluded, counter)
	})
}

func TestSFTPWalk_SameEntriesAsLocalWalk(t *testing.T) {
	root := writeWalkTree(t)
	write := func(rel string) {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("data"), 0o644))
	}
	write("skipme/inside.txt")
	write("d01/._resource")
	write("._mac/inside.txt")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "empty"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(root, "d01"), filepath.Join(root, "link")))

	excluded := map[string]struct{}{"excluded.txt": {}, "skipme": {}}
	sortedPaths := func(entries []DirEntry) []string {
		paths := make([]string, len(entries))
		for i, e := range entries {
			paths[i] = filepath.ToSlash(e.RelativePath)
		}
		sort.Strings(paths)
		return paths
	}

	localEntries, err := (&LocalFS{OneFileSystem: true}).Walk(root, excluded, nil)
	require.NoError(t, err)
	sftpEntries, err := newPipeSFTPFS(t).Walk(root+"/", excluded, nil)
	require.NoError(t, err)

	assert.Equal(t, sortedPaths(localEntries), sortedPaths(sftpEntries))
	assert.Contains(t, sortedPaths(sftpEntries), "empty")
	assert.NotContains(t, sortedPaths(sftpEntries), "link", "symlinks are neither files nor directories")
	assert.NotContains(t, sortedPaths(sftpEntries), "skipme/inside.txt", "excluded directories aren't entered")
	assert.NotContains(t, sortedPaths(sftpEntries), "._mac/inside.txt", "._ directories aren't entered")
}

func TestSFTPWalk_MissingRootGivesNoEntries(t *testing.T) {
	entries, err := newPipeSFTPFS(t).Walk(filepath.Join(t.TempDir(), "missing"), nil, nil)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
