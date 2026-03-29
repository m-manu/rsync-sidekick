package action

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	rsfs "github.com/m-manu/rsync-sidekick/fs"
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
	} else if errors.Is(err, os.ErrNotExist) {
		return os.Rename(a.sourcePath(), a.destinationPath())
	} else {
		return err
	}
}

// Uniqueness generates unique string for file renaming/movement
func (a MoveFileAction) Uniqueness() string {
	return "mv" + cmdSeparator + a.RelativeFromPath
}

func (a MoveFileAction) String() string {
	return fmt.Sprintf(`rename/move file from "%s" to "%s"`, a.sourcePath(), a.destinationPath())
}
