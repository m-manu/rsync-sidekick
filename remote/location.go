package remote

import (
	"fmt"
	"strconv"
	"strings"
)

// Location represents either a local path or a remote user@host:path.
type Location struct {
	IsRemote bool
	User     string // empty = current user
	Host     string
	Port     int // 0 = default (22)
	Path     string
}

// ParseLocation parses a CLI argument into a Location.
//
// Rules:
//   - Starts with "/", "./", or "../" → local
//   - Contains ":" → remote (user@host:path or user@host:port:path)
//   - Everything else → local
func ParseLocation(arg string) (Location, error) {
	if arg == "" {
		return Location{}, fmt.Errorf("empty path argument")
	}

	// Clearly local paths
	if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") {
		return Location{Path: arg}, nil
	}

	// Check for remote format: [user@]host:[port:]path
	colonIdx := strings.Index(arg, ":")
	if colonIdx < 0 {
		// No colon → local
		return Location{Path: arg}, nil
	}

	hostPart := arg[:colonIdx]
	rest := arg[colonIdx+1:]

	if hostPart == "" {
		return Location{}, fmt.Errorf("empty host in remote path %q", arg)
	}

	loc := Location{IsRemote: true}

	// Parse user@host
	if atIdx := strings.Index(hostPart, "@"); atIdx >= 0 {
		loc.User = hostPart[:atIdx]
		loc.Host = hostPart[atIdx+1:]
	} else {
		loc.Host = hostPart
	}

	if loc.Host == "" {
		return Location{}, fmt.Errorf("empty host in remote path %q", arg)
	}

	// Check if rest starts with port:path  (digits followed by colon)
	if secondColon := strings.Index(rest, ":"); secondColon > 0 {
		possiblePort := rest[:secondColon]
		if port, err := strconv.Atoi(possiblePort); err == nil && port > 0 && port <= 65535 {
			loc.Port = port
			rest = rest[secondColon+1:]
		}
	}

	if rest == "" {
		return Location{}, fmt.Errorf("empty path in remote spec %q", arg)
	}

	loc.Path = rest
	return loc, nil
}

// SSHAddr returns the host:port string for SSH connection.
func (l Location) SSHAddr() string {
	port := l.Port
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("%s:%d", l.Host, port)
}

// SSHSpec returns a string like "user@host" or "host" suitable for display and ssh commands.
func (l Location) SSHSpec() string {
	if l.User != "" {
		return l.User + "@" + l.Host
	}
	return l.Host
}
