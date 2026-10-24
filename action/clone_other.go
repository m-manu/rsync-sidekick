//go:build !darwin

package action

import "fmt"

// defaultCloneFile reports that this OS has no clonefile syscall. reflinkOrCopy only
// reaches it when a test forces the Darwin path without replacing cloneFile.
func defaultCloneFile(src, dst string) error {
	return fmt.Errorf("clonefile is unavailable for %q to %q on this OS", src, dst)
}
