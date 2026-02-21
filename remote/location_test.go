package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseLocation_Local(t *testing.T) {
	tests := []struct {
		input string
		path  string
	}{
		{"/home/user/data", "/home/user/data"},
		{"./relative", "./relative"},
		{"../parent", "../parent"},
		{"justadirectory", "justadirectory"},
	}
	for _, tt := range tests {
		loc, err := ParseLocation(tt.input)
		assert.NoError(t, err, "input: %s", tt.input)
		assert.False(t, loc.IsRemote, "input: %s", tt.input)
		assert.Equal(t, tt.path, loc.Path, "input: %s", tt.input)
	}
}

func TestParseLocation_Remote(t *testing.T) {
	tests := []struct {
		input string
		user  string
		host  string
		port  int
		path  string
	}{
		{"user@host:/path", "user", "host", 0, "/path"},
		{"host:/path", "", "host", 0, "/path"},
		{"user@myserver.com:2222:/data/backup", "user", "myserver.com", 2222, "/data/backup"},
		{"root@10.0.0.1:/mnt/disk", "root", "10.0.0.1", 0, "/mnt/disk"},
		{"user@host:22:/path/to/dir", "user", "host", 22, "/path/to/dir"},
	}
	for _, tt := range tests {
		loc, err := ParseLocation(tt.input)
		assert.NoError(t, err, "input: %s", tt.input)
		assert.True(t, loc.IsRemote, "input: %s", tt.input)
		assert.Equal(t, tt.user, loc.User, "input: %s", tt.input)
		assert.Equal(t, tt.host, loc.Host, "input: %s", tt.input)
		assert.Equal(t, tt.port, loc.Port, "input: %s", tt.input)
		assert.Equal(t, tt.path, loc.Path, "input: %s", tt.input)
	}
}

func TestParseLocation_Errors(t *testing.T) {
	tests := []string{
		"",
		":path",  // empty host
		"@:/p",   // empty host after @
		"host:",  // empty path
	}
	for _, input := range tests {
		_, err := ParseLocation(input)
		assert.Error(t, err, "input: %s", input)
	}
}

func TestIsVersionAtLeast(t *testing.T) {
	tests := []struct {
		version string
		min     [3]int
		expect  bool
	}{
		{"v1.10.1", [3]int{1, 10, 1}, true},
		{"v1.11.0", [3]int{1, 10, 1}, true},
		{"v2.0.0", [3]int{1, 10, 1}, true},
		{"v1.10.0", [3]int{1, 10, 1}, false},
		{"v1.9.0", [3]int{1, 10, 1}, false},
		{"v1.10.2", [3]int{1, 10, 1}, true},
		{"v0.9.0", [3]int{1, 10, 1}, false},
		{"garbage", [3]int{1, 10, 1}, false},
		{"v1.9", [3]int{1, 10, 1}, false},
	}
	for _, tt := range tests {
		result := isVersionAtLeast(tt.version, tt.min)
		assert.Equal(t, tt.expect, result, "version: %s, min: %v", tt.version, tt.min)
	}
}

func TestSSHAddr(t *testing.T) {
	loc := Location{IsRemote: true, Host: "myhost", Port: 0}
	assert.Equal(t, "myhost:22", loc.SSHAddr())

	loc.Port = 2222
	assert.Equal(t, "myhost:2222", loc.SSHAddr())
}
