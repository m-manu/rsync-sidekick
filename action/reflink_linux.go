//go:build linux

package action

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// reflinkSupport caches whether a mountpoint supports FICLONE.
// Populated lazily on first attempt per mountpoint.
var reflinkSupport sync.Map // mountpoint (string) → supported (bool)

// mountpointForPath returns the longest matching mountpoint from /proc/self/mounts.
// Falls back to "/" if no match found. Result is cached after first /proc read.
var mountCache map[string]struct{}
var mountCacheOnce sync.Once

func loadMountpoints() {
	mountCache = make(map[string]struct{})
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return
	}
	for _, line := range splitLines(string(data)) {
		fields := splitFields(line)
		if len(fields) >= 2 {
			mountCache[fields[1]] = struct{}{}
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	for len(s) > 0 {
		i := 0
		for i < len(s) && s[i] != '\n' {
			i++
		}
		lines = append(lines, s[:i])
		if i < len(s) {
			i++
		}
		s = s[i:]
	}
	return lines
}

func splitFields(s string) []string {
	var fields []string
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		j := i
		for j < len(s) && s[j] != ' ' && s[j] != '\t' {
			j++
		}
		if j > i {
			fields = append(fields, s[i:j])
		}
		i = j
	}
	return fields
}

func mountpointForPath(path string) string {
	mountCacheOnce.Do(loadMountpoints)
	abs, _ := filepath.Abs(path)
	best := "/"
	for mp := range mountCache {
		if len(mp) > len(best) && (abs == mp || (len(abs) > len(mp) && abs[:len(mp)] == mp && abs[len(mp)] == '/')) {
			best = mp
		}
	}
	return best
}

// FICLONE = _IOW(0x94, 9, int) = 0x40049409
const ficlone = 0x40049409

// reflinkCopy creates a reflink copy using FICLONE ioctl directly, avoiding fork+exec of cp.
// Caches reflink support per mountpoint: first call tries ioctl, subsequent calls skip if unsupported.
func reflinkCopy(src, dst string, mode os.FileMode) error {
	mp := mountpointForPath(dst)
	if supported, ok := reflinkSupport.Load(mp); ok && !supported.(bool) {
		return regularCopyWithMode(src, dst, mode)
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("cannot open source %q: %w", src, err)
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("cannot create destination %q: %w", dst, err)
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, dstFile.Fd(), ficlone, srcFile.Fd())
	if errno != 0 {
		dstFile.Close()
		os.Remove(dst)
		reflinkSupport.Store(mp, false)
		return regularCopyWithMode(src, dst, mode)
	}

	reflinkSupport.Store(mp, true)
	return dstFile.Close()
}
