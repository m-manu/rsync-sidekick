package lib

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGroupThousands(t *testing.T) {
	cases := map[int64]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 1155129: "1,155,129", -4507008: "-4,507,008"}
	for n, want := range cases {
		assert.Equal(t, want, GroupThousands(n), "n=%d", n)
	}
}

func TestFormatRate(t *testing.T) {
	cases := map[float64]string{0: "0/s", -1: "0/s", 0.25: "0.2/s", 3.04: "3.0/s", 350.4: "350/s",
		2400: "2.4k/s", 999_949: "999.9k/s", 1_200_000: "1.2M/s"}
	for rate, want := range cases {
		assert.Equal(t, want, FormatRate(rate), "rate=%v", rate)
	}
}

func TestFormatProgress(t *testing.T) {
	line := FormatProgress("Scanning",
		ProgressPart{Count: 1155129, Label: "src", Where: "remote", Rate: 2400},
		ProgressPart{Count: 4507008, Label: "dst", Where: "local", Done: true, Rate: 9100},
		ProgressPart{Count: 802419, Label: "arch", Where: "local", Rate: 0},
	)
	assert.Equal(t, "Scanning: 1,155,129 [2.4k/s] (src;remote) | 4,507,008 [9.1k/s] (dst;local;DONE) | "+
		"802,419 [0/s] (arch;local)", line)

	withTotal := FormatProgress("Hashing", ProgressPart{Count: 1200, Total: 5000, Label: "src", Rate: 12})
	assert.Equal(t, "Hashing: 1,200/5,000 [12/s] (src)", withTotal)
}

func TestProgress_RatePerIntervalThenAverageOnceDone(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	p := NewProgress("Scanning", start)
	at := func(s int) time.Time { return start.Add(time.Duration(s) * time.Second) }

	assert.Equal(t, "Scanning: 1,000 [100/s] (src)", p.Line(at(10), ProgressPart{Count: 1000, Label: "src"}))
	assert.Equal(t, "Scanning: 4,000 [300/s] (src)", p.Line(at(20), ProgressPart{Count: 4000, Label: "src"}),
		"while running, the rate covers only the last interval")
	assert.Equal(t, "Scanning: 6,000 [200/s] (src;DONE)",
		p.Line(at(30), ProgressPart{Count: 6000, Label: "src", Done: true}),
		"once done, the rate is the average over the whole run")
	assert.Equal(t, "Scanning: 6,000 [200/s] (src;DONE)",
		p.Line(at(60), ProgressPart{Count: 6000, Label: "src", Done: true}),
		"the average stays fixed after the part is done")
}

func TestProgress_LatePartIsTimedFromItsFirstProgress(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	p := NewProgress("Scanning", start)
	at := func(s int) time.Time { return start.Add(time.Duration(s) * time.Second) }

	p.Line(at(10), ProgressPart{Count: 0, Label: "arch"})
	p.Line(at(20), ProgressPart{Count: 0, Label: "arch"})
	p.Line(at(30), ProgressPart{Count: 500, Label: "arch"})
	line := p.Line(at(40), ProgressPart{Count: 1000, Label: "arch", Done: true})
	assert.Equal(t, "Scanning: 1,000 [50/s] (arch;DONE)", line,
		"the average starts at the tick before the first files, not at the start of the scan")
}
