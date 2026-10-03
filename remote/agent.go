package remote

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	set "github.com/deckarep/golang-set/v2"
	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
	"github.com/m-manu/rsync-sidekick/v2/lib"
	"github.com/m-manu/rsync-sidekick/v2/service"
)

// defaultDigestProgressInterval is used when a DigestRequest carries no interval,
// which is what clients older than the throttled progress reporting send.
const defaultDigestProgressInterval = 2 * time.Second

// RunAgent reads JSON-line requests from stdin, executes them locally,
// and writes JSON-line responses to stdout. This is invoked on the remote
// side via "rsync-sidekick --agent".
func RunAgent(agentVersion string) error {
	return runAgentOn(os.Stdin, os.Stdout, agentVersion)
}

// runAgentOn is RunAgent against explicit streams, which is what makes the dispatch
// loop testable.
//
// Reading is kept separate from working: a request is handed to a goroutine so the next
// one can be read right away. That is what lets a client have, say, a walk and a digest
// request in flight at once. Which requests may actually overlap is decided per kind:
//
//   - walks run one at a time, because they configure global filesystem settings
//     (one-file-system, min-size) that a second walk would overwrite;
//   - digests run one at a time, since each already saturates the disk with workers;
//   - perform requests run in a single queue, strictly in arrival order, because a copy
//     can depend on a directory an earlier action created;
//   - version requests answer immediately.
func runAgentOn(in io.Reader, out io.Writer, agentVersion string) error {
	reader := bufio.NewReader(in)
	writer := newSyncWriter(out)

	var walkMu, digestMu sync.Mutex
	var workers sync.WaitGroup

	// Perform requests go through one worker so their order is the order they arrived in;
	// a mutex would not guarantee that.
	performQueue := make(chan Envelope, 64)
	var performWorker sync.WaitGroup
	performWorker.Add(1)
	go func() {
		defer performWorker.Done()
		for env := range performQueue {
			handlePerform(writer.forRequest(env.ID), env.Payload)
		}
	}()
	shutdown := func() {
		close(performQueue)
		performWorker.Wait()
		workers.Wait()
		closeAgentDigestCache()
	}

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			shutdown()
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("agent: read error: %w", err)
		}

		line = []byte(strings.TrimSpace(string(line)))
		if len(line) == 0 {
			continue
		}

		var env Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			writeError(writer.forRequest(env.ID), fmt.Sprintf("invalid message: %v", err))
			continue
		}

		switch env.Type {
		case MsgQuit:
			shutdown()
			return nil

		case MsgWalkRequest:
			workers.Add(1)
			go func(env Envelope) {
				defer workers.Done()
				walkMu.Lock()
				defer walkMu.Unlock()
				handleWalk(writer.forRequest(env.ID), env.Payload)
			}(env)

		case MsgDigestRequest:
			workers.Add(1)
			go func(env Envelope) {
				defer workers.Done()
				digestMu.Lock()
				defer digestMu.Unlock()
				handleDigest(writer.forRequest(env.ID), env.Payload)
			}(env)

		case MsgPerformRequest:
			performQueue <- env

		case MsgVersionRequest:
			writeResponse(writer.forRequest(env.ID), MsgVersionResponse,
				VersionResponse{Version: agentVersion})

		case MsgGlobRequest:
			handleGlob(writer.forRequest(env.ID), env.Payload)

		default:
			writeError(writer.forRequest(env.ID), fmt.Sprintf("unknown message type: %s", env.Type))
		}
	}
}

func handleWalk(w *requestWriter, payload []byte) {
	var req WalkRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		writeError(w, fmt.Sprintf("bad walk request: %v", err))
		return
	}

	// Apply one-file-system setting from the client
	rsfs.DefaultOneFileSystem = req.OneFileSystem
	rsfs.DefaultMinSize = req.MinSize
	if req.WalkThreads > 0 {
		rsfs.DefaultWalkThreads = req.WalkThreads
	}

	excluded := set.NewThreadUnsafeSetWithSize[string](len(req.ExcludedNames))
	for _, name := range req.ExcludedNames {
		excluded.Add(name)
	}

	var counter int32
	var done chan struct{}
	var wg sync.WaitGroup
	if req.ProgressIntervalMs > 0 {
		done = make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(time.Duration(req.ProgressIntervalMs) * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					writeResponse(w, MsgWalkProgress, WalkProgress{FilesFound: int(atomic.LoadInt32(&counter))})
				}
			}
		}()
	}

	files, totalSize, err := service.FindFilesFromDirectory(req.DirPath, excluded, &counter)

	if done != nil {
		close(done)
		wg.Wait()
	}

	if err != nil {
		writeError(w, fmt.Sprintf("walk failed: %v", err))
		return
	}

	dirs, dirErr := service.FindDirsFromDirectory(req.DirPath, excluded)
	if dirErr != nil {
		writeError(w, fmt.Sprintf("walk dirs failed: %v", dirErr))
		return
	}

	resp := WalkResponse{
		Files:     make(map[string]FileMeta, len(files)),
		Dirs:      dirs,
		TotalSize: totalSize,
	}
	for p, fm := range files {
		resp.Files[p] = FileMetaFromEntity(fm)
	}

	writeResponse(w, MsgWalkResponse, resp)
}

