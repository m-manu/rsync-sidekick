package fs

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/fmte"
)

// LocalFS implements FileSystem using standard os.* calls.
type LocalFS struct {
	OneFileSystem bool // if true, skip directories on different filesystems
	// deviceForPath returns the device ID for a path. If nil, syscall.Stat is used.
	// Exposed for testing without real filesystem boundaries.
	deviceForPath func(string) (uint64, bool)
}

// NewLocalFS returns a new LocalFS.
func NewLocalFS() *LocalFS {
	return &LocalFS{OneFileSystem: DefaultOneFileSystem}
}

// DefaultOneFileSystem is the default for OneFileSystem on new LocalFS instances (src+dst scans).
var DefaultOneFileSystem bool

// DefaultArchiveOneFileSystem is the default for OneFileSystem on archive scans.
var DefaultArchiveOneFileSystem bool

// DefaultMinSize is the --min-size threshold in bytes: regular files smaller than this are
// left out of every walk, so they never become orphans, candidates or archive candidates
// and never get hashed. Zero disables it. Directories are never filtered.
//
// Filtering inside the walks rather than afterwards keeps the progress counters honest —
// they report what will actually be worked on.
var DefaultMinSize int64

// DefaultWalkThreads is how many directories a local walk reads at once (--walk-threads).
var DefaultWalkThreads = 4

// SkipBySize reports whether a regular file of this size is below DefaultMinSize.
func SkipBySize(isDir bool, size int64) bool {
	return !isDir && DefaultMinSize > 0 && size < DefaultMinSize
}

// NewLocalFSForArchive returns a LocalFS configured for archive scanning.
func NewLocalFSForArchive() *LocalFS {
	return &LocalFS{OneFileSystem: DefaultArchiveOneFileSystem}
}

func (l *LocalFS) Walk(dirPath string, excludedNames map[string]struct{}, counter *int32) ([]DirEntry, error) {
	return l.walk(dirPath, excludedNames, counter, nil)
}

// WalkEach is Walk handing the entries to emit as they are read, a directory at a time
// and from several goroutines at once, instead of returning them all at the end.
func (l *LocalFS) WalkEach(dirPath string, excludedNames map[string]struct{}, counter *int32,
	emit func([]DirEntry),
) error {
	_, err := l.walk(dirPath, excludedNames, counter, emit)
	return err
}

func (l *LocalFS) walk(dirPath string, excludedNames map[string]struct{}, counter *int32,
	emit func([]DirEntry),
) ([]DirEntry, error) {
	// Use BTRFS-optimized walk if available (batch ioctl instead of per-file stat)
	if !l.OneFileSystem && IsBtrfs(dirPath) {
		entries, err := btrfsWalk(dirPath, excludedNames, counter, emit)
		if err == nil {
			return entries, nil
		}
		// Fall back to standard walk on error
	}

	// Get root device ID for --one-file-system check
	var rootDevice uint64
	if l.OneFileSystem {
		if device, ok := l.getDevice(dirPath); ok {
			rootDevice = device
		}
	}
	var skipDir func(path string) bool
	if l.OneFileSystem {
		// --one-file-system: skip directories on different filesystems
		skipDir = func(path string) bool {
			device, ok := l.getDevice(path)
			return ok && device != rootDevice
		}
	}
	return walkParallel(standardDir{absPath: dirPath}, DefaultWalkThreads,
		func(dir standardDir) ([]DirEntry, []standardDir) {
			return readStandardDir(dir, excludedNames, counter, skipDir)
		}, emit), nil
}

// standardDir is a directory for readStandardDir.
type standardDir struct{ absPath, relativePath string }

// readStandardDir reads one directory with ReadDir and one lstat per entry: its entries,
// and the subdirectories still to walk. skipDir, when set, leaves directories out.
func readStandardDir(dir standardDir, excludedNames map[string]struct{}, counter *int32,
	skipDir func(path string) bool,
) (entries []DirEntry, subdirs []standardDir) {
	children, err := os.ReadDir(dir.absPath)
	if err != nil {
		// Entries read before the error are still walked, as filepath.WalkDir does.
		fmte.PrintfErr("skipping \"%s\": %+v\n", dir.absPath, err)
	}
	for _, d := range children {
		if _, excluded := excludedNames[d.Name()]; excluded {
			continue
		}
		// Ignore dot files (Mac)
		if strings.HasPrefix(d.Name(), "._") {
			continue
		}
		if !d.Type().IsRegular() && !d.IsDir() {
			continue
		}
		path := filepath.Join(dir.absPath, d.Name())
		info, infoErr := d.Info()
		if infoErr != nil {
			fmte.PrintfErr("couldn't get metadata of \"%s\": %+v\n", path, infoErr)
			continue
		}
		if d.IsDir() && skipDir != nil && skipDir(path) {
			continue
		}
		relativePath := filepath.Join(dir.relativePath, d.Name())
		if d.IsDir() {
			subdirs = append(subdirs, standardDir{absPath: path, relativePath: relativePath})
		}
		if SkipBySize(d.IsDir(), info.Size()) {
			continue
		}
		entries = append(entries, DirEntry{
			RelativePath: relativePath,
			Size:         info.Size(),
			ModTime:      info.ModTime().Unix(),
			IsDir:        d.IsDir(),
		})
		if counter != nil && d.Type().IsRegular() {
			atomic.AddInt32(counter, 1)
		}
	}
	return entries, subdirs
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
