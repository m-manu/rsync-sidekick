package service

import (
	"github.com/m-manu/rsync-sidekick/assertions"
	"os"
	"testing"
)

func TestConfig(t *testing.T) {
	assertions.AssertEquals(t, int64(0), thresholdFileSize%4)
}

var paths = []string{
	os.Getenv("GOROOT") + "/src/io/io.go",
	os.Getenv("GOROOT") + "/src/io/pipe.go",
}

func TestGetDigest(t *testing.T) {
	for _, path := range paths {
		digest, err := GetDigest(path)
		assertions.AssertEquals(t, nil, err)
		assertions.AssertTrue(t, digest.FileSize > 0)
		assertions.AssertEquals(t, 9, len(digest.FileFuzzyHash))
		assertions.AssertTrue(t, len(digest.FileExtension) > 0)
	}
}
