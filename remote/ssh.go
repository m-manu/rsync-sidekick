package remote

import (
	"fmt"
	"os/exec"
)

// ExtraOptions are passed to every ssh invocation as "-o option" (--ssh-option), e.g.
// "UserKnownHostsFile=/path/known_hosts" when the defaults in ~/.ssh don't fit.
var ExtraOptions []string

// optionArgs returns ExtraOptions as ssh command-line arguments.
func optionArgs() []string {
	args := make([]string, 0, 2*len(ExtraOptions))
	for _, option := range ExtraOptions {
		args = append(args, "-o", option)
	}
	return args
}

// SSHArgs builds the command-line arguments for the system ssh binary
// based on the given Location and optional explicit key path.
func SSHArgs(loc Location, explicitKeyPath string) []string {
	args := make([]string, 0, 8)
	args = append(args, optionArgs()...)
	if loc.User != "" {
		args = append(args, "-l", loc.User)
	}
	if loc.Port != 0 {
		args = append(args, "-p", fmt.Sprintf("%d", loc.Port))
	}
	if explicitKeyPath != "" {
		args = append(args, "-i", explicitKeyPath)
	}
	// Disable pseudo-terminal allocation for non-interactive use
	args = append(args, "-T")
	args = append(args, loc.Host)
	return args
}

// SSHCommand creates an exec.Cmd that runs the given remote command via system ssh.
func SSHCommand(loc Location, explicitKeyPath string, remoteCmd string) *exec.Cmd {
	args := SSHArgs(loc, explicitKeyPath)
	args = append(args, remoteCmd)
	return exec.Command("ssh", args...)
}

// SSHSubsystemCommand creates an exec.Cmd that invokes an SSH subsystem (e.g. sftp).
func SSHSubsystemCommand(loc Location, explicitKeyPath string, subsystem string) *exec.Cmd {
	args := make([]string, 0, 8)
	args = append(args, optionArgs()...)
	if loc.User != "" {
		args = append(args, "-l", loc.User)
	}
	if loc.Port != 0 {
		args = append(args, "-p", fmt.Sprintf("%d", loc.Port))
	}
	if explicitKeyPath != "" {
		args = append(args, "-i", explicitKeyPath)
	}
	args = append(args, "-s", loc.Host, subsystem)
	return exec.Command("ssh", args...)
}
