package main

import (
	"errors"
	"testing"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingPerform captures every batch it is handed and reports success for all of them.
func recordingPerform(batches *[][]remote.ActionSpec) performRemoteFunc {
	return func(specs []remote.ActionSpec, _ bool) ([]remote.ActionResult, error) {
		batch := make([]remote.ActionSpec, len(specs))
		copy(batch, specs)
		*batches = append(*batches, batch)
		results := make([]remote.ActionResult, len(specs))
		for i := range results {
			results[i] = remote.ActionResult{Index: i, Success: true}
		}
		return results, nil
	}
}

func copyActionTo(dest string) action.CopyFileAction {
	return action.CopyFileAction{
		AbsSourcePath: "/archive/source.bin",
		AbsDestPath:   dest,
		SourceModTime: time.Unix(1, 0),
	}
}

func TestRemoteArchiveActionStreamer_BatchesAndFlushes(t *testing.T) {
	var batches [][]remote.ActionSpec
	var applied int
	onAction, flush := newRemoteArchiveActionStreamer(recordingPerform(&batches), false, &applied)

	// One full batch plus a remainder: sending every action on its own would be a
	// round-trip each, which is what the batching avoids.
	const total = 70
	for i := 0; i < total; i++ {
		require.NoError(t, onAction(copyActionTo("/dest/file-"+string(rune('a'+i%26))+".bin")))
	}
	assert.Len(t, batches, 1, "a batch must go out once it is full, without waiting for flush")

	require.NoError(t, flush())
	require.Len(t, batches, 2, "flush must send the remainder")
	assert.Len(t, batches[0], 64)
	assert.Len(t, batches[1], total-64)
	assert.Equal(t, total, applied, "every action must be counted as applied")
}

func TestRemoteArchiveActionStreamer_FlushWithoutActionsSendsNothing(t *testing.T) {
	var batches [][]remote.ActionSpec
	var applied int
	_, flush := newRemoteArchiveActionStreamer(recordingPerform(&batches), false, &applied)

	require.NoError(t, flush())

	assert.Empty(t, batches, "an empty batch must not cause a round-trip")
	assert.Zero(t, applied)
}

func TestRemoteArchiveActionStreamer_KeepsActionOrder(t *testing.T) {
	var batches [][]remote.ActionSpec
	var applied int
	onAction, flush := newRemoteArchiveActionStreamer(recordingPerform(&batches), false, &applied)

	// A directory before the file that goes into it — the order the agent must preserve.
	require.NoError(t, onAction(action.MakeDirectoryAction{AbsoluteDirPath: "/dest/sub"}))
	require.NoError(t, onAction(copyActionTo("/dest/sub/file.bin")))
	require.NoError(t, flush())

	require.Len(t, batches, 1)
	require.Len(t, batches[0], 2)
	assert.Equal(t, "mkdir", batches[0][0].Type, "the directory has to come first")
	assert.Equal(t, "copy", batches[0][1].Type)
}

func TestRemoteArchiveActionStreamer_ReportsAFailedAction(t *testing.T) {
	failing := func(specs []remote.ActionSpec, _ bool) ([]remote.ActionResult, error) {
		return []remote.ActionResult{{Index: 0, Success: false, Error: "no space left on device"}}, nil
	}
	var applied int
	onAction, flush := newRemoteArchiveActionStreamer(failing, false, &applied)

	require.NoError(t, onAction(copyActionTo("/dest/file.bin")))
	err := flush()

	require.Error(t, err, "a failed copy must not be swallowed")
	assert.Contains(t, err.Error(), "no space left on device")
	assert.Zero(t, applied, "a failed batch must not count as applied")
}

func TestRemoteArchiveActionStreamer_ReportsATransportError(t *testing.T) {
	transportErr := errors.New("connection closed")
	broken := func([]remote.ActionSpec, bool) ([]remote.ActionResult, error) {
		return nil, transportErr
	}
	var applied int
	onAction, flush := newRemoteArchiveActionStreamer(broken, false, &applied)

	require.NoError(t, onAction(copyActionTo("/dest/file.bin")))
	err := flush()

	assert.ErrorIs(t, err, transportErr)
}
