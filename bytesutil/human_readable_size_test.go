package bytesutil

import (
	"github.com/m-manu/rsync-sidekick/assertions"
	"testing"
)

func TestFormats(t *testing.T) {
	tests := map[int64][2]string{
		-1:                  {"", ""},
		0:                   {"0 B", "0 B"},
		2140:                {"2.09 KiB", "2.14 KB"},
		2828382:             {"2.70 MiB", "2.83 MB"},
		2341234123412341234: {"2.03 EiB", "2.34 EB"},
	}
	for value, expectedValues := range tests {
		assertions.AssertEquals(t, expectedValues[0], BinaryFormat(value))
		assertions.AssertEquals(t, expectedValues[1], DecimalFormat(value))
	}
}
