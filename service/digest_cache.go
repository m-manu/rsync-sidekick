package service

import (
	"bufio"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/fmte"
	"github.com/m-manu/rsync-sidekick/v2/lib"
)

const (
	digestCacheHeader       = "# rsync-sidekick digest cache v1 crc32-sampled-16k"
	digestCacheFlushBytes   = 64 * 1024
	digestCacheFlushEvery   = 5 * time.Second
	digestCacheCompactSlack = 10000
	digestCacheRacyWindow   = 2 * time.Second
)

type fileKey struct {
	dev   uint64
	ino   uint64
	size  int64
	mtime int64
	ctime int64
}

type digestCacheEntry struct {
	key  fileKey
	hash string
}

// DigestCache remembers digests by absolute path. Backed by a file it carries them over
// to later runs; without one (NewMemoryDigestCache) it only spares hashing a file twice
// within the same run, e.g. when the destination lies inside an archive path.
type DigestCache struct {
	mu        sync.Mutex
	path      string
	file      *os.File
	readOnly  bool
	roots     []string
	entries   map[string]digestCacheEntry
	buf       []byte
	lastFlush time.Time
	hits      atomic.Int64
	misses    atomic.Int64
	writeErr  error
	// racyWindow skips files changed this recently: timestamps are coarse, so a write
	// right after hashing could leave ctime unchanged and the stale digest would stick.
	racyWindow time.Duration
}

var activeDigestCache atomic.Pointer[DigestCache]

func SetDigestCache(c *DigestCache) {
	activeDigestCache.Store(c)
}

func ActiveDigestCache() *DigestCache {
	return activeDigestCache.Load()
}

func DefaultDigestCachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rsync-sidekick", "digests.tsv"), nil
}

func NewMemoryDigestCache() *DigestCache {
	return &DigestCache{entries: make(map[string]digestCacheEntry), racyWindow: digestCacheRacyWindow}
}

// OpenDigestCache opens or creates the cache file at path. Only entries below roots are
// loaded into memory and saved (all of them when roots is empty); the rest of the file
// is kept as it is.
func OpenDigestCache(path string, roots []string) (*DigestCache, error) {
	c := &DigestCache{path: path, entries: make(map[string]digestCacheEntry), lastFlush: time.Now(),
		racyWindow: digestCacheRacyWindow, roots: cleanRoots(roots)}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil && !fileExists(path) {
		return nil, fmt.Errorf("couldn't create directory for digest cache %s: %w", path, mkErr)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		if !isNotWritable(err) || !fileExists(path) {
			return nil, fmt.Errorf("couldn't open digest cache %s: %w", path, err)
		}
		f, err = os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("couldn't open digest cache %s: %w", path, err)
		}
		c.readOnly = true
		fmte.PrintfErr("warning: digest cache %s is not writable - using it read-only, new digests are not saved\n", path)
	} else if !tryLockFile(f) {
		c.readOnly = true
		fmte.PrintfErr("warning: digest cache %s is in use by another rsync-sidekick process - using it read-only, new digests are not saved\n", path)
	}
	c.file = f
	pathHashes, loadErr := c.load()
	if loadErr != nil {
		_ = f.Close()
		return nil, loadErr
	}
	if !c.readOnly {
		if keep, stale := lastOccurrences(pathHashes); stale > max(digestCacheCompactSlack, len(pathHashes)-stale) {
			if compactErr := c.compact(keep); compactErr != nil {
				fmte.PrintfErr("warning: couldn't compact digest cache %s: %+v\n", path, compactErr)
			}
		}
	}
	return c, nil
}

func cleanRoots(roots []string) []string {
	cleaned := make([]string, 0, len(roots))
	for _, r := range roots {
		if abs, err := filepath.Abs(r); err == nil {
			cleaned = append(cleaned, abs)
		}
	}
	return cleaned
}

