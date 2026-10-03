package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMovedFileGetsTheSourceMtimeWhateverTheDirectoryOrder(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	content := string(make([]byte, 30_000)) + "renamed with another mtime"
	sourceTime, destTime := time.Unix(1_600_000_000, 0), time.Unix(1_500_000_000, 0)
	// The move goes from "z" to "a": sorted by destination directory, the move comes
	// before the timestamp fix on the candidate's old path.
	writeSame(t, content, filepath.Join(src, "a/new.txt"), filepath.Join(dst, "z/old.txt"))
	require.NoError(t, os.MkdirAll(filepath.Join(dst, "a"), 0o755))
	require.NoError(t, os.Chtimes(filepath.Join(src, "a/new.txt"), sourceTime, sourceTime))
	require.NoError(t, os.Chtimes(filepath.Join(dst, "z/old.txt"), destTime, destTime))

	err := rsyncSidekick("test", src, set.NewSet[string](), dst, "", false, false, false, 0, false, false, nil)

	require.NoError(t, err)
	info, statErr := os.Stat(filepath.Join(dst, "a/new.txt"))
	require.NoError(t, statErr)
	assert.Equal(t, sourceTime.Unix(), info.ModTime().Unix(), "moved file must carry the source mtime")
}
