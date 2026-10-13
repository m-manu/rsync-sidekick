package lib

import (
	"strings"
	"sync"
	"time"
)

// ProgressBoard prints the progress of every phase that is running in one line per tick,
// so phases that overlap (the archive walk and the hashing, say) don't print lines in
// turns:
//
//	Scanning: 4,153,422 [3.1k/s] (arch;local) | Hashing: 0/812,331 [0/s] (src;remote) | 0/95,120 [0/s] (dst;local)
//
// A part that is done is shown once with DONE and its average rate, then left out. A
// phase that ends shows its remaining parts that way too: on the next tick while other
// phases still run, right away when it was the last one.
type ProgressBoard struct {
	print  func(line string)
	mu     sync.Mutex
	phases []*progressPhase
	stop   chan struct{}
	done   chan struct{}
}

type progressPhase struct {
	progress  *Progress
	parts     func() []ProgressPart
	shownDone map[string]bool
	ended     bool
}

// NewProgressBoard returns a board that hands each line, without line end, to print.
func NewProgressBoard(print func(line string)) *ProgressBoard {
	return &ProgressBoard{print: print}
}

// Track adds a phase whose parts are read on every tick, until the returned stop function
// is called. The first phase starts the ticker with its frequency; when the last phase
// stops, the ticker stops with it and nothing is printed after stop returns. A frequency
// of zero or less disables the progress of this phase.
func (b *ProgressBoard) Track(frequency time.Duration, title string, parts func() []ProgressPart) (stop func()) {
	if frequency <= 0 {
		return func() {}
	}
	phase := &progressPhase{progress: NewProgress(title, time.Now()), parts: parts,
		shownDone: make(map[string]bool)}
	b.mu.Lock()
	b.phases = append(b.phases, phase)
	if b.stop == nil {
		b.stop, b.done = make(chan struct{}), make(chan struct{})
		go b.tick(frequency, b.stop, b.done)
	}
	b.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { b.end(phase) }) }
}

func (b *ProgressBoard) end(phase *progressPhase) {
	b.mu.Lock()
	phase.ended = true
	running := 0
	for _, p := range b.phases {
		if !p.ended {
			running++
		}
	}
	if running > 0 {
		b.mu.Unlock()
		return
	}
	// Nothing else runs: print the last line now rather than on the next tick, so it
	// doesn't trail behind what the caller prints next.
	if line := b.line(time.Now()); line != "" {
		b.print(line)
	}
	stop, done := b.stop, b.done
	b.stop, b.done = nil, nil
	close(stop)
	b.mu.Unlock()
	<-done
}

func (b *ProgressBoard) tick(frequency time.Duration, stop, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(frequency)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			b.mu.Lock()
			select {
			case <-stop:
				b.mu.Unlock()
				return
			default:
			}
			if line := b.line(time.Now()); line != "" {
				b.print(line)
			}
			b.mu.Unlock()
		}
	}
}

// line renders one tick and drops the phases that ended. b.mu must be held.
func (b *ProgressBoard) line(now time.Time) string {
	var segments []string
	running := b.phases[:0]
	for _, phase := range b.phases {
		parts := phase.parts()
		if phase.ended {
			for i := range parts {
				parts[i].Done = true
			}
		} else {
			running = append(running, phase)
		}
		phase.progress.setRates(now, parts)
		first := true
		for _, part := range parts {
			if part.Done {
				if phase.shownDone[part.Label] {
					continue
				}
				phase.shownDone[part.Label] = true
			}
			segment := formatPart(part)
			if first {
				segment = phase.progress.title + ": " + segment
				first = false
			}
			segments = append(segments, segment)
		}
	}
	b.phases = running
	return strings.Join(segments, " | ")
}
