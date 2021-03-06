package service

import (
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/stretchr/testify/assert"
	"runtime"
	"testing"
)

func TestFindFilesFromDirectories(t *testing.T) {
	files, size, err := FindFilesFromDirectory(runtime.GOROOT(), entity.StringSetOf(".gitignore", ".hidden"))
	assert.Equal(t, nil, err)
	assert.Greater(t, len(files), 0)
	assert.Greater(t, size, int64(0))
}
