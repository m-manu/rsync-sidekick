package action

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
)

// CopyFileAction is a SyncAction for copying a file locally at the destination
// (or from an archive path) to avoid re-transferring via rsync.
type CopyFileAction struct {
	AbsSourcePath string
	AbsDestPath   string
	SourceModTime time.Time
	UseReflink    bool
	// FS is the destination filesystem. Nil means the local disk. A non-nil FS is an
	// SFTP destination: Perform refuses the copy instead of opening those path strings
	// on the local machine.
	FS rsfs.FileSystem
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
	if a.FS != nil {
		return fmt.Errorf("cannot copy %q over SFTP (remote-exec copies on the destination host; otherwise rsync transfers the file)", a.AbsDestPath)
	}
	srcInfo, err := os.Stat(a.AbsSourcePath)
	if err != nil {
		return fmt.Errorf("cannot stat source %q: %w", a.AbsSourcePath, err)
	}

	if a.UseReflink {
		cloned, err := reflinkOrCopy(a.AbsSourcePath, a.AbsDestPath, srcInfo.Mode())
		if err != nil {
			return err
		}
		if !cloned {
			reflinkFallbacks.Add(1)
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
// cloned tells whether it became a reflink. The destination path is never opened until
// the bytes are complete, and an existing destination is left alone.
func reflinkOrCopy(src, dst string, mode os.FileMode) (cloned bool, err error) {
	switch platform {
	case "linux":
		return reflinkCopy(src, dst, mode)
	case "darwin":
		if err := cloneIntoTemp(src, dst); err == nil {
			return true, nil
		} else if errors.Is(err, os.ErrExist) {
			return false, err
		}
	}
	return false, regularCopyWithMode(src, dst, mode)
}

// cloneIntoTemp points cp -c at a temporary file and renames that onto dst. cp -c on the
// final path would truncate dst before the clone finished.
func cloneIntoTemp(src, dst string) error {
	if err := errIfDestExists(dst); err != nil {
		return err
	}
	tmp, err := createCopyTemp(dst)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("failed to close temporary file %q: %w", tmpName, err)
	}
	if err := cloneFile(src, tmpName); err != nil {
		os.Remove(tmpName)
		return err
	}
	return publishNewFile(tmpName, dst)
}

func regularCopyWithMode(src, dst string, mode os.FileMode) error {
	if err := regularCopy(src, dst); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// regularCopy writes src to a uniquely named temporary file beside dst and renames it
// into place once the copy has finished. An existing dst is left alone. On a handled
// failure only that temporary file is removed. A kill can leave a ".rsync-sidekick-*"
// file in the destination directory; a later run does not delete it.
func regularCopy(src, dst string) error {
	if err := errIfDestExists(dst); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("cannot open source %q: %w", src, err)
	}
	defer in.Close()

	out, err := createCopyTemp(dst)
	if err != nil {
		return err
	}
	tmpName := out.Name()

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmpName)
		return fmt.Errorf("copy failed from %q to %q: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("failed to close temporary file %q: %w", tmpName, err)
	}
	return publishNewFile(tmpName, dst)
}

// createCopyTemp creates an empty file in dst's directory. The rename onto dst stays on
// one filesystem, which is what makes it atomic.
func createCopyTemp(dst string) (*os.File, error) {
	dir := filepath.Dir(dst)
	f, err := os.CreateTemp(dir, ".rsync-sidekick-")
	if err != nil {
		return nil, fmt.Errorf("cannot create temporary file in %q: %w", dir, err)
	}
	return f, nil
}

// errIfDestExists reports os.ErrExist when anything already stands at dst, including a
// symlink. The copy refuses that path rather than replacing it.
func errIfDestExists(dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("file %q already exists: %w", dst, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// publishNewFile renames a finished temporary file onto dst. dst must still be absent.
// The temporary file is removed when the rename does not happen.
func publishNewFile(tmpName, dst string) error {
	if err := errIfDestExists(dst); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("failed to rename %q to %q: %w", tmpName, dst, err)
	}
	return nil
}