func (c *DigestCache) relevant(absPath string) bool {
	if len(c.roots) == 0 {
		return true
	}
	for _, r := range c.roots {
		if absPath == r || strings.HasPrefix(absPath, strings.TrimSuffix(r, string(filepath.Separator))+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isNotWritable(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS)
}

func pathFieldHash(quotedPath string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(quotedPath))
	return h.Sum64()
}

// load reads the whole file but keeps only entries below the roots. It returns the hash
// of every data line's path, in file order, which is all compaction needs to know.
func (c *DigestCache) load() ([]uint64, error) {
	if _, err := c.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("couldn't read digest cache %s: %w", c.path, err)
	}
	scanner := bufio.NewScanner(c.file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var pathHashes []uint64
	skipped := 0
	headerSeen := false
	for scanner.Scan() {
		line := scanner.Text()
		if !headerSeen {
			headerSeen = true
			if line != digestCacheHeader {
				return nil, c.discard(fmt.Sprintf("unknown format %q", line))
			}
			continue
		}
		quotedPath, _, found := strings.Cut(line, "\t")
		if !found {
			skipped++
			pathHashes = append(pathHashes, 0)
			continue
		}
		absPath, err := strconv.Unquote(quotedPath)
		if err != nil {
			skipped++
			pathHashes = append(pathHashes, 0)
			continue
		}
		if !c.relevant(absPath) {
			pathHashes = append(pathHashes, pathFieldHash(quotedPath))
			continue
		}
		entry, ok := parseDigestCacheEntry(line)
		if !ok {
			skipped++
			pathHashes = append(pathHashes, 0)
			continue
		}
		pathHashes = append(pathHashes, pathFieldHash(quotedPath))
		c.entries[absPath] = entry
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("couldn't read digest cache %s: %w", c.path, err)
	}
	if !headerSeen && !c.readOnly {
		c.buf = append(c.buf, digestCacheHeader+"\n"...)
		c.flushLocked()
	}
	if skipped > 0 {
		fmte.PrintfErr("warning: digest cache %s: skipped %s malformed lines\n", c.path, lib.GroupThousands(skipped))
	}
	return pathHashes, nil
}

// lastOccurrences marks, per line, whether it is the last line for its path, and counts
// the lines that a later one supersedes.
func lastOccurrences(pathHashes []uint64) ([]bool, int) {
	order := make([]int, len(pathHashes))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		if pathHashes[a] != pathHashes[b] {
			if pathHashes[a] < pathHashes[b] {
				return -1
			}
			return 1
		}
		return a - b
	})
	keep := make([]bool, len(pathHashes))
	stale := 0
	for i, line := range order {
		if pathHashes[line] == 0 || i+1 < len(order) && pathHashes[order[i+1]] == pathHashes[line] {
			stale++
			continue
		}
		keep[line] = true
	}
	return keep, stale
}

func (c *DigestCache) discard(reason string) error {
	c.entries = make(map[string]digestCacheEntry)
	if c.readOnly {
		fmte.PrintfErr("warning: digest cache %s has %s - ignoring it\n", c.path, reason)
		return nil
	}
	fmte.PrintfErr("warning: digest cache %s has %s - starting a fresh one\n", c.path, reason)
	if err := c.file.Truncate(0); err != nil {
		return fmt.Errorf("couldn't reset digest cache %s: %w", c.path, err)
	}
	c.buf = append(c.buf[:0], digestCacheHeader+"\n"...)
	c.flushLocked()
	return c.writeErr
}

func parseDigestCacheEntry(line string) (digestCacheEntry, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) != 7 {
		return digestCacheEntry{}, false
	}
	var e digestCacheEntry
	var errs [5]error
	e.key.dev, errs[0] = strconv.ParseUint(fields[1], 10, 64)
	e.key.ino, errs[1] = strconv.ParseUint(fields[2], 10, 64)
	e.key.size, errs[2] = strconv.ParseInt(fields[3], 10, 64)
	e.key.mtime, errs[3] = strconv.ParseInt(fields[4], 10, 64)
	e.key.ctime, errs[4] = strconv.ParseInt(fields[5], 10, 64)
	for _, err := range errs {
		if err != nil {
			return digestCacheEntry{}, false
		}
	}
	e.hash = fields[6]
	if e.hash == "" {
		return digestCacheEntry{}, false
	}
	return e, true
}

