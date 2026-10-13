package main

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/lib"
)

// actionStats counts what was performed, broken down by kind. One line every few seconds
// says more about a run of a million actions than a million lines do, so the per-action
// output is left to --verbose and this is what a normal run reports.
type actionStats struct {
	copied     atomic.Int64
	reflinked  atomic.Int64
	moved      atomic.Int64
	timestamps atomic.Int64
	dirs       atomic.Int64
	errors     atomic.Int64
}

// record accounts for one performed action. A failure is only counted as an error, not
// also as its kind, so the counters plus errors add up to the number of actions attempted.
func (s *actionStats) record(a action.SyncAction, err error) {
	if err != nil {
		s.errors.Add(1)
		return
	}
	switch act := a.(type) {
	case action.CopyFileAction:
		if act.UseReflink {
			s.reflinked.Add(1)
		} else {
			s.copied.Add(1)
		}
	case action.MoveFileAction:
		s.moved.Add(1)
	case action.PropagateTimestampAction:
		s.timestamps.Add(1)
	case action.MakeDirectoryAction:
		s.dirs.Add(1)
	}
}

// summary lists the counters that are non-zero, in the order actions typically depend on
// each other. An empty breakdown reads as "nothing", not as an empty string.
//
// A reflink request silently falls back to a full copy where the filesystem has no
// FICLONE, so those are moved over to the copied column here - reporting them as
// reflinked would claim a space saving that didn't happen.
func (s *actionStats) summary() string {
	fallbacks := action.ReflinkFallbacks()
	reflinked := s.reflinked.Load()
	copied := s.copied.Load()
	if fallbacks > reflinked {
		fallbacks = reflinked
	}
	reflinked -= fallbacks
	copied += fallbacks

	parts := make([]string, 0, 6)
	add := func(count int64, label string) {
		if count > 0 {
			parts = append(parts, lib.GroupThousands(count)+" "+label)
		}
	}
	add(s.dirs.Load(), "dirs created")
	add(copied, "copied")
	add(reflinked, "reflinked")
	add(s.moved.Load(), "moved")
	add(min(action.MovesAsCopies(), s.moved.Load()), "of them copied (rename can't cross filesystems or subvolumes)")
	add(s.timestamps.Load(), "timestamps")
	add(s.errors.Load(), "errors")
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}

// startActionStatsProgress reports the running breakdown until the returned stop is
// called. done is read for how far through the action list the run is.
func startActionStatsProgress(stats *actionStats, done *atomic.Int64, total int,
	progressFrequency time.Duration,
) (stop func()) {
	return progressBoard.Track(progressFrequency, "Applying", func() []lib.ProgressPart {
		return []lib.ProgressPart{{Count: done.Load(), Total: int64(total), Label: "actions", Note: stats.summary()}}
	})
}
