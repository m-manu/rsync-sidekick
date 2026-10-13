package remote

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// digestTestTree writes fileCount small files and returns the directory plus their
// relative paths.
func digestTestTree(t *testing.T, fileCount int) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	relPaths := make([]string, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		rel := filepath.Join("sub", "file-"+strconv.Itoa(i)+".txt")
		require.NoError(t, os.WriteFile(filepath.Join(dir, rel),
			[]byte("contents of file "+strconv.Itoa(i)), 0o644))
		relPaths = append(relPaths, rel)
	}
	return dir, relPaths
}

// runDigestHandler invokes the handler and splits its output into the progress messages
// and the final response.
func runDigestHandler(t *testing.T, req DigestRequest) ([]DigestProgress, DigestResponse) {
	t.Helper()
	payload, err := json.Marshal(req)
	require.NoError(t, err)

	var buf bytes.Buffer
	handleDigest(newSyncWriter(&buf).forRequest(0), payload)

	var progress []DigestProgress
	var resp DigestResponse
	sawResponse := false
	decoder := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for decoder.More() {
		var env Envelope
		require.NoError(t, decoder.Decode(&env))
		switch env.Type {
		case MsgDigestProgress:
			var p DigestProgress
			require.NoError(t, json.Unmarshal(env.Payload, &p))
			progress = append(progress, p)
		case MsgDigestResponse:
			require.NoError(t, json.Unmarshal(env.Payload, &resp))
			sawResponse = true
		default:
			t.Fatalf("unexpected message type from digest handler: %s (payload %s)",
				env.Type, string(env.Payload))
		}
	}
	require.True(t, sawResponse, "handler must end with a digest_response, output was: %s", buf.String())
	return progress, resp
}

func TestAgentDigest_MatchesLocalDigests(t *testing.T) {
	// More files than workers, so the parallel split is exercised.
	dir, relPaths := digestTestTree(t, 64)

	_, resp := runDigestHandler(t, DigestRequest{
		BasePath:           dir,
		Files:              relPaths,
		ProgressIntervalMs: 3_600_000,
	})

	require.Len(t, resp.Digests, len(relPaths))
	for _, rel := range relPaths {
		expected, err := service.GetDigest(filepath.Join(dir, rel))
		require.NoError(t, err, "local digest of %s", rel)
		assert.Equal(t, FileDigestFromEntity(expected), resp.Digests[rel],
			"agent digest of %q must equal the locally computed one", rel)
	}
}

func TestAgentDigest_ProgressIsThrottledNotPerFile(t *testing.T) {
	dir, relPaths := digestTestTree(t, 32)

	// An interval an hour out cannot elapse during the run, so a throttled reporter stays
	// silent. The old implementation emitted one message per file regardless.
	progress, resp := runDigestHandler(t, DigestRequest{
		BasePath:           dir,
		Files:              relPaths,
		ProgressIntervalMs: 3_600_000,
	})

	assert.Empty(t, progress,
		"no progress message may be sent before the interval elapses, got %d", len(progress))
	assert.Len(t, resp.Digests, len(relPaths), "throttling must not cost any results")
}

func TestAgentDigest_OmittedIntervalUsesAgentDefault(t *testing.T) {
	// A client older than the throttling change sends no interval at all.
	dir, relPaths := digestTestTree(t, 8)
	req := DigestRequest{BasePath: dir, Files: relPaths}
	require.Zero(t, req.ProgressIntervalMs)

	progress, resp := runDigestHandler(t, req)

	assert.Len(t, resp.Digests, len(relPaths), "digests must be returned regardless of interval")
	assert.Less(t, len(progress), len(relPaths),
		"the %ds default must not produce one message per file", int(defaultDigestProgressInterval.Seconds()))
}

func TestAgentDigest_SkipsUnreadableFiles(t *testing.T) {
	dir, relPaths := digestTestTree(t, 4)
	withMissing := append([]string{filepath.Join("sub", "missing.txt")}, relPaths...)

	_, resp := runDigestHandler(t, DigestRequest{
		BasePath:           dir,
		Files:              withMissing,
		ProgressIntervalMs: 3_600_000,
	})

	assert.Len(t, resp.Digests, len(relPaths),
		"a file that cannot be hashed must be skipped, not abort the batch")
	assert.NotContains(t, resp.Digests, filepath.Join("sub", "missing.txt"))
}

func TestAgentDigest_RejectsMalformedRequest(t *testing.T) {
	var buf bytes.Buffer
	handleDigest(newSyncWriter(&buf).forRequest(0), []byte("{not json"))

	var env Envelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	assert.Equal(t, MsgError, env.Type, "malformed request must yield an error message, got: %s", buf.String())
}

func TestDigestRequest_IntervalSurvivesTheWire(t *testing.T) {
	data, err := json.Marshal(DigestRequest{BasePath: "/base", Files: []string{"a"}, ProgressIntervalMs: 5000})
	require.NoError(t, err)

	var decoded DigestRequest
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.EqualValues(t, 5000, decoded.ProgressIntervalMs)

	// omitempty keeps the field off the wire for the zero value, so old agents see the
	// exact same request they always did.
	dataWithoutInterval, err := json.Marshal(DigestRequest{BasePath: "/base", Files: []string{"a"}})
	require.NoError(t, err)
	assert.NotContains(t, string(dataWithoutInterval), "progress_interval_ms")
}

func TestAgentDigest_UsesRequestedDigestCache(t *testing.T) {
	closeAgentDigestCache()
	dir, relPaths := digestTestTree(t, 8)
	cachePath := filepath.Join(t.TempDir(), "remote-digests.tsv")
	time.Sleep(2100 * time.Millisecond)
	req := DigestRequest{BasePath: dir, Files: relPaths, ProgressIntervalMs: 3_600_000,
		DigestCache: &DigestCacheSpec{Path: cachePath}}

	_, first := runDigestHandler(t, req)
	c := service.ActiveDigestCache()
	require.NotNil(t, c, "a digest request with a cache spec must open the cache")
	_, second := runDigestHandler(t, req)
	hits, misses := c.Stats()
	closeAgentDigestCache()

	assert.Equal(t, first.Digests, second.Digests)
	assert.Equal(t, [2]int64{int64(len(relPaths)), int64(len(relPaths))}, [2]int64{hits, misses},
		"second request must be answered from the cache")
	assert.Nil(t, service.ActiveDigestCache(), "shutdown must close the agent's cache")
	data, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	assert.Equal(t, len(relPaths)+1, bytes.Count(data, []byte("\n")), "header plus one line per file:\n%s", data)
}
