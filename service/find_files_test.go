package service

import (
	"github.com/m-manu/rsync-sidekick/assertions"
	"os"
	"testing"
)

func TestFindFilesFromDirectories(t *testing.T) {
	files, size, err := FindFilesFromDirectories(os.Getenv("GOROOT"), map[string]struct{}{
		".gitignore": {},
		".hidden":    {},
	})
	assertions.AssertEquals(t, nil, err)
	assertions.AssertTrue(t, len(files) > 0)
	assertions.AssertTrue(t, size > 0)
}
