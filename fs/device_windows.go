//go:build windows

package fs

// getDevice returns the device ID for a path. Uses deviceForPath hook if set, otherwise syscall.
func (l *LocalFS) getDevice(path string) (uint64, bool) {
	if l.deviceForPath != nil {
		return l.deviceForPath(path)
	}
	return 0, false
}
