//go:build windows

package action

import (
	"errors"
	"os"
	"syscall"
)

// errorNotSameDevice is ERROR_NOT_SAME_DEVICE, what MoveFileEx returns across volumes.
const errorNotSameDevice = syscall.Errno(17)

// isCrossDevice reports whether rename failed because source and destination are on
// different volumes.
func isCrossDevice(err error) bool {
	return errors.Is(err, errorNotSameDevice) || errors.Is(err, syscall.EXDEV)
}

// keepOwner has nothing to do on Windows: a copy gets its owner from the directory's ACL.
func keepOwner(os.FileInfo, string) error {
	return nil
}
