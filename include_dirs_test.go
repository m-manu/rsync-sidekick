package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withIncludeDirs(t *testing.T, dirs ...string) {
	t.Helper()
	old := includeDirs
	includeDirs = dirs
	t.Cleanup(func() { includeDirs = old })
}

func TestNormalizeIncludeDirs_CleansAndDropsNestedEntries(t *testing.T) {
	got, err := normalizeIncludeDirs([]string{"Media/", " FastDrive ", "Media/Movies", "", "./Privat", "FastDrive"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Media", "FastDrive", "Privat"}, got)
}

func TestNormalizeIncludeDirs_RejectsPathsOutsideTheRoot(t *testing.T) {
	for _, bad := range []string{"/abs", "..", "../x", "."} {
		_, err := normalizeIncludeDirs([]string{bad})
		assert.Error(t, err, "include %q", bad)
	}
}

func TestWalkBases_StartAtTheLastComponentWithoutWildcard(t *testing.T) {
	assert.Equal(t, "Backup", walkBase("Backup/*/data"))
	assert.Equal(t, "", walkBase("Back*"))
	assert.Equal(t, "A/B", walkBase("A/B"))
	assert.Equal(t, []string{"A", "C"}, walkBases([]string{"A/*", "A/x/y", "C"}))
	assert.Equal(t, []string{""}, walkBases([]string{"A", "B*"}), "a wildcard at the top level means walking the root")
}

func TestIncludedPath_MatchesPrefixesAndWildcards(t *testing.T) {
	withIncludeDirs(t, "FastDrive", "BackupComputer/*", "Back?p*/x")
	cases := map[string]bool{
		"FastDrive/a.mkv":               true,
		"FastDrive":                     true,
		"FastDriveOld/a.mkv":            false,
		"BackupComputer/nexus/etc/f":    true,
		"BackupComputer/top-level-file": true,
		"BackupArchiv/x/y":              true,
		"BackupArchiv/z/y":              false,
		"Privat/a":                      false,
	}
	for path, want := range cases {
		assert.Equal(t, want, includedPath(path), "path %q", path)
	}
}

func TestWalkIncluded_PrefixesPathsAndSkipsMissingDestinationDirs(t *testing.T) {
	withIncludeDirs(t, "A", "B/*", "Missing")
	var walked []string
	walk := func(dir string) (map[string]entity.FileMeta, map[string]int64, int64, error) {
		walked = append(walked, dir)
		switch dir {
		case "/root/A":
			return map[string]entity.FileMeta{"f1": {Size: 1}}, map[string]int64{"sub": 5}, 1, nil
		case "/root/B":
			return map[string]entity.FileMeta{"x/f2": {Size: 2}, "loose": {Size: 4}}, nil, 6, nil
		}
		t.Fatalf("unexpected walk of %s", dir)
		return nil, nil, 0, nil
	}
	missing := func(dir string) bool { return dir == "/root/Missing" }

	files, dirs, size, err := walkIncluded("/root", missing, walk)

	require.NoError(t, err)
	sort.Strings(walked)
	assert.Equal(t, []string{"/root/A", "/root/B"}, walked)
	assert.Equal(t, map[string]entity.FileMeta{"A/f1": {Size: 1}, "B/x/f2": {Size: 2}, "B/loose": {Size: 4}}, files)
	assert.Equal(t, map[string]int64{"A/sub": 5}, dirs)
	assert.Equal(t, int64(7), size)
}

func TestWalkIncluded_WithoutIncludeDirsWalksTheRoot(t *testing.T) {
	withIncludeDirs(t)
	files, _, _, err := walkIncluded("/root", nil, func(dir string) (map[string]entity.FileMeta, map[string]int64, int64, error) {
		assert.Equal(t, "/root", dir)
		return map[string]entity.FileMeta{"f": {}}, nil, 0, nil
	})
	require.NoError(t, err)
	assert.Contains(t, files, "f")
}

func TestReadIncludeFile_SkipsCommentsAndBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "include.txt")
	require.NoError(t, os.WriteFile(path, []byte("# repair\nFastDrive\n\n  Privat  \nBackup*\n"), 0o644))
	got, err := readIncludeFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"FastDrive", "Privat", "Backup*"}, got)
}

func TestRsyncSidekick_IncludeDirLimitsTheScan(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	// Both folders hold a renamed file; only the included one may be moved.
	write(filepath.Join(src, "In/new-name.txt"), "content inside the included folder")
	write(filepath.Join(dst, "In/old-name.txt"), "content inside the included folder")
	write(filepath.Join(src, "Out/new-name.txt"), "content outside the included folder")
	write(filepath.Join(dst, "Out/old-name.txt"), "content outside the included folder")
	withIncludeDirs(t, "In")

	err := rsyncSidekick("test", src, exclusionsForTests, dst, "", false, false, false, 0, false, false, nil)

	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dst, "In/new-name.txt"))
	assert.NoFileExists(t, filepath.Join(dst, "In/old-name.txt"))
	assert.FileExists(t, filepath.Join(dst, "Out/old-name.txt"), "folder outside --include-dir stays untouched")
	assert.NoFileExists(t, filepath.Join(dst, "Out/new-name.txt"))
}
