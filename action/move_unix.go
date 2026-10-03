//go:build !windows

package action

import (
	"errors"
	"os"
	"syscall"
)

// isCrossDevice reports whether rename failed because source and destination are on
// different filesystems or BTRFS subvolumes.
func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// keepOwner gives path the owner and group of info, as a move would have kept them. Only
// root can do that; anyone else owns the copy anyway.
func keepOwner(info os.FileInfo, path string) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() != 0 {
		return nil
	}
	return os.Lchown(path, int(st.Uid), int(st.Gid))
}
