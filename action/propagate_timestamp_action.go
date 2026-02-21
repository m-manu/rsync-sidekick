package action

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	rsfs "github.com/m-manu/rsync-sidekick/fs"
)

// PropagateTimestampAction is a SyncAction for propagating 'file modification timestamp' from one file to another
type PropagateTimestampAction struct {
	SourceBaseDirPath           string
	DestinationBaseDirPath      string
	SourceFileRelativePath      string
	DestinationFileRelativePath string
	SourceModTime               time.Time       // if set, use this instead of Lstat on source
	FS                          rsfs.FileSystem // optional; if nil, uses os.* directly
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
	// If SourceModTime is pre-populated (remote mode), use it directly
	if !a.SourceModTime.IsZero() {
		if a.FS != nil {
			return a.FS.Chtimes(a.destinationPath(), a.SourceModTime, a.SourceModTime)
		}
		return os.Chtimes(a.destinationPath(), a.SourceModTime, a.SourceModTime)
	}
	if a.FS != nil {
		srcInfo, err := a.FS.Lstat(a.sourcePath())
		if err != nil {
			return err
		}
		return a.FS.Chtimes(a.destinationPath(), srcInfo.ModTime, srcInfo.ModTime)
	}
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
