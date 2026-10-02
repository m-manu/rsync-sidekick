package remote

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/m-manu/rsync-sidekick/v2/entity"
	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
	"github.com/m-manu/rsync-sidekick/v2/lib"
)

// AgentClient communicates with a remote rsync-sidekick agent over SSH
// using the system ssh binary.
//
// A single reader goroutine owns the connection's output and hands each message to the
// request it belongs to, matched by Envelope.ID. That is what makes concurrent requests
// possible: without it, one caller would read another caller's messages off the shared
// stream. Callers therefore never read from the connection themselves.
type AgentClient struct {
	DigestCache *DigestCacheSpec

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	sendMu sync.Mutex // one writer at a time; a large request is several pipe writes
	nextID atomic.Uint64

	pendingMu sync.Mutex
	pending   map[uint64]*pendingRequest

	readDone chan struct{} // closed when the reader goroutine stops
	readErr  error         // why it stopped; read only after readDone is closed

	// concurrent reports whether the agent echoes request IDs. Until that is known, or
	// against an older agent, operations are serialised through opMu so a response can
	// never be ambiguous.
	concurrent bool
	opMu       sync.Mutex
}

// pendingRequest collects what the reader goroutine needs to route messages of one
// request: progress updates go straight into counter, and the single terminal message
// goes to respCh.
type pendingRequest struct {
	respCh  chan *Envelope
	counter *int32
}

// NewAgentClient starts the agent process on the remote host via system ssh
// and returns a client to interact with it.
func NewAgentClient(loc Location, explicitKeyPath string, sidekickPath string) (*AgentClient, error) {
	remoteCmd := sidekickPath + " --agent"
	cmd := SSHCommand(loc, explicitKeyPath, remoteCmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe failed: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe failed: %w", err)
	}

	// Pass SSH stderr through to our stderr so connection errors are visible
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start remote agent via ssh (%s): %w", remoteCmd, err)
	}

	client := newAgentClient(stdin, stdout)
	client.cmd = cmd
	return client, nil
}

// newAgentClient wires a client to the given streams and starts its reader goroutine.
func newAgentClient(stdin io.WriteCloser, stdout io.Reader) *AgentClient {
	c := &AgentClient{
		stdin:    stdin,
		stdout:   bufio.NewReader(stdout),
		pending:  make(map[uint64]*pendingRequest),
		readDone: make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// SetConcurrent enables sending several requests at once, which is only safe once the
// agent is known to echo request IDs.
func (c *AgentClient) SetConcurrent(concurrent bool) {
	c.concurrent = concurrent
}

// IsConcurrent reports whether this connection may carry several requests at once.
// Callers use it to decide whether remote work can overlap.
func (c *AgentClient) IsConcurrent() bool {
	return c.concurrent
}

// readLoop owns the connection's output for the client's lifetime.
func (c *AgentClient) readLoop() {
	defer close(c.readDone)
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			c.readErr = fmt.Errorf("read response: %w", err)
			return
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}
		var env Envelope
		if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
			c.readErr = fmt.Errorf("unmarshal response: %w", err)
			return
		}
		c.route(&env)
	}
}

// route delivers one message to its request. Progress messages only bump a counter, so
// the reader never blocks and a caller that stopped listening cannot stall the connection.
func (c *AgentClient) route(env *Envelope) {
	pending := c.lookup(env.ID)
	if pending == nil {
		// Nothing waiting for it: a late progress message of a finished request.
		return
	}
	switch env.Type {
	case MsgWalkProgress:
		var progress WalkProgress
		if pending.counter != nil && json.Unmarshal(env.Payload, &progress) == nil {
			atomic.StoreInt32(pending.counter, int32(progress.FilesFound))
		}
	case MsgDigestProgress:
		var progress DigestProgress
		if pending.counter != nil && json.Unmarshal(env.Payload, &progress) == nil {
			atomic.StoreInt32(pending.counter, int32(progress.FilesHashed))
		}
	default:
		// respCh has room for exactly the one terminal message.
		select {
		case pending.respCh <- env:
		default:
		}
	}
}

