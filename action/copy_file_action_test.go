package action

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCopyFileAction_StringNamesReflink(t *testing.T) {
	// A reflink and a full copy are the same action but not the same cost, so the log has
	// to tell them apart.
	plain := CopyFileAction{AbsSourcePath: "/a/x.bin", AbsDestPath: "/b/x.bin", SourceModTime: time.Unix(1, 0)}
	reflinked := CopyFileAction{AbsSourcePath: "/a/x.bin", AbsDestPath: "/b/x.bin", SourceModTime: time.Unix(1, 0), UseReflink: true}

	assert.Equal(t, `copy file "/a/x.bin" to "/b/x.bin"`, plain.String())
	assert.Equal(t, `reflink file "/a/x.bin" to "/b/x.bin"`, reflinked.String())
}
