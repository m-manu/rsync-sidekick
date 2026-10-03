//go:build !linux

package action

import (
	"fmt"
	"os"
)

// reflinkCopy is a stub for non-linux platforms.
func reflinkCopy(src, dst string, mode os.FileMode) (bool, error) {
	return false, fmt.Errorf("reflink copy via ioctl not supported on this platform")
}
