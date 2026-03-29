package action

import (
	"fmt"
	"io"
	"os"
	"os/exec"
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
	if a.UseReflink {
		return fmt.Sprintf(`cp -pv --reflink=auto "%s" "%s" && touch -d @%d "%s"`,
			escape(a.AbsSourcePath), escape(a.AbsDestPath),
			a.SourceModTime.Unix(), escape(a.AbsDestPath))
	}
	return fmt.Sprintf(`cp -pv "%s" "%s" && touch -d @%d "%s"`,
		escape(a.AbsSourcePath), escape(a.AbsDestPath),
		a.SourceModTime.Unix(), escape(a.AbsDestPath))
}

// Perform executes the copy action.
func (a CopyFileAction) Perform() error {
	srcInfo, err := os.Stat(a.AbsSourcePath)
	if err != nil {
		return fmt.Errorf("cannot stat source %q: %w", a.AbsSourcePath, err)
	}

	if a.UseReflink {
		cmd := exec.Command("cp", "--reflink=auto", "-p", a.AbsSourcePath, a.AbsDestPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("reflink copy failed: %w: %s", err, string(out))
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
	return fmt.Sprintf(`copy file "%s" to "%s"`, a.AbsSourcePath, a.AbsDestPath)
}

func regularCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("cannot open source %q: %w", src, err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("cannot create destination %q: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy failed from %q to %q: %w", src, dst, err)
	}
	return out.Close()
}
