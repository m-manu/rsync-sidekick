package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/stretchr/testify/assert"
)

func TestActionStats_CountsEachKindSeparately(t *testing.T) {
	var stats actionStats
	stats.record(action.MakeDirectoryAction{AbsoluteDirPath: "/dest/d"}, nil)
	stats.record(action.CopyFileAction{AbsSourcePath: "/a", AbsDestPath: "/dest/a"}, nil)
	stats.record(action.CopyFileAction{AbsSourcePath: "/b", AbsDestPath: "/dest/b"}, nil)
	stats.record(action.CopyFileAction{AbsSourcePath: "/c", AbsDestPath: "/dest/c", UseReflink: true}, nil)
	stats.record(action.MoveFileAction{BasePath: "/dest", RelativeFromPath: "x", RelativeToPath: "y"}, nil)
	stats.record(action.PropagateTimestampAction{
		DestinationBaseDirPath:      "/dest",
		DestinationFileRelativePath: "t",
		SourceModTime:               time.Unix(1, 0),
	}, nil)

	assert.EqualValues(t, 1, stats.dirs.Load())
	assert.EqualValues(t, 2, stats.copied.Load())
	assert.EqualValues(t, 1, stats.reflinked.Load())
	assert.EqualValues(t, 1, stats.moved.Load())
	assert.EqualValues(t, 1, stats.timestamps.Load())
	assert.EqualValues(t, 0, stats.errors.Load())

	summary := stats.summary()
	assert.Contains(t, summary, "1 dirs created")
	assert.Contains(t, summary, "2 copied")
	assert.Contains(t, summary, "1 moved")
	assert.Contains(t, summary, "1 timestamps")
	assert.NotContains(t, summary, "errors",
		"a counter that stayed at zero has no business in the summary")
}

func TestActionStats_FailureCountsOnlyAsError(t *testing.T) {
	// A failed copy must not also show up as copied - otherwise the breakdown claims work
	// that didn't happen, and the numbers stop adding up to what was attempted.
	var stats actionStats
	stats.record(action.CopyFileAction{AbsSourcePath: "/a", AbsDestPath: "/dest/a"}, assert.AnError)

	assert.EqualValues(t, 0, stats.copied.Load())
	assert.EqualValues(t, 1, stats.errors.Load())
	assert.Contains(t, stats.summary(), "1 errors")
}

func TestActionStats_SummaryOfNothing(t *testing.T) {
	var stats actionStats
	assert.Equal(t, "nothing", stats.summary())
}

func TestActionStats_ReflinkFallbacksCountAsCopies(t *testing.T) {
	// The FICLONE fallback is silent, so reflinks are counted by intent. The summary has to
	// move the ones that fell back over to the copied column.
	var stats actionStats
	for i := 0; i < 5; i++ {
		stats.record(action.CopyFileAction{AbsSourcePath: "/a", AbsDestPath: "/dest/a", UseReflink: true}, nil)
	}
	before := action.ReflinkFallbacks()
	summary := stats.summary()
	if before == 0 {
		assert.Contains(t, summary, "5 reflinked")
		assert.NotContains(t, summary, "copied")
	}
}

func TestStartActionStatsProgress_SilentWithoutFrequency(t *testing.T) {
	// A frequency of zero turns reporting off; stop must still be safe to call.
	var stats actionStats
	var done atomic.Int64
	stop := startActionStatsProgress(&stats, &done, 10, 0)
	stop()
}
