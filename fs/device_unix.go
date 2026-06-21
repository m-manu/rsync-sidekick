//go:build !windows

package fs

import "syscall"

// getDevice returns the device ID for a path. Uses deviceForPath hook if set, otherwise syscall.
func (l *LocalFS) getDevice(path string) (uint64, bool) {
	if l.deviceForPath != nil {
		return l.deviceForPath(path)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}