// lookup finds the request a message belongs to. An ID of zero comes from an agent that
// doesn't echo IDs; such a client runs one request at a time, so the only pending request
// is the right one.
func (c *AgentClient) lookup(id uint64) *pendingRequest {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if id != 0 {
		return c.pending[id]
	}
	if len(c.pending) != 1 {
		return nil
	}
	for _, pending := range c.pending {
		return pending
	}
	return nil
}

// AgentError is an error the remote agent reported. Getting one back means the agent is
// alive and speaking the protocol — as opposed to an I/O error, which means it isn't.
type AgentError struct {
	Message string
}

func (e *AgentError) Error() string {
	return "remote agent error: " + e.Message
}

// ErrVersionRequestUnsupported means the agent answered the version request with an
// error, so it predates that message. The connection is fine and stays usable — only the
// version has to be obtained some other way.
var ErrVersionRequestUnsupported = errors.New("remote agent does not support version requests")

// Version asks the running agent for its version, which saves the separate
// "rsync-sidekick --version" ssh call — and with it a second password prompt.
// Returns ErrVersionRequestUnsupported for agents that don't know the message yet.
func (c *AgentClient) Version() (string, error) {
	env, err := c.roundTrip(MsgVersionRequest, VersionRequest{}, nil)
	if err != nil {
		// An error message back from the agent means it runs but doesn't know the request.
		var agentErr *AgentError
		if errors.As(err, &agentErr) {
			return "", ErrVersionRequestUnsupported
		}
		return "", err
	}
	if env.Type != MsgVersionResponse {
		return "", fmt.Errorf("unexpected message type during version request: %s", env.Type)
	}
	var resp VersionResponse
	if err := json.Unmarshal(env.Payload, &resp); err != nil {
		return "", fmt.Errorf("bad version response: %w", err)
	}
	return strings.TrimSpace(resp.Version), nil
}

// Walk asks the remote agent to scan a directory.
// counter, if non-nil, is updated atomically as the agent reports progress.
// progressIntervalMs controls how often the agent sends progress updates (0 = disabled).
// oneFileSystem prevents crossing filesystem boundaries during the scan.
// Returns files, dirs (relPath→modtime), totalSize, error.
func (c *AgentClient) Walk(dirPath string, excludedNames []string, counter *int32, progressIntervalMs int64, oneFileSystem bool) (map[string]entity.FileMeta, map[string]int64, int64, error) {
	req := WalkRequest{
		DirPath: dirPath, ExcludedNames: excludedNames, ProgressIntervalMs: progressIntervalMs,
		OneFileSystem: oneFileSystem, MinSize: rsfs.DefaultMinSize,
	}
	env, err := c.roundTrip(MsgWalkRequest, req, counter)
	if err != nil {
		return nil, nil, 0, err
	}
	if env.Type != MsgWalkResponse {
		return nil, nil, 0, fmt.Errorf("unexpected message type during walk: %s", env.Type)
	}
	var walkResp WalkResponse
	if err := json.Unmarshal(env.Payload, &walkResp); err != nil {
		return nil, nil, 0, fmt.Errorf("bad walk response: %w", err)
	}
	files := make(map[string]entity.FileMeta, len(walkResp.Files))
	totalSize := walkResp.TotalSize
	for p, fm := range walkResp.Files {
		meta := fm.ToEntity()
		// Filter again locally: an agent older than the min_size field returns everything,
		// and both sides must apply the same threshold or small files would count as
		// missing at the destination.
		if rsfs.SkipBySize(false, meta.Size) {
			totalSize -= meta.Size
			continue
		}
		files[p] = meta
	}
	if counter != nil {
		atomic.StoreInt32(counter, int32(len(files)))
	}
	return files, walkResp.Dirs, totalSize, nil
}

