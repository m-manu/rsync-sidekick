package remote

import (
	"fmt"
	"strings"
	"time"

	"github.com/m-manu/rsync-sidekick/fmte"
	"golang.org/x/crypto/ssh"
)

// ProbeRemoteAgent checks whether rsync-sidekick is available on the remote host.
// Returns true if the agent can be used (remote-execution mode).
func ProbeRemoteAgent(sshClient *ssh.Client, sidekickPath string) bool {
	session, err := sshClient.NewSession()
	if err != nil {
		return false
	}
	defer session.Close()

	// Set a timeout via a goroutine — if --version hangs, we give up
	done := make(chan error, 1)
	var output []byte
	go func() {
		var runErr error
		output, runErr = session.CombinedOutput(sidekickPath + " --version")
		done <- runErr
	}()

	select {
	case err := <-done:
		if err != nil {
			fmte.PrintfV("Remote agent probe failed: %v\n", err)
			return false
		}
		version := strings.TrimSpace(string(output))
		fmte.Printf("Remote rsync-sidekick detected: %s\n", version)
		return true
	case <-time.After(10 * time.Second):
		fmte.PrintfV("Remote agent probe timed out\n")
		return false
	}
}

// SetupRemote establishes the connection and determines the mode (agent or SFTP).
// Returns either an AgentClient (remote-execution) or nil (use SFTP).
func SetupRemote(sshClient *ssh.Client, sidekickPath string, forceSFTP bool) (*AgentClient, error) {
	if forceSFTP {
		fmte.Printf("SFTP mode forced via --sftp flag\n")
		return nil, nil
	}

	if ProbeRemoteAgent(sshClient, sidekickPath) {
		client, err := NewAgentClient(sshClient, sidekickPath)
		if err != nil {
			return nil, fmt.Errorf("failed to start remote agent: %w", err)
		}
		fmte.Printf("Using remote-execution mode\n")
		return client, nil
	}

	fmte.Printf("rsync-sidekick not found on remote, falling back to SFTP mode\n")
	return nil, nil
}
