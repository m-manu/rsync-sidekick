package service

import (
	"github.com/stretchr/testify/assert"
	"os"
	"testing"
)

func TestFindFilesFromDirectories(t *testing.T) {
	goRoot, ok := os.LookupEnv("GOROOT")
	if !ok {
		assert.FailNow(t, "Can't run test as GOROOT is not set")
	}
	files, size, err := FindFilesFromDirectories(goRoot, map[string]struct{}{
		".gitignore": {},
		".hidden":    {},
	})
	assert.Equal(t, nil, err)
	assert.Greater(t, len(files), 0)
	assert.Greater(t, size, int64(0))
}
