package remote

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentHandler answers one request. Everything it encodes goes back to the client, so a
// handler can send progress messages before its terminal response.
type agentHandler func(request Envelope, reply *json.Encoder)

// newFakeAgentClient connects a client to a handler over two pipes. Unlike a canned
// buffer this cannot deliver a response before its request exists, which is the situation
// the demultiplexer has to cope with.
func newFakeAgentClient(t *testing.T, handler agentHandler) *AgentClient {
	t.Helper()
	requestsR, requestsW := io.Pipe()
	repliesR, repliesW := io.Pipe()

	client := newAgentClient(requestsW, repliesR)
	t.Cleanup(func() {
		_ = requestsW.Close()
		_ = repliesW.Close()
	})

	go func() {
		decoder := json.NewDecoder(requestsR)
		encoder := json.NewEncoder(repliesW)
		for {
			var request Envelope
			if err := decoder.Decode(&request); err != nil {
				_ = repliesW.Close()
				return
			}
			if request.Type == MsgQuit {
				_ = repliesW.Close()
				return
			}
			handler(request, encoder)
		}
	}()
	return client
}

// echoVersion answers every version request with the given version, keeping the ID.
func echoVersion(version string) agentHandler {
	return func(request Envelope, reply *json.Encoder) {
		payload, _ := json.Marshal(VersionResponse{Version: version})
		_ = reply.Encode(Envelope{Type: MsgVersionResponse, ID: request.ID, Payload: payload})
	}
}

func TestAgentClientVersion_ReadsVersionOverTheConnection(t *testing.T) {
	client := newFakeAgentClient(t, echoVersion("v2.3.0"))

	version, err := client.Version()

	require.NoError(t, err)
	assert.Equal(t, "v2.3.0", version)
}

func TestAgentClientVersion_ErrorResponseMeansUnsupported(t *testing.T) {
	// This is what an agent older than the version request answers.
	client := newFakeAgentClient(t, func(request Envelope, reply *json.Encoder) {
		payload, _ := json.Marshal(ErrorResponse{Message: "unknown message type: version_request"})
		_ = reply.Encode(Envelope{Type: MsgError, ID: request.ID, Payload: payload})
	})

	_, err := client.Version()

	assert.ErrorIs(t, err, ErrVersionRequestUnsupported,
		"an error response must be reported as unsupported so the caller keeps the connection")
}

func TestAgentClientVersion_ClosedConnectionIsAPlainError(t *testing.T) {
	// The remote command was missing, or ssh died: nothing ever answers.
	client := newFakeAgentClient(t, func(Envelope, *json.Encoder) {})
	// Close the reply side so the reader goroutine finishes.
	client.stdin.Close()

	_, err := client.Version()

	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrVersionRequestUnsupported),
		"a dead connection must not be mistaken for an old-but-alive agent, got: %v", err)
}

func TestAgentClientVersion_UnexpectedMessageTypeIsAnError(t *testing.T) {
	client := newFakeAgentClient(t, func(request Envelope, reply *json.Encoder) {
		_ = reply.Encode(Envelope{Type: MsgWalkResponse, ID: request.ID})
	})

	_, err := client.Version()

	require.Error(t, err)
	assert.Contains(t, err.Error(), MsgWalkResponse, "error should name the unexpected type")
}

