package remote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentGlobRequest_AnswersMatchingDirectoriesPerPattern(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"snap/2024-02", "snap/2024-01", "snap/other"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "snap/2024-03"), nil, 0o644)) // a file, not a dir
	request := envelopeLine(t, MsgGlobRequest, GlobRequest{Patterns: []string{
		filepath.Join(root, "snap/2024-*"), filepath.Join(root, "nothing/*"),
	}})
	quit := envelopeLine(t, MsgQuit, struct{}{})

	stdout := runAgentLoop(t, "v2.9.0", request+"\n"+quit+"\n")

	var env Envelope
	require.NoError(t, json.Unmarshal([]byte(firstLine(stdout)), &env))
	require.Equal(t, MsgGlobResponse, env.Type, "agent replied: %s", stdout)
	var resp GlobResponse
	require.NoError(t, json.Unmarshal(env.Payload, &resp))
	assert.Equal(t, [][]string{
		{filepath.Join(root, "snap/2024-01"), filepath.Join(root, "snap/2024-02")},
		{},
	}, resp.Matches)
}

func TestAgentClientGlob_OldAgentFailsWithAnUpdateHint(t *testing.T) {
	client := newFakeAgentClient(t, func(request Envelope, reply *json.Encoder) {
		payload, _ := json.Marshal(ErrorResponse{Message: "unknown message type: glob_request"})
		_ = reply.Encode(Envelope{Type: MsgError, ID: request.ID, Payload: payload})
	})

	_, err := client.Glob([]string{"/snap/*"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "too old")
}

func TestAgentClientGlob_ReturnsMatchesOfTheAgent(t *testing.T) {
	client := newFakeAgentClient(t, func(request Envelope, reply *json.Encoder) {
		payload, _ := json.Marshal(GlobResponse{Matches: [][]string{{"/snap/a", "/snap/b"}}})
		_ = reply.Encode(Envelope{Type: MsgGlobResponse, ID: request.ID, Payload: payload})
	})

	matches, err := client.Glob([]string{"/snap/*"})

	require.NoError(t, err)
	assert.Equal(t, [][]string{{"/snap/a", "/snap/b"}}, matches)
}
