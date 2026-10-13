//go:build !windows

package service

import (
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/action"
)

// reflinkTarget creates targetPath as a reflink of originalPath with the target's own
// mtime. Mode comes along with the reflink; owner and group are taken from the original,
// since the plan doesn't record them per file.
func reflinkTarget(originalPath string, originalInfo os.FileInfo, targetPath string, modTime int64) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	copyAction := action.CopyFileAction{
		AbsSourcePath: originalPath,
		AbsDestPath:   targetPath,
		SourceModTime: time.Unix(modTime, 0),
		UseReflink:    true,
	}
	if err := copyAction.Perform(); err != nil {
		return err
	}
	if st, ok := originalInfo.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		return os.Lchown(targetPath, int(st.Uid), int(st.Gid))
	}
	return nil
}
