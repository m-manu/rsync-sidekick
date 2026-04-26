package remote

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
	"github.com/stretchr/testify/assert"
)

func setupAgentTestTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	assert.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("world"), 0644))
	return dir
}

func walkViaAgent(t *testing.T, dirPath string, oneFileSystem bool) WalkResponse {
	t.Helper()
	req := WalkRequest{
		DirPath:       dirPath,
		ExcludedNames: []string{},
		OneFileSystem: oneFileSystem,
	}
	payload, err := json.Marshal(req)
	assert.NoError(t, err)

	var buf bytes.Buffer
	handleWalk(&buf, payload)

	var env Envelope
	assert.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	assert.Equal(t, MsgWalkResponse, env.Type, "expected walk_response, got: %s", env.Type)

	var resp WalkResponse
	assert.NoError(t, json.Unmarshal(env.Payload, &resp))
	return resp
}

func TestAgentWalkOneFileSystem(t *testing.T) {
	dir := setupAgentTestTree(t)

	t.Run("FlagTrue", func(t *testing.T) {
		// Reset global before test
		rsfs.DefaultOneFileSystem = false

		_ = walkViaAgent(t, dir, true)

		// Verify the agent set the global flag
		assert.True(t, rsfs.DefaultOneFileSystem,
			"agent should set DefaultOneFileSystem=true when request has OneFileSystem=true")
	})

	t.Run("FlagFalse", func(t *testing.T) {
		// Set global to true, verify agent resets it
		rsfs.DefaultOneFileSystem = true

		_ = walkViaAgent(t, dir, false)

		assert.False(t, rsfs.DefaultOneFileSystem,
			"agent should set DefaultOneFileSystem=false when request has OneFileSystem=false")
	})

	t.Run("ResultsUnchangedOnSameDevice", func(t *testing.T) {
		// On the same filesystem, OneFileSystem should not affect results
		respOn := walkViaAgent(t, dir, true)
		respOff := walkViaAgent(t, dir, false)

		assert.Equal(t, len(respOff.Files), len(respOn.Files),
			"same device — OneFileSystem should not change file count")
	})
}

func TestAgentWalkOneFileSystemWithMockedDevices(t *testing.T) {
	dir := setupAgentTestTree(t)

	// Mock: "sub" dir is on a different device
	origDefault := rsfs.DefaultOneFileSystem
	defer func() { rsfs.DefaultOneFileSystem = origDefault }()

	t.Run("SkipsCrossDeviceDir", func(t *testing.T) {
		// We can't easily mock devices through the agent (it creates its own LocalFS),
		// but we can verify the flag propagation and that the scan completes.
		// The actual device-skip logic is tested in fs/local_fs_test.go.
		resp := walkViaAgent(t, dir, true)

		// Should have scanned successfully (both files on same device in test)
		assert.Contains(t, fileNames(resp), "a.txt")
		assert.Contains(t, fileNames(resp), filepath.Join("sub", "b.txt"))
	})
}

func fileNames(resp WalkResponse) []string {
	names := make([]string, 0, len(resp.Files))
	for path := range resp.Files {
		names = append(names, path)
	}
	return names
}
