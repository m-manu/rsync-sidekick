package fs

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/m-manu/rsync-sidekick/fmte"
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

func (s *SFTPFS) Walk(dirPath string, excludedNames map[string]struct{}) ([]DirEntry, error) {
	entries := make([]DirEntry, 0, 10_000)
	walker := s.client.Walk(dirPath)
	for walker.Step() {
		if walker.Err() != nil {
			fmte.PrintfErr("skipping \"%s\": %+v\n", walker.Path(), walker.Err())
			continue
		}
		info := walker.Stat()
		baseName := path.Base(walker.Path())

		if _, excluded := excludedNames[baseName]; excluded {
			if info.IsDir() {
				walker.SkipDir()
			}
			continue
		}

		// Ignore dot files (Mac)
		if strings.HasPrefix(baseName, "._") {
			continue
		}

		if info.Mode().IsRegular() {
			relPath, err := relPath(dirPath, walker.Path())
			if err != nil {
				fmte.PrintfErr("couldn't comprehend path \"%s\": %+v\n", walker.Path(), err)
				continue
			}
			entries = append(entries, DirEntry{
				RelativePath: relPath,
				Size:         info.Size(),
				ModTime:      info.ModTime().Unix(),
			})
		}
	}
	return entries, nil
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

// relPath computes a relative path from base to target using POSIX paths.
func relPath(base, target string) (string, error) {
	// Clean both
	base = path.Clean(base)
	target = path.Clean(target)
	if !strings.HasPrefix(target, base) {
		return "", fmt.Errorf("%q is not under %q", target, base)
	}
	rel := strings.TrimPrefix(target, base)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return ".", nil
	}
	return rel, nil
}
