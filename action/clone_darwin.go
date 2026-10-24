//go:build darwin

package action

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// defaultCloneFile clones src onto dst with clonefile(2). dst must not already exist.
// A flag value of 0 keeps the syscall's default: the clone shares data with src and
// receives its own copy of the metadata, including the mode.
func defaultCloneFile(src, dst string) error {
	if err := unix.Clonefile(src, dst, 0); err != nil {
		return fmt.Errorf("clonefile %q to %q: %w", src, dst, err)
	}
	return nil
}
