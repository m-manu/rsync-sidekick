package action

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// CopyFileAction is a SyncAction for copying a file locally at the destination
// (or from an archive path) to avoid re-transferring via rsync.
type CopyFileAction struct {
	AbsSourcePath string
	AbsDestPath   string
	SourceModTime time.Time
	UseReflink    bool
}

func (a CopyFileAction) sourcePath() string {
	return a.AbsSourcePath
}

func (a CopyFileAction) destinationPath() string {
	return a.AbsDestPath
}

// UnixCommand generates the shell command for this copy action.
func (a CopyFileAction) UnixCommand() string {
	timestamp := a.SourceModTime.Format("200601021504.05")
	touchCmd := fmt.Sprintf(`touch -m -a -t "%s" "%s"`, timestamp, escape(a.AbsDestPath))

	if a.UseReflink {
		return fmt.Sprintf(`if [ "$(uname)" = "Darwin" ]; then cp -pv -c "%s" "%s"; else cp -pv --reflink=auto "%s" "%s"; fi && %s`,
			escape(a.AbsSourcePath), escape(a.AbsDestPath),
			escape(a.AbsSourcePath), escape(a.AbsDestPath),
			touchCmd)
	}
	return fmt.Sprintf(`cp -pv "%s" "%s" && %s`,
		escape(a.AbsSourcePath), escape(a.AbsDestPath),
		touchCmd)
}

// Perform executes the copy action.
func (a CopyFileAction) Perform() error {
	srcInfo, err := os.Stat(a.AbsSourcePath)
	if err != nil {
		return fmt.Errorf("cannot stat source %q: %w", a.AbsSourcePath, err)
	}

	if a.UseReflink {
		if runtime.GOOS == "linux" {
			if err := reflinkCopy(a.AbsSourcePath, a.AbsDestPath, srcInfo.Mode()); err != nil {
				return err
			}
		} else {
			// macOS: use cp -c
			cmd := exec.Command("cp", "-c", "-p", a.AbsSourcePath, a.AbsDestPath)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("reflink copy failed: %w: %s", err, string(out))
			}
		}
	} else {
		if err := regularCopy(a.AbsSourcePath, a.AbsDestPath); err != nil {
			return err
		}
		if err := os.Chmod(a.AbsDestPath, srcInfo.Mode()); err != nil {
			return fmt.Errorf("chmod failed on %q: %w", a.AbsDestPath, err)
		}
	}

	return os.Chtimes(a.AbsDestPath, a.SourceModTime, a.SourceModTime)
}

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

// Uniqueness is keyed on destination path — same source can serve multiple copies.
func (a CopyFileAction) Uniqueness() string {
	return "cp" + cmdSeparator + a.AbsDestPath
}

func (a CopyFileAction) String() string {
	return fmt.Sprintf(`copy file "%s" to "%s"`, sanitizePath(a.AbsSourcePath), sanitizePath(a.AbsDestPath))
}

func regularCopyWithMode(src, dst string, mode os.FileMode) error {
	if err := regularCopy(src, dst); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func regularCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("cannot open source %q: %w", src, err)
	}
	defer in.Close()

	tmpDst := dst + ".tmp"
	out, err := os.Create(tmpDst)
	if err != nil {
		return fmt.Errorf("cannot create destination %q: %w", tmpDst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmpDst)
		return fmt.Errorf("copy failed from %q to %q: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmpDst)
		return fmt.Errorf("failed to close destination %q: %w", tmpDst, err)
	}

	if err := os.Rename(tmpDst, dst); err != nil {
		os.Remove(tmpDst)
		return fmt.Errorf("failed to rename %q to %q: %w", tmpDst, dst, err)
	}
	return nil
}
