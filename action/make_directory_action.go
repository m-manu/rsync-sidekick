package action

import (
	"fmt"
	"os"
)

type MakeDirectoryAction struct {
	AbsoluteDirPath string
}

func (a MakeDirectoryAction) sourcePath() string {
	return "" // Not Applicable
}

func (a MakeDirectoryAction) destinationPath() string {
	return a.AbsoluteDirPath
}

func (a MakeDirectoryAction) UnixCommand() string {
	return fmt.Sprintf(`mkdir -p -v "%s"`, escape(a.destinationPath()))
}

func (a MakeDirectoryAction) Perform() error {
	return os.MkdirAll(a.destinationPath(), os.ModeDir|os.ModePerm)
}

func (a MakeDirectoryAction) Uniqueness() string {
	return "Mkdir\u0001" + a.AbsoluteDirPath
}

func (a MakeDirectoryAction) String() string {
	return fmt.Sprintf(`create directory "%s"`, a.destinationPath())
}
