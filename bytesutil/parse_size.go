package bytesutil

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// ParseBinarySize turns a human-written size into bytes. A bare number is bytes; the
// suffixes k, m, g and t are binary multiples (1k = 1024), matching BinaryFormat's output.
// Case and a "b"/"ib" tail are accepted, as is a fractional value, so "10M", "10m",
// "10MB", "10MiB" all mean the same and BinaryFormat's own "1.00 KiB" parses back.
//
// Whitespace is only allowed around the value and between number and unit — "1 000" stays
// an error rather than silently becoming 1000.
func ParseBinarySize(s string) (int64, error) {
	text := strings.TrimSpace(s)
	if text == "" {
		return 0, fmt.Errorf("empty size")
	}

	// Split off the trailing unit letters, whatever they are.
	unitStart := len(text)
	for unitStart > 0 && unicode.IsLetter(rune(text[unitStart-1])) {
		unitStart--
	}
	number := strings.TrimSpace(text[:unitStart])
	unit := strings.ToLower(text[unitStart:])

	multiplier := int64(1)
	switch strings.TrimSuffix(strings.TrimSuffix(unit, "b"), "i") {
	case "":
		if unit != "" && unit != "b" {
			return 0, fmt.Errorf("size %q has an unknown unit %q", s, unit)
		}
	case "k":
		multiplier = KIBI
	case "m":
		multiplier = MEBI
	case "g":
		multiplier = GIBI
	case "t":
		multiplier = TEBI
	default:
		return 0, fmt.Errorf("size %q has an unknown unit %q", s, unit)
	}

	if number == "" {
		return 0, fmt.Errorf("size %q has no number", s)
	}
	value, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q is not a number followed by k, m, g or t", s)
	}
	if value < 0 {
		return 0, fmt.Errorf("size %q is negative", s)
	}
	bytes := value * float64(multiplier)
	if bytes > math.MaxInt64/2 {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return int64(math.Round(bytes)), nil
}
