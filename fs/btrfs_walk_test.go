//go:build linux

package fs

import (
	"testing"
)

func TestBtrfsWalk_SameEntriesWithAnyNumberOfThreads(t *testing.T) {
	root := writeWalkTree(t)
	if !IsBtrfs(root) {
		t.Skipf("%s is not on BTRFS", root)
	}
	assertSameWalkWithAnyNumberOfThreads(t, func(excluded map[string]struct{}, counter *int32) ([]DirEntry, error) {
		return BtrfsWalk(root, excluded, counter)
	})
}
