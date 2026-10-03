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
		// Only GNU cp knows --reflink=auto; macOS clones with -c and has no fallback of its own.
		src, dst := escape(a.AbsSourcePath), escape(a.AbsDestPath)
		return fmt.Sprintf(`case "$(uname)" in `+
			`Linux) cp -pv --reflink=auto "%s" "%s" ;; `+
			`Darwin) cp -pv -c "%s" "%s" 2>/dev/null || cp -pv "%s" "%s" ;; `+
			`*) cp -pv "%s" "%s" ;; esac && %s`,
			src, dst, src, dst, src, dst, src, dst, touchCmd)
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
		if err := reflinkOrCopy(a.AbsSourcePath, a.AbsDestPath, srcInfo.Mode()); err != nil {
			return err
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

// String names what the action is about to do. A reflink says so: the two are the same
// action here, but they cost very different amounts of disk, and a log that calls both
// "copy" hides which one actually happened.
func (a CopyFileAction) String() string {
	verb := "copy"
	if a.UseReflink {
		verb = "reflink"
	}
	return fmt.Sprintf(`%s file "%s" to "%s"`, verb, sanitizePath(a.AbsSourcePath), sanitizePath(a.AbsDestPath))
}

// platform and cloneFile are variables so tests can take the other platforms' paths.
var (
	platform  = runtime.GOOS
	cloneFile = func(src, dst string) error {
		if out, err := exec.Command("cp", "-c", "-p", src, dst).CombinedOutput(); err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
		return nil
	}
)

// reflinkOrCopy reflinks src to dst where the filesystem supports it and copies it
// otherwise: FICLONE on Linux, clonefile (cp -c) on macOS, a plain copy everywhere else.
// Every reflink that ends up as a full copy is counted in reflinkFallbacks.
func reflinkOrCopy(src, dst string, mode os.FileMode) error {
	switch platform {
	case "linux":
		return reflinkCopy(src, dst, mode)
	case "darwin":
		if err := cloneFile(src, dst); err == nil {
			return nil
		}
	}
	reflinkFallbacks.Add(1)
	return regularCopyWithMode(src, dst, mode)
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
