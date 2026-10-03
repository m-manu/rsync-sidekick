package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGroupThousands(t *testing.T) {
	cases := map[int64]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 1155129: "1,155,129", -4507008: "-4,507,008"}
	for n, want := range cases {
		assert.Equal(t, want, groupThousands(n), "n=%d", n)
	}
}

func TestScanProgressLine(t *testing.T) {
	line := scanProgressLine(
		scanPart{1155129, "src", "remote", false},
		scanPart{4507008, "dst", "local", true},
		scanPart{802419, "arch", "local", false},
	)
	assert.Equal(t, "Scanning: 1,155,129 (src,remote), 4,507,008 (dst,local) [FINISHED], 802,419 (arch,local)...\n", line)

	allLocal := scanProgressLine(scanPart{12, "src", "", true}, scanPart{3, "dst", "", false})
	assert.Equal(t, "Scanning: 12 (src) [FINISHED], 3 (dst)...\n", allLocal)
}
