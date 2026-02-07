package bytesutil

import (
	"testing"
)

func FuzzBinaryFormat(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(1024))
	f.Add(int64(2140))
	f.Add(int64(-1))
	f.Fuzz(func(t *testing.T, size int64) {
		BinaryFormat(size)
	})
}

func FuzzDecimalFormat(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(1000))
	f.Add(int64(2140))
	f.Add(int64(-1))
	f.Fuzz(func(t *testing.T, size int64) {
		DecimalFormat(size)
	})
}
