package remote

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/fmte"
)

// minAgentVersion is the minimum remote rsync-sidekick version required for agent mode.
var minAgentVersion = [3]int{1, 10, 6}

// ProbeRemoteAgent checks whether rsync-sidekick is available on the remote host
// and whether its version is at least minAgentVersion.
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

	if !isVersionAtLeast(version, minAgentVersion) {
		fmte.Printf("Remote version %s is too old for agent mode (need >= v%d.%d.%d), falling back to SFTP\n",
			version, minAgentVersion[0], minAgentVersion[1], minAgentVersion[2])
		return false
	}

	return true
}

// SetupRemote determines the mode (agent or SFTP) and optionally starts an agent.
// Returns either an AgentClient (remote-execution) or nil (use SFTP).
//
// The agent is started right away and asked for its version over that same connection,
// so a run needs one ssh connection instead of two — and with password authentication,
// one prompt instead of two. Agents too old for the version request keep the previous
// behaviour: their version is fetched over a second connection.
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
	switch {
	case versionErr == nil:
		fmte.Printf("Remote rsync-sidekick detected: %s\n", version)
		if !isVersionAtLeast(version, minAgentVersion) {
			fmte.Printf("Remote version %s is too old for agent mode (need >= v%d.%d.%d), falling back to SFTP\n",
				version, minAgentVersion[0], minAgentVersion[1], minAgentVersion[2])
			_ = client.Close()
			return nil, nil
		}
	case errors.Is(versionErr, ErrVersionRequestUnsupported):
		// The agent is running and speaks the protocol, it just predates this message.
		// Keep the connection and spend a second one on "--version", as before.
		if !ProbeRemoteAgent(loc, explicitKeyPath, sidekickPath) {
			_ = client.Close()
			fmte.Printf("rsync-sidekick on remote is too old, falling back to SFTP mode\n")
			return nil, nil
		}
	default:
		// No usable agent: the command is missing, or ssh failed.
		fmte.PrintfV("Remote agent handshake failed: %v\n", versionErr)
		_ = client.Close()
		fmte.Printf("rsync-sidekick not found on remote or not usable, falling back to SFTP mode\n")
		return nil, nil
	}

	fmte.Printf("Using remote-execution mode\n")
	return client, nil
}

// isVersionAtLeast parses a version string like "v1.10.0" and checks if it's >= min.
func isVersionAtLeast(version string, min [3]int) bool {
	version = strings.TrimPrefix(version, "v")
	parts := strings.SplitN(version, ".", 3)
	if len(parts) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return false
		}
		if n > min[i] {
			return true
		}
		if n < min[i] {
			return false
		}
	}
	return true // equal
}
