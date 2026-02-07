package remote

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/m-manu/rsync-sidekick/fmte"
)

// ProbeRemoteAgent checks whether rsync-sidekick is available on the remote host.
// Returns true if the agent can be used (remote-execution mode).
func ProbeRemoteAgent(loc Location, explicitKeyPath string, sidekickPath string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Build SSH args and run with timeout context
	args := SSHArgs(loc, explicitKeyPath)
	args = append(args, sidekickPath+" --version")
	cmd := exec.CommandContext(ctx, "ssh", args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		fmte.PrintfV("Remote agent probe failed: %v\n", err)
		return false
	}
	version := strings.TrimSpace(string(output))
	fmte.Printf("Remote rsync-sidekick detected: %s\n", version)
	return true
}

// SetupRemote determines the mode (agent or SFTP) and optionally starts an agent.
// Returns either an AgentClient (remote-execution) or nil (use SFTP).
func SetupRemote(loc Location, explicitKeyPath string, sidekickPath string, forceSFTP bool) (*AgentClient, error) {
	if forceSFTP {
		fmte.Printf("SFTP mode forced via --sftp flag\n")
		return nil, nil
	}

	if ProbeRemoteAgent(loc, explicitKeyPath, sidekickPath) {
		client, err := NewAgentClient(loc, explicitKeyPath, sidekickPath)
		if err != nil {
			return nil, fmt.Errorf("failed to start remote agent: %w", err)
		}
		fmte.Printf("Using remote-execution mode\n")
		return client, nil
	}

	fmte.Printf("rsync-sidekick not found on remote, falling back to SFTP mode\n")
	return nil, nil
}
