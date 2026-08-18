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
	"sync/atomic"

	"github.com/m-manu/rsync-sidekick/v2/entity"
	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
)

// AgentClient communicates with a remote rsync-sidekick agent over SSH
// using the system ssh binary.
type AgentClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
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

	return &AgentClient{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
	}, nil
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
	if err := c.send(MsgVersionRequest, VersionRequest{}); err != nil {
		return "", err
	}
	env, err := c.recv()
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
	if err := c.send(MsgWalkRequest, req); err != nil {
		return nil, nil, 0, err
	}

	for {
		env, err := c.recv()
		if err != nil {
			return nil, nil, 0, err
		}
		switch env.Type {
		case MsgWalkProgress:
			if counter != nil {
				var progress WalkProgress
				if err := json.Unmarshal(env.Payload, &progress); err == nil {
					atomic.StoreInt32(counter, int32(progress.FilesFound))
				}
			}
		case MsgWalkResponse:
			var walkResp WalkResponse
			if err := json.Unmarshal(env.Payload, &walkResp); err != nil {
				return nil, nil, 0, fmt.Errorf("bad walk response: %w", err)
			}
			files := make(map[string]entity.FileMeta, len(walkResp.Files))
			totalSize := walkResp.TotalSize
			for p, fm := range walkResp.Files {
				meta := fm.ToEntity()
				// Filter again locally: an agent older than the min_size field returns
				// everything, and both sides must apply the same threshold or small files
				// would count as missing at the destination.
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
		default:
			return nil, nil, 0, fmt.Errorf("unexpected message type during walk: %s", env.Type)
		}
	}
}

// BatchDigest asks the remote agent to compute digests for a batch of files.
// counter, if non-nil, is updated atomically as the agent reports progress, which it
// does every progressIntervalMs milliseconds (zero leaves the interval to the agent).
func (c *AgentClient) BatchDigest(basePath string, files []string, counter *int32,
	progressIntervalMs int64,
) (map[string]entity.FileDigest, error) {
	req := DigestRequest{BasePath: basePath, Files: files, ProgressIntervalMs: progressIntervalMs}
	if err := c.send(MsgDigestRequest, req); err != nil {
		return nil, err
	}

	for {
		env, err := c.recv()
		if err != nil {
			return nil, err
		}
		switch env.Type {
		case MsgDigestProgress:
			if counter != nil {
				var progress DigestProgress
				if err := json.Unmarshal(env.Payload, &progress); err == nil {
					atomic.StoreInt32(counter, int32(progress.FilesHashed))
				}
			}
		case MsgDigestResponse:
			var digestResp DigestResponse
			if err := json.Unmarshal(env.Payload, &digestResp); err != nil {
				return nil, fmt.Errorf("bad digest response: %w", err)
			}
			digests := make(map[string]entity.FileDigest, len(digestResp.Digests))
			for p, fd := range digestResp.Digests {
				digests[p] = fd.ToEntity()
			}
			if counter != nil {
				atomic.StoreInt32(counter, int32(len(files)))
			}
			return digests, nil
		default:
			return nil, fmt.Errorf("unexpected message type during digest: %s", env.Type)
		}
	}
}

// Perform asks the remote agent to execute actions.
func (c *AgentClient) Perform(actions []ActionSpec, dryRun bool) ([]ActionResult, error) {
	req := PerformRequest{Actions: actions, DryRun: dryRun}
	resp, err := c.roundTrip(MsgPerformRequest, req)
	if err != nil {
		return nil, err
	}

	var performResp PerformResponse
	if err := json.Unmarshal(resp.Payload, &performResp); err != nil {
		return nil, fmt.Errorf("bad perform response: %w", err)
	}
	return performResp.Results, nil
}

// Close sends a quit message and waits for the ssh process to exit.
func (c *AgentClient) Close() error {
	// Best-effort quit
	_ = c.send(MsgQuit, nil)
	c.stdin.Close()
	return c.cmd.Wait()
}

func (c *AgentClient) roundTrip(msgType string, payload interface{}) (*Envelope, error) {
	if err := c.send(msgType, payload); err != nil {
		return nil, err
	}
	return c.recv()
}

func (c *AgentClient) send(msgType string, payload interface{}) error {
	var payloadBytes []byte
	if payload != nil {
		var err error
		payloadBytes, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
	}
	env := Envelope{Type: msgType, Payload: payloadBytes}
	line, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	line = append(line, '\n')
	_, err = c.stdin.Write(line)
	return err
}

func (c *AgentClient) recv() (*Envelope, error) {
	line, err := c.stdout.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	line = []byte(strings.TrimSpace(string(line)))

	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if env.Type == MsgError {
		var errResp ErrorResponse
		if err := json.Unmarshal(env.Payload, &errResp); err == nil {
			return nil, &AgentError{Message: errResp.Message}
		}
		return nil, &AgentError{Message: "(unparseable)"}
	}

	return &env, nil
}