func handleDigest(w *requestWriter, payload []byte) {
	var req DigestRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		writeError(w, fmt.Sprintf("bad digest request: %v", err))
		return
	}

	useAgentDigestCache(req.DigestCache)
	total := len(req.Files)

	// Hashing runs with the same parallelism as a local run, so a sync is equally fast
	// in either direction. Progress is reported on a timer rather than per file — one
	// message per file used to put len(Files) round-trips on the wire.
	interval := time.Duration(req.ProgressIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = defaultDigestProgressInterval
	}
	var counter int32
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				writeResponse(w, MsgDigestProgress,
					DigestProgress{FilesHashed: int(atomic.LoadInt32(&counter)), Total: total})
			}
		}
	}()

	digests := service.BatchDigestsParallel(nil, req.BasePath, req.Files, &counter)

	// Stop reporting before writing the response: writeResponse has a single writer.
	close(done)
	wg.Wait()

	resp := DigestResponse{
		Digests: make(map[string]FileDigest, len(digests)),
	}
	for relPath, digest := range digests {
		resp.Digests[relPath] = FileDigestFromEntity(digest)
	}

	writeResponse(w, MsgDigestResponse, resp)
}

func handleGlob(w *requestWriter, payload []byte) {
	var req GlobRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		writeError(w, fmt.Sprintf("bad glob request: %v", err))
		return
	}
	matches, err := lib.GlobDirsAll(req.Patterns)
	if err != nil {
		writeError(w, fmt.Sprintf("glob failed: %v", err))
		return
	}
	writeResponse(w, MsgGlobResponse, GlobResponse{Matches: matches})
}

func handlePerform(w *requestWriter, payload []byte) {
	var req PerformRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		writeError(w, fmt.Sprintf("bad perform request: %v", err))
		return
	}

	resp := PerformResponse{
		Results: make([]ActionResult, len(req.Actions)),
	}

	for i, spec := range req.Actions {
		resp.Results[i].Index = i
		if req.DryRun {
			resp.Results[i].Success = true
			continue
		}
		err := executeAction(spec)
		if err != nil {
			resp.Results[i].Success = false
			resp.Results[i].Error = err.Error()
		} else {
			resp.Results[i].Success = true
		}
	}

	writeResponse(w, MsgPerformResponse, resp)
}

func executeAction(spec ActionSpec) error {
	switch spec.Type {
	case "move":
		from := filepath.Join(spec.BasePath, spec.FromRelPath)
		to := filepath.Join(spec.BasePath, spec.ToRelPath)
		if _, err := os.Stat(to); err == nil {
			return fmt.Errorf("file %q already exists", to)
		}
		return os.Rename(from, to)

	case "timestamp":
		dstPath := filepath.Join(spec.DestBasePath, spec.DestRelPath)
		modTime := time.Unix(spec.ModTimestamp, 0)
		return os.Chtimes(dstPath, modTime, modTime)

	case "mkdir":
		return os.MkdirAll(spec.DirPath, os.ModeDir|os.ModePerm)

	case "copy":
		// Create parent directory
		parentDir := filepath.Dir(spec.ToAbsPath)
		if err := os.MkdirAll(parentDir, os.ModeDir|os.ModePerm); err != nil {
			return fmt.Errorf("mkdir for copy failed: %w", err)
		}
		srcInfo, err := os.Stat(spec.FromAbsPath)
		if err != nil {
			return fmt.Errorf("cannot stat source %q: %w", spec.FromAbsPath, err)
		}
		if spec.UseReflink {
			cmd := exec.Command("cp", "--reflink=auto", "-p", spec.FromAbsPath, spec.ToAbsPath)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("reflink copy failed: %w: %s", err, string(out))
			}
		} else {
			in, err := os.Open(spec.FromAbsPath)
			if err != nil {
				return fmt.Errorf("cannot open source %q: %w", spec.FromAbsPath, err)
			}
			out, err := os.Create(spec.ToAbsPath)
			if err != nil {
				in.Close()
				return fmt.Errorf("cannot create destination %q: %w", spec.ToAbsPath, err)
			}
			_, copyErr := io.Copy(out, in)
			in.Close()
			out.Close()
			if copyErr != nil {
				return fmt.Errorf("copy failed: %w", copyErr)
			}
			if err := os.Chmod(spec.ToAbsPath, srcInfo.Mode()); err != nil {
				return fmt.Errorf("chmod failed: %w", err)
			}
		}
		modTime := time.Unix(spec.ModTimestamp, 0)
		return os.Chtimes(spec.ToAbsPath, modTime, modTime)

	default:
		return fmt.Errorf("unknown action type: %s", spec.Type)
	}
}

// syncWriter serialises writes to the agent's single output stream. One response can be
// megabytes, hence several pipe writes, and two goroutines must never interleave theirs.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func newSyncWriter(w io.Writer) *syncWriter {
	return &syncWriter{w: w}
}

// forRequest returns a writer that stamps every message with the given request ID, so the
// client can tell whose message it is.
func (s *syncWriter) forRequest(id uint64) *requestWriter {
	return &requestWriter{out: s, id: id}
}

// requestWriter writes messages belonging to one request.
type requestWriter struct {
	out *syncWriter
	id  uint64
}

func (w *requestWriter) writeLine(line []byte) {
	w.out.mu.Lock()
	defer w.out.mu.Unlock()
	_, _ = w.out.w.Write(line)
}

func writeResponse(w *requestWriter, msgType string, payload interface{}) {
	data, _ := json.Marshal(payload)
	env := Envelope{Type: msgType, ID: w.id, Payload: data}
	line, _ := json.Marshal(env)
	line = append(line, '\n')
	w.writeLine(line)
}

func writeError(w *requestWriter, msg string) {
	writeResponse(w, MsgError, ErrorResponse{Message: msg})
}
