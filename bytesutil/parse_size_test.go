package bytesutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseBinarySize_Accepted(t *testing.T) {
	cases := map[string]int64{
		"0":       0,
		"512":     512,
		"1k":      KIBI,
		"1K":      KIBI,
		"1KB":     KIBI,
		"1KiB":    KIBI,
		"512k":    512 * KIBI,
		"1m":      MEBI,
		"1M":      MEBI,
		"10MiB":   10 * MEBI,
		"2g":      2 * GIBI,
		"3G":      3 * GIBI,
		"1t":      TEBI,
		"  4M  ":  4 * MEBI,
		"1024":    KIBI,
		"1048576": MEBI,
		// BinaryFormat's own output, so a displayed size can be pasted back into the flag.
		"1.00 KiB": KIBI,
		"1.5M":     MEBI + MEBI/2,
		"2.50 GiB": 2*GIBI + GIBI/2,
	}
	for input, expected := range cases {
		actual, err := ParseBinarySize(input)
		require.NoError(t, err, "parsing %q", input)
		assert.Equal(t, expected, actual, "parsing %q", input)
	}
}

func TestParseBinarySize_Rejected(t *testing.T) {
	// "1e5M" is deliberately absent: ParseFloat reads it as 100000 MiB, which is what
	// someone typing it would mean.
	for _, input := range []string{"", "   ", "M", "k", "abc", "1x", "-5M", "1 000", "m1"} {
		_, err := ParseBinarySize(input)
		assert.Error(t, err, "%q must be rejected", input)
	}
}

func TestParseBinarySize_RoundTripsWithBinaryFormat(t *testing.T) {
	for _, size := range []int64{KIBI, MEBI, 4 * MEBI, GIBI, 2 * TEBI} {
		formatted := BinaryFormat(size)
		parsed, err := ParseBinarySize(formatted)
		require.NoError(t, err, "BinaryFormat produced %q, which must parse back", formatted)
		assert.Equal(t, size, parsed, "round trip of %d via %q", size, formatted)
	}
}
