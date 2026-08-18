package remote

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clientTalkingTo builds an AgentClient whose "agent" is the given canned response lines,
// and returns the client plus the buffer that captures what the client sent.
func clientTalkingTo(t *testing.T, responseLines ...string) (*AgentClient, *bytes.Buffer) {
	t.Helper()
	sent := &bytes.Buffer{}
	return &AgentClient{
		stdin:  nopWriteCloser{sent},
		stdout: bufio.NewReader(strings.NewReader(strings.Join(responseLines, "\n") + "\n")),
	}, sent
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func envelopeLine(t *testing.T, msgType string, payload any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	line, err := json.Marshal(Envelope{Type: msgType, Payload: data})
	require.NoError(t, err)
	return string(line)
}

func TestAgentClientVersion_ReadsVersionOverTheConnection(t *testing.T) {
	client, sent := clientTalkingTo(t, envelopeLine(t, MsgVersionResponse, VersionResponse{Version: "v2.1.5"}))

	version, err := client.Version()

	require.NoError(t, err)
	assert.Equal(t, "v2.1.5", version)
	assert.Contains(t, sent.String(), MsgVersionRequest,
		"client must send a version_request, sent: %s", sent.String())
}

func TestAgentClientVersion_ErrorResponseMeansUnsupported(t *testing.T) {
	// This is what an agent older than the version request answers.
	client, _ := clientTalkingTo(t,
		envelopeLine(t, MsgError, ErrorResponse{Message: "unknown message type: version_request"}))

	_, err := client.Version()

	assert.ErrorIs(t, err, ErrVersionRequestUnsupported,
		"an error response must be reported as unsupported so the caller keeps the connection")
}

func TestAgentClientVersion_ClosedConnectionIsAPlainError(t *testing.T) {
	// No output at all: the remote command was missing, or ssh died.
	client := &AgentClient{
		stdin:  nopWriteCloser{&bytes.Buffer{}},
		stdout: bufio.NewReader(strings.NewReader("")),
	}

	_, err := client.Version()

	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrVersionRequestUnsupported),
		"a dead connection must not be mistaken for an old-but-alive agent, got: %v", err)
}

func TestAgentClientVersion_UnexpectedMessageTypeIsAnError(t *testing.T) {
	client, _ := clientTalkingTo(t, envelopeLine(t, MsgWalkResponse, WalkResponse{}))

	_, err := client.Version()

	require.Error(t, err)
	assert.Contains(t, err.Error(), MsgWalkResponse, "error should name the unexpected type")
}

func TestAgentVersionRequest_AnsweredByHandler(t *testing.T) {
	// The dispatch loop is what answers version requests, so drive it directly.
	request := envelopeLine(t, MsgVersionRequest, VersionRequest{})
	quit := envelopeLine(t, MsgQuit, struct{}{})

	stdout := runAgentLoop(t, "v9.9.9", request+"\n"+quit+"\n")

	var env Envelope
	require.NoError(t, json.Unmarshal([]byte(firstLine(stdout)), &env))
	require.Equal(t, MsgVersionResponse, env.Type, "agent replied: %s", stdout)

	var resp VersionResponse
	require.NoError(t, json.Unmarshal(env.Payload, &resp))
	assert.Equal(t, "v9.9.9", resp.Version,
		"agent must report the version it was started with")
}

// runAgentLoop feeds input to the agent dispatch loop and returns everything it wrote.
func runAgentLoop(t *testing.T, agentVersion string, input string) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, runAgentOn(strings.NewReader(input), &out, agentVersion))
	return out.String()
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}