func formatDigestCacheLine(absPath string, e digestCacheEntry) string {
	return fmt.Sprintf("%s\t%d\t%d\t%d\t%d\t%d\t%s\n",
		strconv.Quote(absPath), e.key.dev, e.key.ino, e.key.size, e.key.mtime, e.key.ctime, e.hash)
}

// compact rewrites the file with only the lines marked in keep, streaming it rather than
// relying on the entries in memory, which may cover just part of the file.
func (c *DigestCache) compact(keep []bool) error {
	if _, err := c.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tmpPath := c.path + ".tmp"
	tmp, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	scanner := bufio.NewScanner(c.file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	w := bufio.NewWriter(tmp)
	_, _ = w.WriteString(digestCacheHeader + "\n")
	line := -1
	for scanner.Scan() {
		if line >= 0 && line < len(keep) && keep[line] {
			_, _ = w.Write(scanner.Bytes())
			_ = w.WriteByte('\n')
		}
		line++
	}
	if err := scanner.Err(); err != nil {
		return fail(err)
	}
	if err := w.Flush(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, c.path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	f, err := os.OpenFile(c.path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_ = c.file.Close()
	c.file = f
	if !tryLockFile(f) {
		c.readOnly = true
	}
	return nil
}

func (c *DigestCache) Lookup(absPath string, info os.FileInfo) (string, bool) {
	key, ok := statKey(info)
	if !ok {
		return "", false
	}
	c.mu.Lock()
	e, found := c.entries[absPath]
	c.mu.Unlock()
	if !found || e.key != key {
		c.misses.Add(1)
		return "", false
	}
	c.hits.Add(1)
	return e.hash, true
}

func (c *DigestCache) Store(absPath string, info os.FileInfo, hash string) {
	key, ok := statKey(info)
	if !ok {
		return
	}
	newest := max(key.mtime, key.ctime)
	if time.Now().UnixNano()-newest < c.racyWindow.Nanoseconds() {
		return
	}
	e := digestCacheEntry{key: key, hash: hash}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[absPath] = e
	// Outside the roots a digest still serves the rest of this run, but isn't kept.
	if c.file == nil || c.readOnly || c.writeErr != nil || !c.relevant(absPath) {
		return
	}
	c.buf = append(c.buf, formatDigestCacheLine(absPath, e)...)
	if len(c.buf) >= digestCacheFlushBytes || time.Since(c.lastFlush) >= digestCacheFlushEvery {
		c.flushLocked()
	}
}

func (c *DigestCache) flushLocked() {
	c.lastFlush = time.Now()
	if len(c.buf) == 0 || c.writeErr != nil {
		return
	}
	if _, err := c.file.Write(c.buf); err != nil {
		c.writeErr = err
		fmte.PrintfErr("warning: couldn't write digest cache %s, no more digests are saved: %+v\n", c.path, err)
	}
	c.buf = c.buf[:0]
}

func (c *DigestCache) ReadOnly() bool {
	return c.readOnly
}

func (c *DigestCache) Path() string {
	return c.path
}

func (c *DigestCache) Persistent() bool {
	return c.path != ""
}

func (c *DigestCache) Stats() (hits, misses int64) {
	return c.hits.Load(), c.misses.Load()
}

func (c *DigestCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file == nil {
		return nil
	}
	if !c.readOnly {
		c.flushLocked()
	}
	err := c.file.Close()
	c.file = nil
	if c.writeErr != nil {
		return c.writeErr
	}
	return err
}

func cachedFileHash(path string, info os.FileInfo) (string, error) {
	compute := func() (string, error) { return fileHash(path, info.Size()) }
	c := activeDigestCache.Load()
	if c == nil {
		return hashOnce(info, compute)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return hashOnce(info, compute)
	}
	if hash, ok := c.Lookup(absPath, info); ok {
		return hash, nil
	}
	hash, err := hashOnce(info, compute)
	if err != nil {
		return "", err
	}
	c.Store(absPath, info, hash)
	return hash, nil
}
