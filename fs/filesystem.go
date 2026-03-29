package fs

import (
	"io/fs"
	"time"
)

// FileSystem abstracts file system operations so that callers can work with
// local directories, SFTP mounts, or remote agents interchangeably.
type FileSystem interface {
	// Walk recursively walks dirPath, returning all regular files.
	// excludedNames contains base names to skip (files and directories).
	// counter, if non-nil, is incremented atomically for each regular file found.
	Walk(dirPath string, excludedNames map[string]struct{}, counter *int32) ([]DirEntry, error)

	// Lstat returns file info without following symlinks.
	Lstat(path string) (FileInfo, error)

	// Stat returns file info, following symlinks.
	Stat(path string) (FileInfo, error)

	// ReadFile reads the entire file.
	ReadFile(path string) ([]byte, error)

	// ReadAt reads len(buf) bytes starting at offset.
	ReadAt(path string, buf []byte, offset int64) (int, error)

	// Rename moves/renames a file.
	Rename(oldPath, newPath string) error

	// Chtimes changes the access and modification times of the named file.
	Chtimes(path string, atime, mtime time.Time) error

	// MkdirAll creates a directory path and all parents that do not yet exist.
	MkdirAll(path string) error

	// IsReadableDirectory returns true if path is an existing, readable directory.
	IsReadableDirectory(path string) bool

	// Close releases any resources held by the filesystem (e.g. SSH connections).
	Close() error
}

// FileInfo holds the subset of os.FileInfo fields we need.
type FileInfo struct {
	Name    string
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
	IsDir   bool
}

// DirEntry represents a single file or directory discovered during Walk.
type DirEntry struct {
	// RelativePath is the path relative to the walk root.
	RelativePath string
	// Size is the file size in bytes (0 for directories).
	Size int64
	// ModTime is the modification time as a Unix timestamp.
	ModTime int64
	// IsDir is true for directory entries.
	IsDir bool
}
