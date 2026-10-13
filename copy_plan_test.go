package main

import (
	"testing"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/stretchr/testify/assert"
)

func TestUnresolvedOrphans_LeavesOnlyWhatNoActionServes(t *testing.T) {
	actions := []action.SyncAction{
		action.MoveFileAction{BasePath: "/dst", RelativeFromPath: "old/a", RelativeToPath: "a"},
		action.CopyFileAction{AbsSourcePath: "/dst/x", AbsDestPath: "/dst/b"},
		action.PropagateTimestampAction{SourceFileRelativePath: "c", DestinationFileRelativePath: "c",
			SourceModTime: time.Unix(1, 0)},
		// a timestamp fix on another path only prepares a move or copy, it serves nothing
		action.PropagateTimestampAction{SourceFileRelativePath: "d", DestinationFileRelativePath: "elsewhere",
			SourceModTime: time.Unix(1, 0)},
	}
	streamed := set.NewSet("e")

	got := unresolvedOrphans([]string{"a", "b", "c", "d", "e", "f"}, actions, "/dst/", streamed)

	assert.Equal(t, []string{"d", "f"}, got)
}

func TestRecordResolvedOrphans_RecordsOnlyAppliedCopies(t *testing.T) {
	resolved := set.NewSet[string]()
	onAction := recordResolvedOrphans(func(a action.SyncAction) error { return nil }, "/dst", resolved)

	_ = onAction(action.MakeDirectoryAction{AbsoluteDirPath: "/dst/dir"})
	_ = onAction(action.CopyFileAction{AbsSourcePath: "/arch/f", AbsDestPath: "/dst/dir/f"})

	assert.ElementsMatch(t, []string{"dir/f"}, resolved.ToSlice())
}
