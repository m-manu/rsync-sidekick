//go:build !windows

package service

import (
	"os"
	"syscall"
)

// fileInode returns the inode number of path. Hashing in inode order keeps a spinning
// disk reading forwards instead of seeking back and forth.
func fileInode(path string) (uint64, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Ino, true
}
