//go:build windows

package service

// fileInode reports no inode on Windows, so callers keep their path ordering.
func fileInode(_ string) (uint64, bool) {
	return 0, false
}