// BatchDigest asks the remote agent to compute digests for a batch of files.
// counter, if non-nil, is updated atomically as the agent reports progress, which it
// does every progressIntervalMs milliseconds (zero leaves the interval to the agent).
func (c *AgentClient) BatchDigest(basePath string, files []string, counter *int32,
	progressIntervalMs int64,
) (map[string]entity.FileDigest, error) {
	req := DigestRequest{BasePath: basePath, Files: files, ProgressIntervalMs: progressIntervalMs,
		DigestCache: c.DigestCache}
	env, err := c.roundTrip(MsgDigestRequest, req, counter)
	if err != nil {
		return nil, err
	}
	if env.Type != MsgDigestResponse {
		return nil, fmt.Errorf("unexpected message type during digest: %s", env.Type)
	}
	var digestResp DigestResponse
	if err := json.Unmarshal(env.Payload, &digestResp); err != nil {
		return nil, fmt.Errorf("bad digest response: %w", err)
	}
	digests := make(map[string]entity.FileDigest, len(digestResp.Digests))
	for p, fd := range digestResp.Digests {
		digest := fd.ToEntity()
		if lib.IgnoreFileExtension {
			digest.FileExtension = ""
		}
		digests[p] = digest
	}
	if counter != nil {
		atomic.StoreInt32(counter, int32(len(files)))
	}
	return digests, nil
}

// Perform asks the remote agent to execute actions. Actions of one call keep their order,
// and so do concurrent calls — the agent runs perform requests strictly in arrival order,
// because a copy can depend on a directory an earlier action created.
func (c *AgentClient) Perform(actions []ActionSpec, dryRun bool) ([]ActionResult, error) {
	req := PerformRequest{Actions: actions, DryRun: dryRun}
	env, err := c.roundTrip(MsgPerformRequest, req, nil)
	if err != nil {
		return nil, err
	}
	var performResp PerformResponse
	if err := json.Unmarshal(env.Payload, &performResp); err != nil {
		return nil, fmt.Errorf("bad perform response: %w", err)
	}
	return performResp.Results, nil
}

// Close sends a quit message and waits for the ssh process to exit.
func (c *AgentClient) Close() error {
	// Best-effort quit
	_ = c.write(0, MsgQuit, nil)
	_ = c.stdin.Close()
	if c.cmd == nil {
		return nil
	}
	return c.cmd.Wait()
}

// roundTrip sends one request and waits for its terminal message. Progress messages are
// applied to counter along the way.
func (c *AgentClient) roundTrip(msgType string, payload interface{}, counter *int32) (*Envelope, error) {
	if !c.concurrent {
		// Without ID echoing, a second request in flight would make responses ambiguous.
		c.opMu.Lock()
		defer c.opMu.Unlock()
	}

	id := c.nextID.Add(1)
	pending := &pendingRequest{respCh: make(chan *Envelope, 1), counter: counter}
	c.pendingMu.Lock()
	c.pending[id] = pending
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	if err := c.write(id, msgType, payload); err != nil {
		return nil, err
	}

	select {
	case env := <-pending.respCh:
		if env.Type == MsgError {
			var errResp ErrorResponse
			if err := json.Unmarshal(env.Payload, &errResp); err == nil {
				return nil, &AgentError{Message: errResp.Message}
			}
			return nil, &AgentError{Message: "(unparseable)"}
		}
		return env, nil
	case <-c.readDone:
		// Connection gone: report why rather than waiting forever.
		if c.readErr != nil {
			return nil, c.readErr
		}
		return nil, fmt.Errorf("remote agent connection closed")
	}
}

func (c *AgentClient) write(id uint64, msgType string, payload interface{}) error {
	var payloadBytes []byte
	if payload != nil {
		var err error
		payloadBytes, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
	}
	env := Envelope{Type: msgType, ID: id, Payload: payloadBytes}
	line, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	line = append(line, '\n')

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_, err = c.stdin.Write(line)
	return err
}
