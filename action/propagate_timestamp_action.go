package action

import (
	"fmt"
	"os"
	"path/filepath"
)

// PropagateTimestampAction is a SyncAction for propagating 'file modification timestamp' from one file to another
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

// UnixCommand for propagating 'file modification timestamp'
func (a PropagateTimestampAction) UnixCommand() string {
	return fmt.Sprintf(`touch -r "%s" "%s"`, escape(a.sourcePath()), escape(a.destinationPath()))
}

// Perform the 'file modification timestamp' propagation action
func (a PropagateTimestampAction) Perform() error {
	fileInfo, err := os.Lstat(a.sourcePath())
	if err != nil {
		return err
	}
	modTime := fileInfo.ModTime()
	return os.Chtimes(a.destinationPath(), modTime, modTime)
}

// Uniqueness generate unique string for 'file modification timestamp' propagation action
func (a PropagateTimestampAction) Uniqueness() string {
	return "touch" + cmdSeparator + a.DestinationFileRelativePath
}

func (a PropagateTimestampAction) String() string {
	return fmt.Sprintf(`propagate timestamp of "%s" to "%s"`, a.sourcePath(), a.destinationPath())
}
