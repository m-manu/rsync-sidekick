package service

import (
	set "github.com/deckarep/golang-set/v2"
	"github.com/stretchr/testify/assert"
	"runtime"
	"testing"
)

func TestFindFilesFromDirectories(t *testing.T) {
	files, size, err := FindFilesFromDirectory(runtime.GOROOT(), set.NewThreadUnsafeSet(".gitignore", ".hidden"))
	assert.Equal(t, nil, err)
	assert.Greater(t, len(files), 0)
	assert.Greater(t, size, int64(0))
}
