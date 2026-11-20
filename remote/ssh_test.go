package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSSHCommands_PassExtraOptions(t *testing.T) {
	previous := ExtraOptions
	ExtraOptions = []string{"UserKnownHostsFile=/tmp/known_hosts", "StrictHostKeyChecking=yes"}
	defer func() { ExtraOptions = previous }()
	loc := Location{User: "backup", Host: "nas", Port: 2222}

	assert.Equal(t, []string{
		"-o", "UserKnownHostsFile=/tmp/known_hosts", "-o", "StrictHostKeyChecking=yes",
		"-l", "backup", "-p", "2222", "-i", "/key", "-T", "nas",
	}, SSHArgs(loc, "/key"))
	assert.Equal(t, []string{
		"ssh", "-o", "UserKnownHostsFile=/tmp/known_hosts", "-o", "StrictHostKeyChecking=yes",
		"-l", "backup", "-p", "2222", "-i", "/key", "-s", "nas", "sftp",
	}, SSHSubsystemCommand(loc, "/key", "sftp").Args)
}

func TestSSHCommands_NoExtraOptionsByDefault(t *testing.T) {
	assert.Equal(t, []string{"-l", "backup", "-T", "nas"}, SSHArgs(Location{User: "backup", Host: "nas"}, ""))
}
