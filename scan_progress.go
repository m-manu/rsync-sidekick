package main

import (
	"strconv"
	"strings"
)

// scanPart is one side of the scan progress line, e.g. "4,507,008 (dst,local) [FINISHED]".
type scanPart struct {
	count    int32
	label    string // src, dst, arch
	where    string // local, remote, or empty when everything is local
	finished bool
}

func scanProgressLine(parts ...scanPart) string {
	var b strings.Builder
	b.WriteString("Scanning: ")
	for i, part := range parts {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(groupThousands(int64(part.count)))
		b.WriteString(" (")
		b.WriteString(part.label)
		if part.where != "" {
			b.WriteString(",")
			b.WriteString(part.where)
		}
		b.WriteString(")")
		if part.finished {
			b.WriteString(" [FINISHED]")
		}
	}
	b.WriteString("...\n")
	return b.String()
}

// groupThousands writes n with a comma between every three digits: 1234567 → 1,234,567.
func groupThousands(n int64) string {
	digits := strconv.FormatInt(n, 10)
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
