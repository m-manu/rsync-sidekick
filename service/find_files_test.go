package service

import (
	"os"
	"path/filepath"
	"testing"

	set "github.com/deckarep/golang-set/v2"
	"github.com/stretchr/testify/assert"
)

func TestFindFilesFromDirectories(t *testing.T) {
	files, size, err := FindFilesFromDirectory(os.Getenv("GOROOT"), set.NewThreadUnsafeSet(".gitignore", ".hidden"), nil)
	assert.Equal(t, nil, err)
	assert.Greater(t, len(files), 0)
	assert.Greater(t, size, int64(0))
}

func TestFindFilesAndDirsFromDirectory_MatchesTheSeparateWalks(t *testing.T) {
	root := t.TempDir()
	assert.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b"), 0o755))
	assert.NoError(t, os.MkdirAll(filepath.Join(root, "skip"), 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(root, "a", "one.txt"), []byte("12345"), 0o644))
	assert.NoError(t, os.WriteFile(filepath.Join(root, "a", "b", "two.txt"), []byte("123"), 0o644))
	assert.NoError(t, os.WriteFile(filepath.Join(root, "skip", "three.txt"), []byte("1"), 0o644))
	excluded := set.NewThreadUnsafeSet("skip")

	var counter int32
	files, dirs, size, err := FindFilesAndDirsFromDirectory(root, excluded, &counter)
	assert.NoError(t, err)

	separateFiles, separateSize, err := FindFilesFromDirectory(root, excluded, nil)
	assert.NoError(t, err)
	separateDirs, err := FindDirsFromDirectory(root, excluded)
	assert.NoError(t, err)

	assert.Equal(t, separateFiles, files)
	assert.Equal(t, separateDirs, dirs)
	assert.Equal(t, separateSize, size)
	assert.Equal(t, int64(8), size)
	assert.ElementsMatch(t, []string{"a", filepath.Join("a", "b")}, keys(dirs), "excluded dirs stay out")
	assert.Equal(t, int32(2), counter, "the counter counts files only")
}

func keys[V any](m map[string]V) []string {
	result := make([]string, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	return result
}
