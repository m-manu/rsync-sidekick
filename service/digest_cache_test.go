//go:build linux || darwin || freebsd || netbsd

package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestCache(t *testing.T, path string) *DigestCache {
	t.Helper()
	c, err := OpenDigestCache(path)
	require.NoError(t, err)
	c.racyWindow = 0
	return c
}

func writeTestFile(t *testing.T, path string, content []byte, mtime time.Time) os.FileInfo {
	t.Helper()
	require.NoError(t, os.WriteFile(path, content, 0o644))
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	info, err := os.Lstat(path)
	require.NoError(t, err)
	return info
}

func cacheLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestDigestCache_reusedAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache", "digests.tsv")
	file := filepath.Join(dir, "a.bin")
	info := writeTestFile(t, file, bytes.Repeat([]byte("x"), 100_000), time.Unix(1_700_000_000, 0))

	c := openTestCache(t, cachePath)
	_, found := c.Lookup(file, info)
	assert.False(t, found, "empty cache must not hit")
	c.Store(file, info, "s12345678")
	require.NoError(t, c.Close())

	c = openTestCache(t, cachePath)
	hash, found := c.Lookup(file, info)
	assert.True(t, found, "entry must survive a reopen; cache file:\n%v", cacheLines(t, cachePath))
	assert.Equal(t, "s12345678", hash)
	hits, misses := c.Stats()
	assert.Equal(t, [2]int64{1, 0}, [2]int64{hits, misses})
	require.NoError(t, c.Close())
}

func TestDigestCache_rewriteWithSameSizeAndMtimeIsNotReused(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "digests.tsv")
	file := filepath.Join(dir, "repaired.bin")
	mtime := time.Unix(1_700_000_000, 0)
	broken := writeTestFile(t, file, bytes.Repeat([]byte{0}, 100_000), mtime)

	c := openTestCache(t, cachePath)
	SetDigestCache(c)
	defer SetDigestCache(nil)
	brokenDigest, err := getDigest(file)
	require.NoError(t, err)
	_, found := c.Lookup(file, broken)
	require.True(t, found, "digest of the first content must be cached")

	time.Sleep(20 * time.Millisecond)
	repaired := writeTestFile(t, file, bytes.Repeat([]byte{7}, 100_000), mtime)
	require.Equal(t, broken.Size(), repaired.Size())
	require.Equal(t, broken.ModTime(), repaired.ModTime())

	repairedDigest, err := getDigest(file)
	require.NoError(t, err)
	assert.NotEqual(t, brokenDigest.FileFuzzyHash, repairedDigest.FileFuzzyHash,
		"same size and mtime but new content (ctime changed) must be hashed again")
	require.NoError(t, c.Close())
}

func TestDigestCache_getDigestUsesCache(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	writeTestFile(t, file, []byte("hello"), time.Unix(1_700_000_000, 0))
	c := openTestCache(t, filepath.Join(dir, "digests.tsv"))
	SetDigestCache(c)
	defer SetDigestCache(nil)

	first, err := getDigest(file)
	require.NoError(t, err)
	second, err := getDigest(file)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	hits, misses := c.Stats()
	assert.Equal(t, [2]int64{1, 1}, [2]int64{hits, misses}, "second digest must come from the cache")
	require.NoError(t, c.Close())
}

func TestDigestCache_recentlyChangedFileIsNotStored(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "fresh.txt")
	require.NoError(t, os.WriteFile(file, []byte("fresh"), 0o644))
	info, err := os.Lstat(file)
	require.NoError(t, err)
	c, err := OpenDigestCache(filepath.Join(dir, "digests.tsv"))
	require.NoError(t, err)

	c.Store(file, info, "f00000000")
	_, found := c.Lookup(file, info)
	assert.False(t, found, "a file changed within the racy window must not be cached")
	require.NoError(t, c.Close())
}

func TestDigestCache_readOnlyFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only file")
	}
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "digests.tsv")
	file := filepath.Join(dir, "a.bin")
	info := writeTestFile(t, file, []byte("content"), time.Unix(1_700_000_000, 0))
	c := openTestCache(t, cachePath)
	c.Store(file, info, "f11111111")
	require.NoError(t, c.Close())
	require.NoError(t, os.Chmod(cachePath, 0o400))
	before, err := os.ReadFile(cachePath)
	require.NoError(t, err)

	c = openTestCache(t, cachePath)
	assert.True(t, c.ReadOnly(), "a read-only cache file must open read-only")
	hash, found := c.Lookup(file, info)
	assert.True(t, found)
	assert.Equal(t, "f11111111", hash)
	other := filepath.Join(dir, "b.bin")
	otherInfo := writeTestFile(t, other, []byte("other"), time.Unix(1_700_000_000, 0))
	c.Store(other, otherInfo, "f22222222")
	require.NoError(t, c.Close())

	after, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a read-only cache must not be written")
}

func TestDigestCache_secondProcessGetsReadOnly(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "digests.tsv")
	first := openTestCache(t, cachePath)
	defer first.Close()
	second := openTestCache(t, cachePath)
	defer second.Close()
	assert.False(t, first.ReadOnly())
	assert.True(t, second.ReadOnly(), "a cache locked by another opener must open read-only")
}

func TestDigestCache_unknownHeaderStartsFresh(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "digests.tsv")
	require.NoError(t, os.WriteFile(cachePath, []byte("# some other format\n\"/x\"\t1\t2\t3\t4\t5\tf0\n"), 0o600))

	c := openTestCache(t, cachePath)
	assert.Empty(t, c.entries)
	require.NoError(t, c.Close())
	assert.Equal(t, []string{digestCacheHeader}, cacheLines(t, cachePath))
}

func TestDigestCache_pathWithTabAndNewline(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "digests.tsv")
	file := filepath.Join(dir, "odd\tname\nwith newline")
	info := writeTestFile(t, file, []byte("odd"), time.Unix(1_700_000_000, 0))
	c := openTestCache(t, cachePath)
	c.Store(file, info, "f33333333")
	require.NoError(t, c.Close())

	c = openTestCache(t, cachePath)
	hash, found := c.Lookup(file, info)
	assert.True(t, found, "cache file:\n%v", cacheLines(t, cachePath))
	assert.Equal(t, "f33333333", hash)
	require.NoError(t, c.Close())
}

func TestDigestCache_compactsWhenMostlyStale(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "digests.tsv")
	file := filepath.Join(dir, "a.bin")
	info := writeTestFile(t, file, []byte("a"), time.Unix(1_700_000_000, 0))
	c := openTestCache(t, cachePath)
	for i := 0; i < digestCacheCompactSlack+10; i++ {
		c.Store(file, info, "f44444444")
	}
	require.NoError(t, c.Close())
	require.Greater(t, len(cacheLines(t, cachePath)), digestCacheCompactSlack)

	c = openTestCache(t, cachePath)
	hash, found := c.Lookup(file, info)
	require.NoError(t, c.Close())
	assert.True(t, found)
	assert.Equal(t, "f44444444", hash)
	assert.Len(t, cacheLines(t, cachePath), 2, "header plus one entry after compaction")
}
