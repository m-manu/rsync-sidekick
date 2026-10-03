package remote

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRealAgentClient connects a client to a real agent loop through pipes.
func newRealAgentClient(t *testing.T) *AgentClient {
	t.Helper()
	requestsR, requestsW := io.Pipe()
	repliesR, repliesW := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runAgentOn(requestsR, repliesW, "test")
		_ = repliesW.Close()
	}()
	client := newAgentClient(requestsW, repliesR)
	client.SetConcurrent(true)
	t.Cleanup(func() {
		_ = requestsW.Close()
		<-done
	})
	return client
}

func TestAgentWalk_ChunkedResultEqualsALocalWalk(t *testing.T) {
	root := t.TempDir()
	for d := 0; d < 5; d++ {
		dir := filepath.Join(root, fmt.Sprintf("d%d", d), "sub")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		for f := 0; f < 9; f++ {
			path := filepath.Join(dir, fmt.Sprintf("f%d.txt", f))
			require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("%d-%d", d, f)), 0o644))
			mtime := time.Unix(int64(1_700_000_000+d*100+f), 0)
			require.NoError(t, os.Chtimes(path, mtime, mtime))
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "skip.me"), []byte("x"), 0o644))
	previous := walkChunkSize
	walkChunkSize = 7 // 45 files and 10 directories: several chunks and a partial last one
	t.Cleanup(func() { walkChunkSize = previous })

	var counter int32 = 100
	files, dirs, size, err := newRealAgentClient(t).Walk(root, []string{"skip.me"}, &counter, 0, false)

	require.NoError(t, err)
	wantFiles, wantDirs, wantSize, err := service.FindFilesAndDirsFromDirectory(root, set.NewSet("skip.me"), nil)
	require.NoError(t, err)
	assert.Equal(t, wantFiles, files)
	assert.Equal(t, wantDirs, dirs)
	assert.Equal(t, wantSize, size)
	assert.Len(t, files, 45)
	assert.EqualValues(t, 145, atomic.LoadInt32(&counter), "the counter grows from where it stood by the files received")
}

func TestAgentWalk_LostChunkIsAnError(t *testing.T) {
	client := newFakeAgentClient(t, func(request Envelope, reply *json.Encoder) {
		chunk, _ := json.Marshal(WalkChunk{Entries: []WalkEntry{{Path: "a", Size: 1}, {Path: "b", Size: 1}}})
		_ = reply.Encode(Envelope{Type: MsgWalkChunk, ID: request.ID, Payload: chunk})
		walkResp, _ := json.Marshal(WalkResponse{Entries: 3, Chunked: true})
		_ = reply.Encode(Envelope{Type: MsgWalkResponse, ID: request.ID, Payload: walkResp})
	})

	_, _, _, err := client.Walk("/dir", nil, nil, 0, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "received 2 of 3 entries")
}