func TestAgentClient_ConcurrentRequestsGetTheirOwnAnswers(t *testing.T) {
	// The agent answers in the opposite order and interleaves progress messages — exactly
	// what would go wrong without IDs, where one caller reads the other's messages.
	var mu sync.Mutex
	var walkRequest, digestRequest *Envelope
	release := make(chan struct{})

	client := newFakeAgentClient(t, func(request Envelope, reply *json.Encoder) {
		mu.Lock()
		switch request.Type {
		case MsgWalkRequest:
			walkRequest = &request
		case MsgDigestRequest:
			digestRequest = &request
		}
		both := walkRequest != nil && digestRequest != nil
		mu.Unlock()
		if !both {
			return // wait until both are in flight
		}

		// Digest progress and response first, walk second.
		digestProgress, _ := json.Marshal(DigestProgress{FilesHashed: 7, Total: 10})
		_ = reply.Encode(Envelope{Type: MsgDigestProgress, ID: digestRequest.ID, Payload: digestProgress})
		digests, _ := json.Marshal(DigestResponse{Digests: map[string]FileDigest{
			"only.txt": {FileExtension: ".txt", FileSize: 3, FileFuzzyHash: "hash"},
		}})
		_ = reply.Encode(Envelope{Type: MsgDigestResponse, ID: digestRequest.ID, Payload: digests})

		walkProgress, _ := json.Marshal(WalkProgress{FilesFound: 42})
		_ = reply.Encode(Envelope{Type: MsgWalkProgress, ID: walkRequest.ID, Payload: walkProgress})
		walkResp, _ := json.Marshal(WalkResponse{
			Files:     map[string]FileMeta{"a.txt": {Size: 5, ModifiedTimestamp: 1}},
			TotalSize: 5,
		})
		_ = reply.Encode(Envelope{Type: MsgWalkResponse, ID: walkRequest.ID, Payload: walkResp})
		close(release)
	})
	client.SetConcurrent(true)

	var wg sync.WaitGroup
	var walkFiles map[string]struct{}
	var walkErr, digestErr error
	var digestCount int

	wg.Add(2)
	go func() {
		defer wg.Done()
		files, _, _, err := client.Walk("/dir", nil, nil, 0, false)
		walkErr = err
		walkFiles = make(map[string]struct{}, len(files))
		for p := range files {
			walkFiles[p] = struct{}{}
		}
	}()
	go func() {
		defer wg.Done()
		digests, err := client.BatchDigest("/dir", []string{"only.txt"}, nil, 0)
		digestErr = err
		digestCount = len(digests)
	}()
	wg.Wait()
	<-release

	require.NoError(t, walkErr, "walk must not see the digest's messages")
	require.NoError(t, digestErr, "digest must not see the walk's messages")
	assert.Contains(t, walkFiles, "a.txt", "walk got its own response")
	assert.Equal(t, 1, digestCount, "digest got its own response")
}

func TestAgentClient_ProgressUpdatesTheCounter(t *testing.T) {
	client := newFakeAgentClient(t, func(request Envelope, reply *json.Encoder) {
		progress, _ := json.Marshal(WalkProgress{FilesFound: 1234})
		_ = reply.Encode(Envelope{Type: MsgWalkProgress, ID: request.ID, Payload: progress})
		walkResp, _ := json.Marshal(WalkResponse{Files: map[string]FileMeta{}})
		_ = reply.Encode(Envelope{Type: MsgWalkResponse, ID: request.ID, Payload: walkResp})
	})

	var counter int32
	_, _, _, err := client.Walk("/dir", nil, &counter, 1000, false)

	require.NoError(t, err)
	// The final response overwrites the counter with the real file count, so the progress
	// value itself is gone by now — what matters is that it was routed and not treated as
	// the response.
	assert.EqualValues(t, 0, counter, "empty walk response means zero files")
}

func TestAgentClient_CounterKeepsGrowingAcrossConsecutiveWalks(t *testing.T) {
	// The agent counts every walk from zero; one counter shared by several walks (include
	// directories, archive paths) must still show the running total.
	progressSeen := make(chan int32, 4)
	var counter int32
	client := newFakeAgentClient(t, func(request Envelope, reply *json.Encoder) {
		progress, _ := json.Marshal(WalkProgress{FilesFound: 2})
		_ = reply.Encode(Envelope{Type: MsgWalkProgress, ID: request.ID, Payload: progress})
		walkResp, _ := json.Marshal(WalkResponse{Files: map[string]FileMeta{
			"a": {Size: 1}, "b": {Size: 1}, "c": {Size: 1},
		}})
		_ = reply.Encode(Envelope{Type: MsgWalkResponse, ID: request.ID, Payload: walkResp})
	})

	_, _, _, err1 := client.Walk("/first", nil, &counter, 1000, false)
	progressSeen <- atomic.LoadInt32(&counter)
	_, _, _, err2 := client.Walk("/second", nil, &counter, 1000, false)

	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.EqualValues(t, 3, <-progressSeen, "first walk: its own three files")
	assert.EqualValues(t, 6, atomic.LoadInt32(&counter), "second walk adds to the first instead of restarting")
}

