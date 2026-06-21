package action

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
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
