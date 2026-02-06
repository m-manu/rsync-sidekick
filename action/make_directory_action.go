package action

import (
	"fmt"
	"os"

	rsfs "github.com/m-manu/rsync-sidekick/fs"
)

// MakeDirectoryAction is a SyncAction for creating a directory
type MakeDirectoryAction struct {
	AbsoluteDirPath string
	FS              rsfs.FileSystem // optional; if nil, uses os.* directly
}

func (a MakeDirectoryAction) sourcePath() string {
	return "" // Not Applicable
}

func (a MakeDirectoryAction) destinationPath() string {
	return a.AbsoluteDirPath
}

// UnixCommand for creating a directory
func (a MakeDirectoryAction) UnixCommand() string {
	return fmt.Sprintf(`mkdir -p -v "%s"`, escape(a.destinationPath()))
}

// Perform the 'create directory' action
func (a MakeDirectoryAction) Perform() error {
	if a.FS != nil {
		return a.FS.MkdirAll(a.destinationPath())
	}
	return os.MkdirAll(a.destinationPath(), os.ModeDir|os.ModePerm)
}

// Uniqueness generates unique string for directory creation
func (a MakeDirectoryAction) Uniqueness() string {
	return "Mkdir" + cmdSeparator + a.AbsoluteDirPath
}

func (a MakeDirectoryAction) String() string {
	return fmt.Sprintf(`create directory "%s"`, a.destinationPath())
}
