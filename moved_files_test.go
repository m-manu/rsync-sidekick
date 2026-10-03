package main

import (
	"os"
	"path/filepath"
	"testing"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/m-manu/rsync-sidekick/v2/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMovedFiles_RedirectsCopiesFromMovedSources(t *testing.T) {
	moved := movedFiles{}
	moved.record(action.MoveFileAction{BasePath: "/dst", RelativeFromPath: "old/x", RelativeToPath: "new/x"})

	redirected := moved.redirect(action.CopyFileAction{AbsSourcePath: "/dst/old/x", AbsDestPath: "/dst/b"})
	untouched := moved.redirect(action.CopyFileAction{AbsSourcePath: "/arch/y", AbsDestPath: "/dst/c"})

	assert.Equal(t, "/dst/new/x", redirected.(action.CopyFileAction).AbsSourcePath)
	assert.Equal(t, "/arch/y", untouched.(action.CopyFileAction).AbsSourcePath)
}

func TestRedirectInOrder_FollowsOnlyMovesThatComeEarlier(t *testing.T) {
	actions := []action.SyncAction{
		action.CopyFileAction{AbsSourcePath: "/dst/x", AbsDestPath: "/dst/before"},
		action.MoveFileAction{BasePath: "/dst", RelativeFromPath: "x", RelativeToPath: "a"},
		action.CopyFileAction{AbsSourcePath: "/dst/x", AbsDestPath: "/dst/b"},
		action.PropagateTimestampAction{DestinationBaseDirPath: "/dst", DestinationFileRelativePath: "x"},
	}

	redirectInOrder(actions)

	assert.Equal(t, "/dst/x", actions[0].(action.CopyFileAction).AbsSourcePath, "x is still there at that point")
	assert.Equal(t, "/dst/a", actions[2].(action.CopyFileAction).AbsSourcePath)
	assert.Equal(t, "a", actions[3].(action.PropagateTimestampAction).DestinationFileRelativePath)
}

func TestLocalArchiveStreamer_VanishedSourceIsSkippedNotFatal(t *testing.T) {
	dst := t.TempDir()
	var applied int
	streamer := newLocalArchiveActionStreamer(&applied, movedFiles{})

	err := streamer(action.CopyFileAction{
		AbsSourcePath: filepath.Join(t.TempDir(), "deleted-meanwhile.bin"),
		AbsDestPath:   filepath.Join(dst, "target.bin"),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrActionSkipped)
	assert.Equal(t, 0, applied)
}

func TestMovedFiles_RebaseWalksFollowsMovesWithoutTouchingSharedMaps(t *testing.T) {
	moved := movedFiles{}
	moved.record(action.MoveFileAction{BasePath: "/dst", RelativeFromPath: "x.bin", RelativeToPath: "sub/a.bin"})
	moved.record(action.MoveFileAction{BasePath: "/dst", RelativeFromPath: "arch/y.bin", RelativeToPath: "b.bin"})
	shared := map[string]entity.FileMeta{"x.bin": {Size: 1}, "keep.bin": {Size: 2}, "arch/y.bin": {Size: 3}}
	walks := []service.ArchiveWalk{
		{Path: "/dst", Files: shared},
		{Path: "/dst/arch", Files: map[string]entity.FileMeta{"y.bin": {Size: 3}}},
		{Path: "/other", Files: map[string]entity.FileMeta{"z.bin": {Size: 4}}},
	}

	rebased := moved.rebaseWalks(walks)

	assert.Equal(t, map[string]entity.FileMeta{"sub/a.bin": {Size: 1}, "keep.bin": {Size: 2}, "b.bin": {Size: 3}},
		rebased[0].Files, "moves within the archive keep the entry under its new path")
	assert.Empty(t, rebased[1].Files, "a file moved out of the archive path is dropped")
	assert.Equal(t, walks[2].Files, rebased[2].Files)
	assert.Contains(t, shared, "x.bin", "the original map, possibly the destination's list, stays as it was")
}

// recordingApplier notes in order when destination actions are applied.
type recordingApplier struct {
	inner destApplier
	order *[]string
}

func (r *recordingApplier) apply(actions []action.SyncAction) error {
	*r.order = append(*r.order, "destination")
	return r.inner.apply(actions)
}

func (r *recordingApplier) report() { r.inner.report() }

// writeSame writes the same content to every path.
func writeSame(t *testing.T, content string, paths ...string) {
	t.Helper()
	for _, path := range paths {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
}

func TestDestinationActionsRunBeforeTheArchiveScan(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	content := string(make([]byte, 50_000)) + "moved and copied"
	// Source wants the content at a.bin and b.bin; the destination has it only at x.bin.
	writeSame(t, content, filepath.Join(src, "a.bin"), filepath.Join(src, "b.bin"), filepath.Join(dst, "x.bin"))

	var order []string
	moved := movedFiles{}
	applyDest := &recordingApplier{inner: newDestApplier("", dst, false, false, moved), order: &order}
	var applied int
	streamer := newLocalArchiveActionStreamer(&applied, moved)
	onArchive := func(a action.SyncAction) error {
		order = append(order, "archive")
		return streamer(a)
	}

	// Without -c the destination phase only moves x.bin to one orphan; the archive (the
	// destination itself, walked before that move) serves the other one from x.bin.
	actions, err := getSyncActionsWithProgressFS("test", src, nil, set.NewSet[string](), dst, nil,
		false, 0, false, false, []string{dst}, onArchive, applyDest, moved)

	require.NoError(t, err)
	assert.Equal(t, "destination", order[0], "order: %v", order)
	assert.Contains(t, order, "archive")
	assert.Empty(t, actions, "applied actions must not be returned a second time")
	for _, name := range []string{"a.bin", "b.bin"} {
		got, readErr := os.ReadFile(filepath.Join(dst, name))
		require.NoError(t, readErr, "%s must exist", name)
		assert.Equal(t, content, string(got))
	}
	assert.NoFileExists(t, filepath.Join(dst, "x.bin"), "x.bin was moved, the archive copy followed it")
}
