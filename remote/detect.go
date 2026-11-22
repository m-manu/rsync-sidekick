package remote

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/fmte"
)

// SetupRemote determines the mode (agent or SFTP) and optionally starts an agent.
// Returns either an AgentClient (remote-execution) or nil (use SFTP).
//
// The agent is started right away and asked for its version over that same connection,
// so a run needs one ssh connection instead of two — and with password authentication,
// one prompt instead of two. Agents too old for the version request keep the previous
// behaviour: their version is fetched over a second connection.
//
// The same major and minor version is required. A mismatch stops the run. A missing
// binary, a failed ssh call, or --sftp still falls back to SFTP.
func SetupRemote(loc Location, explicitKeyPath string, sidekickPath string, forceSFTP bool) (*AgentClient, error) {
	if forceSFTP {
		fmte.Printf("SFTP mode forced via --sftp flag\n")
		return nil, nil
	}

	client, err := NewAgentClient(loc, explicitKeyPath, sidekickPath)
	if err != nil {
		fmte.Printf("Failed to start remote agent (%v), falling back to SFTP mode\n", err)
		return nil, nil
	}

	version, versionErr := client.Version()
	var probed string
	var probeErr error
	switch {
	case versionErr == nil:
		fmte.Printf("Remote rsync-sidekick detected: %s\n", version)
	case errors.Is(versionErr, ErrVersionRequestUnsupported):
		probed, probeErr = probeRemoteVersion(loc, explicitKeyPath, sidekickPath)
		if probeErr != nil {
			fmte.PrintfV("Remote agent probe failed: %v\n", probeErr)
		} else {
			fmte.Printf("Remote rsync-sidekick detected: %s\n", probed)
		}
	default:
		// No usable agent: the command is missing, or ssh failed.
		fmte.PrintfV("Remote agent handshake failed: %v\n", versionErr)
		_ = client.Close()
		fmte.Printf("rsync-sidekick not found on remote or not usable, falling back to SFTP mode\n")
		return nil, nil
	}

	class, classErr := classifyAgentHandshake(version, versionErr, probed, probeErr)
	switch class {
	case handshakeStop:
		_ = client.Close()
		return nil, classErr
	case handshakeSFTP:
		_ = client.Close()
		fmte.Printf("rsync-sidekick on remote did not report a usable version, falling back to SFTP mode\n")
		return nil, nil
	default:
		// This release's agent echoes request IDs, so overlapping requests are safe.
		client.SetConcurrent(true)
		fmte.PrintfV("Remote agent supports concurrent requests\n")
		fmte.Printf("Using remote-execution mode\n")
		return client, nil
	}
}

// probeRemoteVersion runs "rsync-sidekick --version" on the remote host. Agents that
// predate the version request are identified this way.
func probeRemoteVersion(loc Location, explicitKeyPath string, sidekickPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	args := SSHArgs(loc, explicitKeyPath)
	args = append(args, sidekickPath+" --version")
	cmd := exec.CommandContext(ctx, "ssh", args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
