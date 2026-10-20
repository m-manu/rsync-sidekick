package remote

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecuteActionCopy_UsesATempAndRefusesAnExistingDest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "nested", "dst.bin")
	neighbor := dst + ".tmp"
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o640))
	require.NoError(t, os.WriteFile(neighbor, []byte("keep"), 0o644))
	mtime := time.Unix(1_700_000_000, 0)

	err := executeAction(ActionSpec{
		Type: "copy", FromAbsPath: src, ToAbsPath: dst, ModTimestamp: mtime.Unix(),
	})

	require.NoError(t, err)
	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(data))
	kept, err := os.ReadFile(neighbor)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(kept))
	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, mtime.Unix(), info.ModTime().Unix())
	assertNoAgentTemps(t, filepath.Dir(dst))

	err = executeAction(ActionSpec{
		Type: "copy", FromAbsPath: src, ToAbsPath: dst, ModTimestamp: mtime.Unix(), UseReflink: true,
	})
	require.Error(t, err)
	data, err = os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(data), "a second copy must not replace the destination")
	assertNoAgentTemps(t, filepath.Dir(dst))
}

func TestExecuteActionCopy_ReflinkLeavesNeighborAlone(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	neighbor := dst + ".tmp"
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o640))
	require.NoError(t, os.WriteFile(neighbor, []byte("keep"), 0o644))

	err := executeAction(ActionSpec{
		Type: "copy", FromAbsPath: src, ToAbsPath: dst, ModTimestamp: 1_700_000_000, UseReflink: true,
	})

	require.NoError(t, err)
	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(data))
	kept, err := os.ReadFile(neighbor)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(kept))
	assertNoAgentTemps(t, dir)
}

func assertNoAgentTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.NotContains(t, entry.Name(), ".rsync-sidekick-")
	}
}
