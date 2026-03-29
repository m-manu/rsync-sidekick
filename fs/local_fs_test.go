package fs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// setupTestTree creates:
//
//	tmpdir/
//	  a.txt
//	  sub/
//	    b.txt
//	  other/
//	    c.txt
func setupTestTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	assert.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0755))
	assert.NoError(t, os.MkdirAll(filepath.Join(dir, "other"), 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("b"), 0644))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "other", "c.txt"), []byte("c"), 0644))
	return dir
}

func entryPaths(entries []DirEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			paths = append(paths, e.RelativePath)
		}
	}
	return paths
}

func TestWalkOneFileSystem_SkipsCrossDevice(t *testing.T) {
	dir := setupTestTree(t)

	// Mock: "other/" is on device 99, everything else on device 1
	fs := &LocalFS{
		OneFileSystem: true,
		deviceForPath: func(path string) (uint64, bool) {
			if filepath.Base(path) == "other" {
				return 99, true
			}
			return 1, true
		},
	}

	entries, err := fs.Walk(dir, map[string]struct{}{}, nil)
	assert.NoError(t, err)

	paths := entryPaths(entries)
	assert.Contains(t, paths, "a.txt")
	assert.Contains(t, paths, filepath.Join("sub", "b.txt"))
	assert.NotContains(t, paths, filepath.Join("other", "c.txt"),
		"should skip 'other/' which is on a different device")
}

func TestWalkOneFileSystem_DisabledCrossesDevices(t *testing.T) {
	dir := setupTestTree(t)

	// Same mock but OneFileSystem=false — should see everything
	fs := &LocalFS{
		OneFileSystem: false,
		deviceForPath: func(path string) (uint64, bool) {
			if filepath.Base(path) == "other" {
				return 99, true
			}
			return 1, true
		},
	}

	entries, err := fs.Walk(dir, map[string]struct{}{}, nil)
	assert.NoError(t, err)

	paths := entryPaths(entries)
	assert.Contains(t, paths, "a.txt")
	assert.Contains(t, paths, filepath.Join("sub", "b.txt"))
	assert.Contains(t, paths, filepath.Join("other", "c.txt"),
		"should include 'other/' when OneFileSystem is disabled")
}

func TestWalkOneFileSystem_SameDeviceUnaffected(t *testing.T) {
	dir := setupTestTree(t)

	// All on same device — OneFileSystem should not filter anything
	fsOn := &LocalFS{
		OneFileSystem: true,
		deviceForPath:    func(path string) (uint64, bool) { return 1, true },
	}
	fsOff := &LocalFS{
		OneFileSystem: false,
		deviceForPath:    func(path string) (uint64, bool) { return 1, true },
	}

	entriesOn, err := fsOn.Walk(dir, map[string]struct{}{}, nil)
	assert.NoError(t, err)
	entriesOff, err := fsOff.Walk(dir, map[string]struct{}{}, nil)
	assert.NoError(t, err)

	assert.Equal(t, len(entriesOff), len(entriesOn),
		"same device — OneFileSystem should not change results")
}

func TestWalkOneFileSystem_MultipleDevices(t *testing.T) {
	dir := setupTestTree(t)
	// Add a deeper structure
	assert.NoError(t, os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "deep", "d.txt"), []byte("d"), 0644))

	// Mock: "sub/deep" is on device 42, rest on device 1
	fs := &LocalFS{
		OneFileSystem: true,
		deviceForPath: func(path string) (uint64, bool) {
			if filepath.Base(path) == "deep" {
				return 42, true
			}
			return 1, true
		},
	}

	entries, err := fs.Walk(dir, map[string]struct{}{}, nil)
	assert.NoError(t, err)

	paths := entryPaths(entries)
	assert.Contains(t, paths, "a.txt")
	assert.Contains(t, paths, filepath.Join("sub", "b.txt"))
	assert.Contains(t, paths, filepath.Join("other", "c.txt"))
	assert.NotContains(t, paths, filepath.Join("sub", "deep", "d.txt"),
		"should skip 'sub/deep/' which is on a different device")
}
