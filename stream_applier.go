package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/fmte"
	"github.com/m-manu/rsync-sidekick/v2/lib"
)

// destApplier applies destination actions in chunks while hashing is still going on, and
// reports what it did once the destination phase is over.
type destApplier interface {
	apply(actions []action.SyncAction) error
	report()
}

// streamApplier is the destApplier of a local destination. Actions run in the order they
// come in, which is already the order they depend on (directory, then the file into it);
// moves are recorded so later copies and timestamp fixes find the file at its new path.
type streamApplier struct {
	destDirPath     string
	dryRun, verbose bool
	moved           movedFiles
	stats           actionStats
	done, failed    int
	start           time.Time
}

// newDestApplier returns the applier of a run whose destination is local, or nil where
// the actions have to be collected instead: a script needs the complete list.
func newDestApplier(outputScriptPath, destDirPath string, dryRun, verbose bool, moved movedFiles) destApplier {
	if outputScriptPath != "" {
		return nil
	}
	return &streamApplier{destDirPath: destDirPath, dryRun: dryRun, verbose: verbose, moved: moved}
}

func (s *streamApplier) apply(actions []action.SyncAction) error {
	if s.start.IsZero() {
		s.start = time.Now()
	}
	for _, a := range actions {
		a = s.moved.redirect(a)
		var err error
		if !s.dryRun {
			err = a.Perform()
		}
		s.stats.record(a, err)
		description := strings.Replace(fmt.Sprint(a), s.destDirPath+"/", "", -1)
		if err != nil {
			s.failed++
			fmte.PrintfErr("%s: failed due to: %+v\n", description, err)
			continue
		}
		s.done++
		if !s.dryRun {
			s.moved.record(a)
		}
		if s.verbose {
			fmte.Printf("%s: done\n", description)
		}
	}
	return nil
}

func (s *streamApplier) report() {
	if s.done+s.failed == 0 {
		return
	}
	verb := "Applied"
	if s.dryRun {
		verb = "Would apply"
	}
	fmte.Printf("%s %s of %s destination actions while hashing (%s)\n",
		verb, lib.GroupThousands(s.done), lib.GroupThousands(s.done+s.failed), s.stats.summary())
}

// hashSide is one side of the hashing progress.
type hashSide struct {
	done         *int32
	total        int
	label, where string
}

func (s hashSide) part() lib.ProgressPart {
	count := int64(atomic.LoadInt32(s.done))
	return lib.ProgressPart{Count: count, Total: int64(s.total), Label: s.label, Where: s.where,
		Done: count >= int64(s.total)}
}

// trackHashProgress shows the hashing progress of both sides until the returned stop is
// called.
func trackHashProgress(source, destination hashSide, frequency time.Duration) (stop func()) {
	return progressBoard.Track(frequency, "Hashing", func() []lib.ProgressPart {
		return []lib.ProgressPart{source.part(), destination.part()}
	})
}
