package remote

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/m-manu/rsync-sidekick/entity"
	"golang.org/x/crypto/ssh"
)

// AgentClient communicates with a remote rsync-sidekick agent over SSH.
type AgentClient struct {
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  *bufio.Reader
}

// NewAgentClient starts the agent process on the remote host and returns
// a client to interact with it.
func NewAgentClient(sshClient *ssh.Client, sidekickPath string) (*AgentClient, error) {
	session, err := sshClient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("SSH session failed: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("stdin pipe failed: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("stdout pipe failed: %w", err)
	}

	cmd := sidekickPath + " --agent"
	if err := session.Start(cmd); err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to start remote agent (%s): %w", cmd, err)
	}

	return &AgentClient{
		session: session,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
	}, nil
}

// Walk asks the remote agent to scan a directory.
func (c *AgentClient) Walk(dirPath string, excludedNames []string) (map[string]entity.FileMeta, int64, error) {
	req := WalkRequest{DirPath: dirPath, ExcludedNames: excludedNames}
	resp, err := c.roundTrip(MsgWalkRequest, req)
	if err != nil {
		return nil, 0, err
	}

	var walkResp WalkResponse
	if err := json.Unmarshal(resp.Payload, &walkResp); err != nil {
		return nil, 0, fmt.Errorf("bad walk response: %w", err)
	}

	files := make(map[string]entity.FileMeta, len(walkResp.Files))
	for p, fm := range walkResp.Files {
		files[p] = fm.ToEntity()
	}
	return files, walkResp.TotalSize, nil
}

// BatchDigest asks the remote agent to compute digests for a batch of files.
func (c *AgentClient) BatchDigest(basePath string, files []string) (map[string]entity.FileDigest, error) {
	req := DigestRequest{BasePath: basePath, Files: files}
	resp, err := c.roundTrip(MsgDigestRequest, req)
	if err != nil {
		return nil, err
	}

	var digestResp DigestResponse
	if err := json.Unmarshal(resp.Payload, &digestResp); err != nil {
		return nil, fmt.Errorf("bad digest response: %w", err)
	}

	digests := make(map[string]entity.FileDigest, len(digestResp.Digests))
	for p, fd := range digestResp.Digests {
		digests[p] = fd.ToEntity()
	}
	return digests, nil
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

// Close sends a quit message and closes the SSH session.
func (c *AgentClient) Close() error {
	// Best-effort quit
	c.send(MsgQuit, nil)
	c.stdin.Close()
	return c.session.Wait()
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
			return nil, fmt.Errorf("remote agent error: %s", errResp.Message)
		}
		return nil, fmt.Errorf("remote agent error (unparseable)")
	}

	return &env, nil
}
