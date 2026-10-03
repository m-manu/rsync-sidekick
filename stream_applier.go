package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/fmte"
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
	fmte.Printf("%s %d of %d destination actions while hashing (%s)\n",
		verb, s.done, s.done+s.failed, s.stats.summary())
}

// reportHashProgress prints the hashing progress of both sides until stop is closed.
func reportHashProgress(stop <-chan struct{}, sourceDone *int32, sourceTotal int32,
	destinationDone *int32, destinationTotal int32, frequency time.Duration,
) {
	if frequency <= 0 {
		return
	}
	percent := func(done *int32, total int32) float64 {
		if total == 0 {
			return 100
		}
		return 100.0 * float64(atomic.LoadInt32(done)) / float64(total)
	}
	ticker := time.NewTicker(frequency)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			fmte.Printf("%.0f%% done at source and %.0f%% done at destination\n",
				percent(sourceDone, sourceTotal), percent(destinationDone, destinationTotal))
		}
	}
}
