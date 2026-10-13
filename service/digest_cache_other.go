//go:build !linux && !darwin && !freebsd && !netbsd

package service

import "os"

func statKey(os.FileInfo) (fileKey, bool) {
	return fileKey{}, false
}

func tryLockFile(*os.File) bool {
	return true
}
