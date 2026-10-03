//go:build !windows

package service

import (
	"os"
	"syscall"
)

// hardlinkIdentity returns device and inode of a file that has more than one name.
func hardlinkIdentity(info os.FileInfo) (fileIdentity, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink < 2 {
		return fileIdentity{}, false
	}
	return fileIdentity{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true
}
