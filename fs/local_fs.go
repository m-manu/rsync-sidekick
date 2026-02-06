package fs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/m-manu/rsync-sidekick/fmte"
)

// LocalFS implements FileSystem using standard os.* calls.
type LocalFS struct{}

// NewLocalFS returns a new LocalFS.
func NewLocalFS() *LocalFS {
	return &LocalFS{}
}

func (l *LocalFS) Walk(dirPath string, excludedNames map[string]struct{}) ([]DirEntry, error) {
	entries := make([]DirEntry, 0, 10_000)
	err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			fmte.PrintfErr("skipping \"%s\": %+v\n", path, err)
			return nil
		}
		if _, excluded := excludedNames[d.Name()]; excluded {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Ignore dot files (Mac)
		if strings.HasPrefix(d.Name(), "._") {
			return nil
		}
		if d.Type().IsRegular() {
			info, infoErr := d.Info()
			if infoErr != nil {
				fmte.PrintfErr("couldn't get metadata of \"%s\": %+v\n", path, infoErr)
				return nil
			}
			relativePath, relErr := filepath.Rel(dirPath, path)
			if relErr != nil {
				fmte.PrintfErr("couldn't comprehend path \"%s\": %+v\n", path, relErr)
				return nil
			}
			entries = append(entries, DirEntry{
				RelativePath: relativePath,
				Size:         info.Size(),
				ModTime:      info.ModTime().Unix(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("couldn't scan directory %s: %v", dirPath, err)
	}
	return entries, nil
}

func (l *LocalFS) Lstat(path string) (FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return fileInfoFromOS(info), nil
}

func (l *LocalFS) Stat(path string) (FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return fileInfoFromOS(info), nil
}

func (l *LocalFS) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (l *LocalFS) ReadAt(path string, buf []byte, offset int64) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	return file.ReadAt(buf, offset)
}

func (l *LocalFS) Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func (l *LocalFS) Chtimes(path string, atime, mtime time.Time) error {
	return os.Chtimes(path, atime, mtime)
}

func (l *LocalFS) MkdirAll(path string) error {
	return os.MkdirAll(path, os.ModeDir|os.ModePerm)
}

func (l *LocalFS) IsReadableDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func (l *LocalFS) Close() error {
	return nil
}

func fileInfoFromOS(info os.FileInfo) FileInfo {
	return FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}
}
