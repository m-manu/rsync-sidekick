package action

import (
	"fmt"
	"os"
	"path/filepath"
)

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

func (a MoveFileAction) UnixCommand() string {
	return fmt.Sprintf(`mv -v "%s" "%s"`, escape(a.sourcePath()), escape(a.destinationPath()))
}

func (a MoveFileAction) Perform() error {
	return os.Rename(a.sourcePath(), a.destinationPath())
}

func (a MoveFileAction) Uniqueness() string {
	return "mv\u0001" + a.RelativeFromPath
}

func (a MoveFileAction) String() string {
	return fmt.Sprintf(`rename/move file from "%s" to "%s"`, a.sourcePath(), a.destinationPath())
}
