package action

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
)

// MoveFileAction is a SyncAction for moving or renaming a file
type MoveFileAction struct {
	BasePath         string
	RelativeFromPath string
	RelativeToPath   string
	FS               rsfs.FileSystem // optional; if nil, uses os.* directly
}

func (a MoveFileAction) sourcePath() string {
	return filepath.Join(a.BasePath, a.RelativeFromPath)
}

func (a MoveFileAction) destinationPath() string {
	return filepath.Join(a.BasePath, a.RelativeToPath)
}

// UnixCommand for moving or renaming a file
func (a MoveFileAction) UnixCommand() string {
	return fmt.Sprintf(`mv -v -n "%s" "%s"`, escape(a.sourcePath()), escape(a.destinationPath()))
}

// Perform 'file move/rename' action
func (a MoveFileAction) Perform() error {
	if a.FS != nil {
		_, err := a.FS.Stat(a.destinationPath())
		if err == nil {
			return fmt.Errorf(`error: file "%s" already exists`, a.destinationPath())
		}
		return a.FS.Rename(a.sourcePath(), a.destinationPath())
	}
	if _, err := os.Stat(a.destinationPath()); err == nil {
		return fmt.Errorf(`error: file "%s" already exists`, a.destinationPath())
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err := os.Rename(a.sourcePath(), a.destinationPath())
	if isCrossDevice(err) {
		return copyInsteadOfMove(a.sourcePath(), a.destinationPath())
	}
	return err
}

// copyInsteadOfMove handles a move that rename can't do: across filesystems, and across
// BTRFS subvolumes of one filesystem. Like mv, it copies the file - as a reflink where
// possible, so within one BTRFS it takes no space - but unlike mv it keeps the original,
// since rsync-sidekick never deletes; rsync --delete removes it later.
func copyInsteadOfMove(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if _, err := reflinkOrCopy(src, dst, info.Mode()); err != nil {
		return fmt.Errorf("rename across devices, copy failed too: %w", err)
	}
	if err := os.Chtimes(dst, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	if err := keepOwner(info, dst); err != nil {
		return err
	}
	movesAsCopies.Add(1)
	return nil
}

// Uniqueness generates unique string for file renaming/movement
func (a MoveFileAction) Uniqueness() string {
	return "mv" + cmdSeparator + a.RelativeFromPath
}

func (a MoveFileAction) String() string {
	return fmt.Sprintf(`rename/move file from "%s" to "%s"`, sanitizePath(a.sourcePath()), sanitizePath(a.destinationPath()))
}
