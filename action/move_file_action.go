package action

import (
	"fmt"
	"os"
	"path/filepath"
)

// MoveFileAction is a SyncAction for moving or renaming a file
type MoveFileAction struct {
	BasePath         string
	RelativeFromPath string
	RelativeToPath   string
}

func (a MoveFileAction) sourcePath() string {
	return filepath.Join(a.BasePath, a.RelativeFromPath)
}

func (a MoveFileAction) destinationPath() string {
	return filepath.Join(a.BasePath, a.RelativeToPath)
}

// UnixCommand for moving or renaming a file
func (a MoveFileAction) UnixCommand() string {
	return fmt.Sprintf(`mv -v "%s" "%s"`, escape(a.sourcePath()), escape(a.destinationPath()))
}

// Perform 'file move/rename' action
func (a MoveFileAction) Perform() error {
	return os.Rename(a.sourcePath(), a.destinationPath())
}

// Uniqueness generates unique string for file renaming/movement
func (a MoveFileAction) Uniqueness() string {
	return "mv\u0001" + a.RelativeFromPath
}

func (a MoveFileAction) String() string {
	return fmt.Sprintf(`rename/move file from "%s" to "%s"`, a.sourcePath(), a.destinationPath())
}
