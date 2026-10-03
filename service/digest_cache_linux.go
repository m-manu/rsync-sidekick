//go:build linux

package service

import (
	"os"
	"syscall"
)

func statKey(info os.FileInfo) (fileKey, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileKey{}, false
	}
	return fileKey{
		dev:   uint64(st.Dev),
		ino:   uint64(st.Ino),
		size:  st.Size,
		mtime: int64(st.Mtim.Sec)*1e9 + int64(st.Mtim.Nsec),
		ctime: int64(st.Ctim.Sec)*1e9 + int64(st.Ctim.Nsec),
	}, true
}

func tryLockFile(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}
