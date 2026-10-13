//go:build darwin || freebsd || netbsd

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
		mtime: int64(st.Mtimespec.Sec)*1e9 + int64(st.Mtimespec.Nsec),
		ctime: int64(st.Ctimespec.Sec)*1e9 + int64(st.Ctimespec.Nsec),
	}, true
}

func tryLockFile(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}
