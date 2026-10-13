package lib

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addPhase adds a phase to the board without starting its ticker, so line can be driven
// with chosen times.
func addPhase(b *ProgressBoard, title string, start time.Time, parts func() []ProgressPart) *progressPhase {
	phase := &progressPhase{progress: NewProgress(title, start), parts: parts, shownDone: make(map[string]bool)}
	b.phases = append(b.phases, phase)
	return phase
}

func TestProgressBoard_OverlappingPhasesShareOneLine(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	at := func(s int) time.Time { return start.Add(time.Duration(s) * time.Second) }
	b := NewProgressBoard(func(string) {})
	var src, dst, arch, hashed int64
	var srcDone, dstDone int32
	scan := addPhase(b, "Scanning", start, func() []ProgressPart {
		return []ProgressPart{
			{Count: src, Label: "src", Where: "remote", Done: atomic.LoadInt32(&srcDone) == 1},
			{Count: dst, Label: "dst", Where: "local", Done: atomic.LoadInt32(&dstDone) == 1},
			{Count: arch, Label: "arch", Where: "local"},
		}
	})

	src, dst, arch = 1000, 2000, 0
	assert.Equal(t, "Scanning: 1,000 [100/s] (src;remote) | 2,000 [200/s] (dst;local) | 0 [0/s] (arch;local)",
		b.line(at(10)))

	src, dst, arch, dstDone = 1500, 4000, 500, 1
	assert.Equal(t, "Scanning: 1,500 [50/s] (src;remote) | 4,000 [200/s] (dst;local;DONE) | 500 [50/s] (arch;local)",
		b.line(at(20)), "a part that is done shows once, with its average rate")
	src, arch, srcDone = 3000, 1500, 1
	assert.Equal(t, "Scanning: 3,000 [100/s] (src;remote;DONE) | 1,500 [100/s] (arch;local)",
		b.line(at(30)), "and is left out from then on")

	addPhase(b, "Hashing", at(30), func() []ProgressPart {
		return []ProgressPart{{Count: hashed, Total: 800, Label: "src", Where: "remote"}}
	})
	arch, hashed = 2500, 200
	assert.Equal(t, "Scanning: 2,500 [100/s] (arch;local) | Hashing: 200/800 [20/s] (src;remote)", b.line(at(40)),
		"a phase that starts is appended; the title goes with each phase's first part")

	scan.ended = true
	arch, hashed = 3000, 400
	assert.Equal(t, "Scanning: 3,000 [75/s] (arch;local;DONE) | Hashing: 400/800 [20/s] (src;remote)",
		b.line(at(50)), "an ended phase shows its remaining parts done, once, at their average rate")
	hashed = 600
	assert.Equal(t, "Hashing: 600/800 [20/s] (src;remote)", b.line(at(60)))
	assert.Len(t, b.phases, 1, "the ended phase is dropped")
}

// lines collects what a board prints.
type lines struct {
	mu  sync.Mutex
	all []string
}

func (l *lines) print(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.all = append(l.all, line)
}

func (l *lines) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.all...)
}

func TestProgressBoard_LastPhasePrintsItsEndRightAwayAndNothingAfter(t *testing.T) {
	var printed lines
	b := NewProgressBoard(printed.print)
	var count atomic.Int64
	stop := b.Track(5*time.Millisecond, "Applying", func() []ProgressPart {
		return []ProgressPart{{Count: count.Load(), Total: 10, Label: "actions", Note: "3 moved"}}
	})
	count.Store(4)
	require.Eventually(t, func() bool { return len(printed.get()) > 0 }, time.Second, time.Millisecond)

	count.Store(10)
	stop()
	afterStop := printed.get()
	last := afterStop[len(afterStop)-1]
	assert.True(t, strings.HasPrefix(last, "Applying: 10/10 ["), "got %q", last)
	assert.True(t, strings.HasSuffix(last, "(actions;DONE) - 3 moved"), "got %q", last)

	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, afterStop, printed.get(), "nothing is printed once the last phase stopped")
	stop()
	assert.Equal(t, afterStop, printed.get(), "stopping twice is harmless")

	again := b.Track(5*time.Millisecond, "Hashing", func() []ProgressPart {
		return []ProgressPart{{Count: 1, Label: "src"}}
	})
	require.Eventually(t, func() bool { return len(printed.get()) > len(afterStop) }, time.Second, time.Millisecond,
		"the board starts ticking again for a new phase")
	again()
}

func TestProgressBoard_ZeroFrequencyShowsNothing(t *testing.T) {
	var printed lines
	b := NewProgressBoard(printed.print)
	stop := b.Track(0, "Hashing", func() []ProgressPart { return []ProgressPart{{Count: 1, Label: "src"}} })
	stop()
	assert.Empty(t, printed.get())
}
