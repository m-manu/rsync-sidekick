package action

import (
	"fmt"
	"os"
	"path/filepath"
)

type PropagateTimestampAction struct {
	SourceBaseDirPath           string
	DestinationBaseDirPath      string
	SourceFileRelativePath      string
	DestinationFileRelativePath string
}

func (a PropagateTimestampAction) sourcePath() string {
	return filepath.Join(a.SourceBaseDirPath, a.SourceFileRelativePath)
}

func (a PropagateTimestampAction) destinationPath() string {
	return filepath.Join(a.DestinationBaseDirPath, a.DestinationFileRelativePath)
}

func (a PropagateTimestampAction) UnixCommand() string {
	return fmt.Sprintf(`touch -r "%s" "%s"`, escape(a.sourcePath()), escape(a.destinationPath()))
}

func (a PropagateTimestampAction) Perform() error {
	fileInfo, err := os.Lstat(a.sourcePath())
	if err != nil {
		return err
	}
	modTime := fileInfo.ModTime()
	return os.Chtimes(a.destinationPath(), modTime, modTime)
}

func (a PropagateTimestampAction) Uniqueness() string {
	return "touch\u0001" + a.DestinationFileRelativePath
}

func (a PropagateTimestampAction) String() string {
	return fmt.Sprintf(`propagate timestamp of "%s" to "%s"`, a.sourcePath(), a.destinationPath())
}
