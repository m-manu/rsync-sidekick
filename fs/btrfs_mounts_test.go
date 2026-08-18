//go:build linux

package fs

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// chunkedReader hands out at most chunk bytes per Read, the way procfs does: a single read
// of /proc/self/mounts returns a few kilobytes no matter how large a buffer it is given.
type chunkedReader struct {
	data  string
	chunk int
	pos   int
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	n := len(c.data) - c.pos
	if n > c.chunk {
		n = c.chunk
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, c.data[c.pos:c.pos+n])
	c.pos += n
	return n, nil
}

// mountsLike builds a mount table with the interesting entry far past the first chunk, the
// way a docker host pushes the real filesystems behind a wall of overlay mounts.
func mountsLike(overlays int) string {
	var b strings.Builder
	b.WriteString("/dev/sda1 / btrfs rw,relatime 0 0\n")
	for i := 0; i < overlays; i++ {
		b.WriteString("overlay /var/lib/docker/overlay2/")
		b.WriteString(strings.Repeat("a", 64))
		b.WriteString("/merged overlay rw,relatime,lowerdir=/x,upperdir=/y,workdir=/z 0 0\n")
	}
	b.WriteString("/dev/sdb1 /media/raid btrfs rw,relatime,space_cache=v2 0 0\n")
	return b.String()
}

func TestParseMountFSTypes_ReadsPastTheFirstChunk(t *testing.T) {
	// The bug this guards: one read() of procfs returned ~3 KiB of a 54 KiB mount table, so
	// /media/raid was missing from the map and the BTRFS walk silently never engaged.
	table := mountsLike(200)
	assert.Greater(t, len(table), 20_000, "the interesting entry has to sit well past one chunk")

	byMountpoint := parseMountFSTypes(&chunkedReader{data: table, chunk: 3297})

	assert.Equal(t, "btrfs", byMountpoint["/media/raid"])
	assert.Equal(t, "btrfs", byMountpoint["/"])
	assert.Len(t, byMountpoint, 3, "one root, one overlay path, one raid")
}

func TestParseMountFSTypes_UnescapesMountpoints(t *testing.T) {
	byMountpoint := parseMountFSTypes(strings.NewReader(
		"/dev/sdc1 /media/my\\040disk ext4 rw 0 0\n"))
	assert.Equal(t, "ext4", byMountpoint["/media/my disk"])
}

func TestParseMountFSTypes_SkipsShortLines(t *testing.T) {
	byMountpoint := parseMountFSTypes(strings.NewReader("garbage\n\n/dev/sda1 / btrfs rw 0 0\n"))
	assert.Equal(t, map[string]string{"/": "btrfs"}, byMountpoint)
}

func TestFsTypeForPath_LongestPrefixWins(t *testing.T) {
	// /media is ext4 and /media/raid is btrfs on the host that turned this up: picking the
	// shorter match is what made IsBtrfs answer false for a path on BTRFS.
	mountFSTypeCache = map[string]string{
		"/":           "btrfs",
		"/media":      "ext4",
		"/media/raid": "btrfs",
	}
	mountFSTypeCacheOnce.Do(func() {})

	assert.Equal(t, "btrfs", fsTypeForPath("/media/raid/BackupArchiv/video/7c/43"))
	assert.Equal(t, "ext4", fsTypeForPath("/media/somewhere/else"))
}
