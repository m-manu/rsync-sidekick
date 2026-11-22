package remote

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// VersionMajor, VersionMinor and VersionPatch are this release. The CLI and the
// agent both use these numbers. A protocol change bumps VersionMinor. A patch
// release stays wire-compatible with the other patches of that minor, because a
// different patch is still accepted.
const (
	VersionMajor = 2
	VersionMinor = 10
	VersionPatch = 0
)

// errAgentTooOld stops the run. The agent on the server is older than this copy.
var errAgentTooOld = errors.New("Sorry, you need to upgrade the agent on the server before running.")

// errAgentTooNew stops the run. The agent on the server is newer than this copy.
var errAgentTooNew = errors.New("This copy of rsync-sidekick is older than the agent on the server. Upgrade this copy before running.")

// Version is the release string printed by --version and reported by the agent.
func Version() string {
	return fmt.Sprintf("v%d.%d.%d", VersionMajor, VersionMinor, VersionPatch)
}

// handshakeClass is what SetupRemote does after the version exchange.
type handshakeClass int

const (
	// handshakeSFTP means no usable agent was found, so the run continues over SFTP.
	handshakeSFTP handshakeClass = iota
	// handshakeUseAgent means the agent is the same major and minor version.
	handshakeUseAgent
	// handshakeStop means an agent answered and its major or minor version differs.
	handshakeStop
)

// classifyAgentHandshake decides between using the agent, falling back to SFTP, and
// stopping. versionErr is the result of the in-band version request. probed and
// probeErr are the separate "rsync-sidekick --version" call, used only when the
// agent does not understand that request.
func classifyAgentHandshake(version string, versionErr error, probed string, probeErr error) (handshakeClass, error) {
	switch {
	case versionErr == nil:
		if err := checkAgentVersion(version); err != nil {
			return handshakeStop, err
		}
		return handshakeUseAgent, nil
	case errors.Is(versionErr, ErrVersionRequestUnsupported):
		// The agent is running. A failed probe is a failed SSH call, which keeps
		// the SFTP fallback. A version that comes back is checked like any other.
		if probeErr != nil {
			return handshakeSFTP, nil
		}
		if err := checkAgentVersion(probed); err != nil {
			return handshakeStop, err
		}
		return handshakeUseAgent, nil
	default:
		return handshakeSFTP, nil
	}
}

// checkAgentVersion accepts the same major and minor. A missing patch counts as 0,
// and a different patch is accepted. Anything that does not parse is treated as an
// older agent: a running agent that cannot name this release has to be upgraded.
func checkAgentVersion(version string) error {
	major, minor, ok := parseRelease(version)
	if !ok || major < VersionMajor || (major == VersionMajor && minor < VersionMinor) {
		return errAgentTooOld
	}
	if major > VersionMajor || minor > VersionMinor {
		return errAgentTooNew
	}
	return nil
}

// parseRelease reads a version such as "v2.10.0", "2.10.0" or "2.10". The patch
// is checked to be a number and otherwise ignored.
func parseRelease(version string) (major, minor int, ok bool) {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, false
	}
	var err error
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	if len(parts) == 3 {
		if _, err = strconv.Atoi(parts[2]); err != nil {
			return 0, 0, false
		}
	}
	return major, minor, true
}
