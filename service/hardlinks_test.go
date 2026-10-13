//go:build !windows

package service

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetHardlinkHashes(t *testing.T) {
	t.Helper()
	hardlinkHashes.Lock()
	hardlinkHashes.byIdentity = make(map[fileIdentity]*hardlinkHash)
	hardlinkHashes.Unlock()
}

// linkedPair creates a file with a second name and returns the stat of both names.
func linkedPair(t *testing.T) (os.FileInfo, os.FileInfo) {
	t.Helper()
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first.bin"), filepath.Join(dir, "second.bin")
	require.NoError(t, os.WriteFile(first, []byte("shared content"), 0o644))
	require.NoError(t, os.Link(first, second))
	firstInfo, err := os.Lstat(first)
	require.NoError(t, err)
	secondInfo, err := os.Lstat(second)
	require.NoError(t, err)
	return firstInfo, secondInfo
}

func TestHashOnce_HardlinksAreHashedOnce(t *testing.T) {
	resetHardlinkHashes(t)
	firstInfo, secondInfo := linkedPair(t)
	var computed atomic.Int32
	compute := func() (string, error) { computed.Add(1); return "s1234", nil }

	first, err1 := hashOnce(firstInfo, compute)
	second, err2 := hashOnce(secondInfo, compute)

	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.Equal(t, "s1234", first)
	assert.Equal(t, "s1234", second)
	assert.Equal(t, int32(1), computed.Load(), "the second name reuses the hash of the first")
}

func TestHashOnce_FilesWithOneNameAreNeitherSharedNorRemembered(t *testing.T) {
	resetHardlinkHashes(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "single.bin")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	info, err := os.Lstat(path)
	require.NoError(t, err)
	var computed atomic.Int32
	compute := func() (string, error) { computed.Add(1); return "f1", nil }

	_, _ = hashOnce(info, compute)
	_, _ = hashOnce(info, compute)

	assert.Equal(t, int32(2), computed.Load())
	assert.Empty(t, hardlinkHashes.byIdentity, "files without hardlinks cost no table entry")
}

func TestHashOnce_FailureIsNotReusedByTheOtherName(t *testing.T) {
	resetHardlinkHashes(t)
	firstInfo, secondInfo := linkedPair(t)

	_, err := hashOnce(firstInfo, func() (string, error) { return "", errors.New("read error") })
	require.Error(t, err)
	hash, err := hashOnce(secondInfo, func() (string, error) { return "s99", nil })

	require.NoError(t, err)
	assert.Equal(t, "s99", hash)
}

func TestHashOnce_ConcurrentWorkersOnTheSameInodeHashItOnce(t *testing.T) {
	resetHardlinkHashes(t)
	firstInfo, secondInfo := linkedPair(t)
	var computed atomic.Int32
	release := make(chan struct{})
	compute := func() (string, error) {
		computed.Add(1)
		<-release
		return "s7", nil
	}

	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			info := firstInfo
			if i%2 == 1 {
				info = secondInfo
			}
			results[i], _ = hashOnce(info, compute)
		}(i)
	}
	close(release)
	wg.Wait()

	assert.Equal(t, int32(1), computed.Load())
	for _, r := range results {
		assert.Equal(t, "s7", r)
	}
}

func TestGetDigest_HardlinkedNamesGetTheSameDigest(t *testing.T) {
	resetHardlinkHashes(t)
	dir := t.TempDir()
	content := make([]byte, 50_000)
	content[25_000] = 7
	first, second := filepath.Join(dir, "a.bin"), filepath.Join(dir, "b.bin")
	require.NoError(t, os.WriteFile(first, content, 0o644))
	require.NoError(t, os.Link(first, second))

	d1, err1 := GetDigest(first)
	d2, err2 := GetDigest(second)

	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.Equal(t, d1, d2)
}
