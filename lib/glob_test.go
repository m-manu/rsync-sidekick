package lib

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandPaths_KeepsOrderAndReplacesPatternsByTheirMatches(t *testing.T) {
	glob := func(patterns []string) ([][]string, error) {
		assert.Equal(t, []string{"/snap/*", "/none/*"}, patterns, "only paths with wildcards are globbed")
		return [][]string{{"/snap/a", "/plain"}, {}}, nil
	}

	expanded, unmatched, err := ExpandPaths([]string{"/plain", "/snap/*", "/none/*", "/last"}, glob)

	require.NoError(t, err)
	assert.Equal(t, []string{"/plain", "/snap/a", "/last"}, expanded, "duplicates keep their first position")
	assert.Equal(t, []string{"/none/*"}, unmatched)
}

func TestExpandPaths_PlainPathsNeverCallGlob(t *testing.T) {
	glob := func([]string) ([][]string, error) { return nil, errors.New("must not be called") }

	expanded, unmatched, err := ExpandPaths([]string{"/a", "/b"}, glob)

	require.NoError(t, err)
	assert.Equal(t, []string{"/a", "/b"}, expanded)
	assert.Empty(t, unmatched)
}

func TestGlobDirs_ReturnsOnlyDirectoriesInLexicalOrder(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "b"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "c"), nil, 0o644))

	dirs, err := GlobDirs(filepath.Join(root, "*"))

	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(root, "a"), filepath.Join(root, "b")}, dirs)
}
