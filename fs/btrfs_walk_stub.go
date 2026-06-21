//go:build !linux

package fs

import "fmt"

var ErrNotBtrfs = fmt.Errorf("not a btrfs filesystem")

func IsBtrfs(path string) bool {
	return false
}

func BtrfsWalk(dirPath string, excludedNames map[string]struct{}, counter *int32) ([]DirEntry, error) {
	return nil, ErrNotBtrfs
}