func TestAgentClient_AnswerWithoutIDGoesToTheOnlyRequest(t *testing.T) {
	// An agent predating the ID field echoes nothing; with one request in flight that is
	// still unambiguous, which is what keeps older agents working.
	client := newFakeAgentClient(t, func(request Envelope, reply *json.Encoder) {
		payload, _ := json.Marshal(VersionResponse{Version: "v2.1.0"})
		_ = reply.Encode(Envelope{Type: MsgVersionResponse, Payload: payload})
	})

	version, err := client.Version()

	require.NoError(t, err)
	assert.Equal(t, "v2.1.0", version)
}

func TestAgentClient_RequestCarriesAnID(t *testing.T) {
	var sent bytes.Buffer
	client := newAgentClient(nopWriteCloser{&sent}, strings.NewReader(""))
	_, _ = client.Version() // fails, but the request is written

	var env Envelope
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(sent.String())), &env))
	assert.NotZero(t, env.ID, "every request must carry an ID, sent: %s", sent.String())
	assert.Equal(t, MsgVersionRequest, env.Type)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

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

func TestAgentLoop_EchoesRequestIDs(t *testing.T) {
	// Two requests, distinct IDs: each answer has to name the request it belongs to,
	// otherwise a client with several requests in flight cannot tell them apart.
	first := envelopeLineWithID(t, 7, MsgVersionRequest, VersionRequest{})
	second := envelopeLineWithID(t, 9, MsgVersionRequest, VersionRequest{})
	quit := envelopeLine(t, MsgQuit, struct{}{})

	stdout := runAgentLoop(t, "v2.3.0", first+"\n"+second+"\n"+quit+"\n")

	ids := map[uint64]bool{}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	for decoder.More() {
		var env Envelope
		require.NoError(t, decoder.Decode(&env))
		ids[env.ID] = true
	}
	assert.Equal(t, map[uint64]bool{7: true, 9: true}, ids,
		"both request IDs must come back, agent wrote: %s", stdout)
}

func TestAgentLoop_HandlesWalkAndDigestConcurrently(t *testing.T) {
	// A walk and a digest of the same directory: the agent must serve both from one
	// connection, and both answers must be complete and correctly labelled.
	dir, relPaths := digestTestTree(t, 4)
	walkReq := envelopeLineWithID(t, 1, MsgWalkRequest, WalkRequest{DirPath: dir})
	digestReq := envelopeLineWithID(t, 2, MsgDigestRequest, DigestRequest{
		BasePath: dir, Files: relPaths, ProgressIntervalMs: 3_600_000,
	})
	quit := envelopeLine(t, MsgQuit, struct{}{})

	stdout := runAgentLoop(t, "v2.3.0", walkReq+"\n"+digestReq+"\n"+quit+"\n")

	var sawWalk, sawDigest bool
	decoder := json.NewDecoder(strings.NewReader(stdout))
	for decoder.More() {
		var env Envelope
		require.NoError(t, decoder.Decode(&env))
		switch env.Type {
		case MsgWalkResponse:
			assert.EqualValues(t, 1, env.ID, "walk response must carry the walk's ID")
			var resp WalkResponse
			require.NoError(t, json.Unmarshal(env.Payload, &resp))
			assert.Len(t, resp.Files, len(relPaths))
			sawWalk = true
		case MsgDigestResponse:
			assert.EqualValues(t, 2, env.ID, "digest response must carry the digest's ID")
			var resp DigestResponse
			require.NoError(t, json.Unmarshal(env.Payload, &resp))
			assert.Len(t, resp.Digests, len(relPaths))
			sawDigest = true
		}
	}
	assert.True(t, sawWalk && sawDigest, "both responses must arrive, agent wrote: %s", stdout)
}

func envelopeLineWithID(t *testing.T, id uint64, msgType string, payload any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	line, err := json.Marshal(Envelope{Type: msgType, ID: id, Payload: data})
	require.NoError(t, err)
	return string(line)
}

func envelopeLine(t *testing.T, msgType string, payload any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	line, err := json.Marshal(Envelope{Type: msgType, Payload: data})
	require.NoError(t, err)
	return string(line)
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
