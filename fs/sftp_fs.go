package fs

import (
	"io/fs"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/fmte"
	"github.com/pkg/sftp"
)

// SFTPFS implements FileSystem over an SFTP connection.
type SFTPFS struct {
	client *sftp.Client
}

// NewSFTPFS wraps an existing sftp.Client in a FileSystem.
func NewSFTPFS(client *sftp.Client) *SFTPFS {
	return &SFTPFS{client: client}
}

// Walk reads DefaultWalkThreads directories at once (--walk-threads). Over SFTP every
// directory costs several round trips (open, read until EOF, close), so reading one
// directory after the other makes a walk over a high-latency link take as many round
// trips as there are directories; the client multiplexes concurrent requests over the
// one connection.
func (s *SFTPFS) Walk(dirPath string, excludedNames map[string]struct{}, counter *int32) ([]DirEntry, error) {
	return walkParallel(sftpDir{absPath: path.Clean(dirPath)}, DefaultWalkThreads,
		func(dir sftpDir) ([]DirEntry, []sftpDir) {
			return s.readDir(dir, excludedNames, counter)
		}, nil), nil
}

// sftpDir is a directory for SFTPFS.readDir.
type sftpDir struct{ absPath, relativePath string }

// readDir reads one remote directory: its entries, and the subdirectories still to walk.
// READDIR returns lstat attributes, so no request per entry is needed.
func (s *SFTPFS) readDir(dir sftpDir, excludedNames map[string]struct{}, counter *int32,
) (entries []DirEntry, subdirs []sftpDir) {
	children, err := s.client.ReadDir(dir.absPath)
	if err != nil {
		fmte.PrintfErr("skipping \"%s\": %+v\n", dir.absPath, err)
	}
	for _, info := range children {
		name := info.Name()
		if _, excluded := excludedNames[name]; excluded {
			continue
		}
		// Ignore dot files (Mac)
		if strings.HasPrefix(name, "._") {
			continue
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			continue
		}
		relativePath := path.Join(dir.relativePath, name)
		if info.IsDir() {
			subdirs = append(subdirs, sftpDir{absPath: path.Join(dir.absPath, name), relativePath: relativePath})
		}
		if SkipBySize(info.IsDir(), info.Size()) {
			continue
		}
		entries = append(entries, DirEntry{
			RelativePath: relativePath,
			Size:         info.Size(),
			ModTime:      info.ModTime().Unix(),
			IsDir:        info.IsDir(),
		})
		if counter != nil && info.Mode().IsRegular() {
			atomic.AddInt32(counter, 1)
		}
	}
	return entries, subdirs
}

func (s *SFTPFS) Lstat(p string) (FileInfo, error) {
	info, err := s.client.Lstat(p)
	if err != nil {
		return FileInfo{}, err
	}
	return sftpFileInfo(info), nil
}

func (s *SFTPFS) Stat(p string) (FileInfo, error) {
	info, err := s.client.Stat(p)
	if err != nil {
		return FileInfo{}, err
	}
	return sftpFileInfo(info), nil
}

func (s *SFTPFS) ReadFile(p string) ([]byte, error) {
	f, err := s.client.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	buf := make([]byte, info.Size())
	n, err := f.Read(buf)
	if err != nil && err.Error() != "EOF" {
		return nil, err
	}
	return buf[:n], nil
}

func (s *SFTPFS) ReadAt(p string, buf []byte, offset int64) (int, error) {
	f, err := s.client.Open(p)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.ReadAt(buf, offset)
}

func (s *SFTPFS) Rename(oldPath, newPath string) error {
	// sftp.Client.Rename can fail if dest exists on some servers, but that matches our needs
	return s.client.Rename(oldPath, newPath)
}

func (s *SFTPFS) Chtimes(p string, atime, mtime time.Time) error {
	return s.client.Chtimes(p, atime, mtime)
}

func (s *SFTPFS) MkdirAll(p string) error {
	// SFTP only has Mkdir, so we iterate path components
	return s.mkdirAll(p)
}

func (s *SFTPFS) mkdirAll(p string) error {
	// Check if it already exists
	if info, err := s.client.Stat(p); err == nil && info.IsDir() {
		return nil
	}
	// Ensure parent exists
	parent := path.Dir(p)
	if parent != p && parent != "/" && parent != "." {
		if err := s.mkdirAll(parent); err != nil {
			return err
		}
	}
	err := s.client.Mkdir(p)
	if err != nil {
		// May already exist due to race; check again
		if info, statErr := s.client.Stat(p); statErr == nil && info.IsDir() {
			return nil
		}
		return err
	}
	return nil
}

func (s *SFTPFS) IsReadableDirectory(p string) bool {
	info, err := s.client.Lstat(p)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func (s *SFTPFS) Close() error {
	return s.client.Close()
}

func sftpFileInfo(info fs.FileInfo) FileInfo {
	return FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}
}
