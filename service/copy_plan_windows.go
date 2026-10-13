//go:build windows

package service

import (
	"os"
	"path/filepath"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/action"
)

// reflinkTarget creates targetPath as a copy of originalPath with the target's own mtime.
func reflinkTarget(originalPath string, _ os.FileInfo, targetPath string, modTime int64) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	return action.CopyFileAction{
		AbsSourcePath: originalPath,
		AbsDestPath:   targetPath,
		SourceModTime: time.Unix(modTime, 0),
		UseReflink:    true,
	}.Perform()
}
