package lib

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ProgressPart is one counter of a progress line, e.g. "1,200/5,000 [2.4k/s] (src;remote;DONE)".
type ProgressPart struct {
	Count int64
	Total int64  // shown as Count/Total when known
	Label string // src, dst, arch, groups ...
	Where string // local, remote, or empty when it doesn't matter
	Done  bool
	Rate  float64 // per second; set by Progress.Line
	Note  string  // shown after the part, e.g. a breakdown of what was done
}

// FormatProgress writes "Title: part | part" without a line end, so callers can add
// "..." or a summary.
func FormatProgress(title string, parts ...ProgressPart) string {
	formatted := make([]string, len(parts))
	for i, part := range parts {
		formatted[i] = formatPart(part)
	}
	return title + ": " + strings.Join(formatted, " | ")
}

func formatPart(part ProgressPart) string {
	count := GroupThousands(part.Count)
	if part.Total > 0 {
		count += "/" + GroupThousands(part.Total)
	}
	tags := []string{part.Label}
	if part.Where != "" {
		tags = append(tags, part.Where)
	}
	if part.Done {
		tags = append(tags, "DONE")
	}
	formatted := count + " [" + FormatRate(part.Rate) + "] (" + strings.Join(tags, ";") + ")"
	if part.Note != "" {
		formatted += " - " + part.Note
	}
	return formatted
}

// Progress keeps the rate of every part of a progress line from one tick to the next:
// the rate over the last interval while a part runs, the average over its whole run once
// it is done.
type Progress struct {
	title string
	start time.Time
	rates map[string]*progressRate
}

func NewProgress(title string, start time.Time) *Progress {
	return &Progress{title: title, start: start, rates: make(map[string]*progressRate)}
}

// Line formats one tick of the progress, without a line end.
func (p *Progress) Line(now time.Time, parts ...ProgressPart) string {
	p.setRates(now, parts)
	return FormatProgress(p.title, parts...)
}

func (p *Progress) setRates(now time.Time, parts []ProgressPart) {
	for i, part := range parts {
		rate, known := p.rates[part.Label]
		if !known {
			rate = &progressRate{start: p.start, lastAt: p.start}
			p.rates[part.Label] = rate
		}
		parts[i].Rate = rate.update(now, part.Count, part.Done)
	}
}

// progressRate follows one counter. A part that starts late (the archive walk waits for
// the destination) is timed from its first progress. Start and end are known to one
// progress interval.
type progressRate struct {
	start, lastAt, doneAt time.Time
	last, doneCount       int64
	started               bool
}

func (r *progressRate) update(now time.Time, count int64, done bool) float64 {
	if !r.started && count > 0 {
		r.start, r.started = r.lastAt, true
	}
	if done {
		if r.doneAt.IsZero() {
			r.doneAt, r.doneCount = now, count
		}
		return perSecond(r.doneCount, r.doneAt.Sub(r.start))
	}
	rate := perSecond(count-r.last, now.Sub(r.lastAt))
	r.last, r.lastAt = count, now
	return rate
}

func perSecond(count int64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(count) / elapsed.Seconds()
}

// FormatRate writes a per-second rate short enough for a progress line: 0.004/s, 0.04/s,
// 0.3/s, 350/s, 2.4k/s, 1.2M/s. Below 1/s it takes as many decimals as its first digit
// needs, up to three; anything smaller shows as 0/s.
func FormatRate(perSecond float64) string {
	switch {
	case perSecond < 0.0005:
		return "0/s"
	case perSecond < 0.0095:
		return strconv.FormatFloat(perSecond, 'f', 3, 64) + "/s"
	case perSecond < 0.095:
		return strconv.FormatFloat(perSecond, 'f', 2, 64) + "/s"
	case perSecond < 10:
		return strconv.FormatFloat(perSecond, 'f', 1, 64) + "/s"
	case perSecond < 1000:
		return strconv.FormatFloat(perSecond, 'f', 0, 64) + "/s"
	case perSecond < 1_000_000:
		return strconv.FormatFloat(perSecond/1000, 'f', 1, 64) + "k/s"
	default:
		return strconv.FormatFloat(perSecond/1_000_000, 'f', 1, 64) + "M/s"
	}
}

// integer is every integer type a count, size or index in the output can have.
type integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

// GroupThousands writes n with a comma between every three digits: 1234567 → 1,234,567.
// Every number in the human-readable output goes through it; files meant for tools don't.
func GroupThousands[T integer](n T) string {
	digits := fmt.Sprintf("%d", n)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	var b strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(digit)
	}
	return sign + b.String()
}
