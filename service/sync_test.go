package service

import (
	"testing"

	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/stretchr/testify/assert"
)

func TestGetParallelism(t *testing.T) {
	for i := 1; i < 7; i++ {
		p1, p2 := getParallelism(i)
		assert.GreaterOrEqual(t, p1, 1)
		assert.GreaterOrEqual(t, p2, 1)
		if i > 1 {
			assert.LessOrEqual(t, p1+p2, i)
		}
	}
}

func TestComputeSyncActions_RaceOnErrorCollection(t *testing.T) {
	sourceFiles := map[string]entity.FileMeta{}
	destFiles := map[string]entity.FileMeta{}

	// create many fake files to force parallel indexing
	orphans := []string{}
	candidates := []string{}
	for i := 0; i < 200; i++ {
		name := "file" + string(rune(i))
		sourceFiles[name] = entity.FileMeta{
			Size:              100,
			ModifiedTimestamp: int64(i),
		}
		destFiles[name] = entity.FileMeta{
			Size:              100,
			ModifiedTimestamp: int64(i),
		}
		orphans = append(orphans, name)
		candidates = append(candidates, name)
	}

	var srcCounter int32
	var dstCounter int32

	// paths don't exist -> buildIndex will generate errors
	_, _, _ = ComputeSyncActionsWithFS(
		nil,
		nil,
		"/nonexistent/source",
		sourceFiles,
		orphans,
		"/nonexistent/dest",
		destFiles,
		candidates,
		&srcCounter,
		&dstCounter,
		false,
		false,
	)
}
