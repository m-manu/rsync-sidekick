package service

import (
	"github.com/m-manu/rsync-sidekick/bytesutil"
	"github.com/stretchr/testify/assert"
	"runtime"
	"testing"
)

func TestConfig(t *testing.T) {
	assert.Equal(t, int64(0), thresholdFileSize%(4*bytesutil.KIBI))
}

func TestGetDigest(t *testing.T) {
	var paths = []string{
		runtime.GOROOT() + "/src/io/io.go",
		runtime.GOROOT() + "/src/io/pipe.go",
	}
	for _, path := range paths {
		digest, err := getDigest(path)
		assert.Equal(t, nil, err)
		assert.Greater(t, digest.FileSize, int64(0))
		assert.Equal(t, 9, len(digest.FileFuzzyHash))
		assert.Greater(t, len(digest.FileExtension), 0)
	}
}
