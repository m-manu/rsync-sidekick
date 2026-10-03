package action

import (
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexOfAction returns the position of the action with the given destination path.
func indexOfAction(actions []SyncAction, destinationPath string) int {
	for i, a := range actions {
		if a.destinationPath() == destinationPath {
			return i
		}
	}
	return -1
}

func TestSortByDestinationDir_DirectoryComesBeforeItsFiles(t *testing.T) {
	// The exact shape that failed in the field: a nested destination directory that has to
	// be created before the file can be copied into it.
	dir := "/dest/image/3b/fc"
	file := dir + "/3bfce60345552c9854f04947ec345341fecd2cbbceeb4991aaa1be7087611acd.jpg"

	// Enough entries that sorting really has to move things around, and with the copy
	// placed before its directory so a broken sort cannot accidentally look correct.
	actions := []SyncAction{
		CopyFileAction{AbsSourcePath: "/archive/x.jpg", AbsDestPath: file, SourceModTime: time.Unix(1, 0)},
		MakeDirectoryAction{AbsoluteDirPath: dir},
	}
	for i := 0; i < 40; i++ {
		other := "/dest/zzz/" + string(rune('a'+i%26)) + "/file.bin"
		actions = append(actions, CopyFileAction{
			AbsSourcePath: "/archive/other.bin", AbsDestPath: other, SourceModTime: time.Unix(1, 0),
		})
		actions = append(actions, MakeDirectoryAction{AbsoluteDirPath: filepath.Dir(other)})
	}

	SortByDestinationDir(actions)

	dirAt := indexOfAction(actions, dir)
	fileAt := indexOfAction(actions, file)
	require.NotEqual(t, -1, dirAt)
	require.NotEqual(t, -1, fileAt)
	assert.Less(t, dirAt, fileAt,
		"the directory must be created before the file is copied into it")
}

func TestSortByDestinationDir_EveryDirectoryPrecedesItsFiles(t *testing.T) {
	// Same guarantee across many directories at once: a copy may never come before the
	// creation of the directory it targets.
	var actions []SyncAction
	dirs := []string{"/dest/b/2", "/dest/a/1", "/dest/c/3", "/dest/a/2", "/dest/b/1"}
	for _, dir := range dirs {
		actions = append(actions,
			CopyFileAction{AbsSourcePath: "/archive/f", AbsDestPath: dir + "/f.bin", SourceModTime: time.Unix(1, 0)},
			MakeDirectoryAction{AbsoluteDirPath: dir},
		)
	}

	SortByDestinationDir(actions)

	for _, dir := range dirs {
		dirAt := indexOfAction(actions, dir)
		fileAt := indexOfAction(actions, dir+"/f.bin")
		require.NotEqual(t, -1, dirAt, "mkdir for %s missing", dir)
		assert.Less(t, dirAt, fileAt, "mkdir for %s must precede the copy into it", dir)
	}
}

func TestSortByDestinationDir_ActuallySortsByDirectory(t *testing.T) {
	// The keys must travel with their action. Computing them into a side slice and letting
	// sort swap only the actions produces a permutation that is not ordered at all.
	var actions []SyncAction
	for _, name := range []string{"m", "c", "x", "a", "q", "b", "z", "d", "e", "f", "g", "h"} {
		actions = append(actions, CopyFileAction{
			AbsSourcePath: "/archive/f",
			AbsDestPath:   "/dest/" + name + "/file.bin",
			SourceModTime: time.Unix(1, 0),
		})
	}

	SortByDestinationDir(actions)

	dirs := make([]string, len(actions))
	for i, a := range actions {
		dirs[i] = filepath.Dir(a.destinationPath())
	}
	assert.True(t, sort.StringsAreSorted(dirs),
		"actions must end up ordered by destination directory, got %v", dirs)
}

func TestSortByDestinationDir_KeepsOrderWithinADirectory(t *testing.T) {
	// Stable within one directory: performActions relies on the order of moves, so equal
	// keys must not be reshuffled.
	first := "/dest/same/a.bin"
	second := "/dest/same/b.bin"
	third := "/dest/same/c.bin"
	actions := []SyncAction{
		CopyFileAction{AbsSourcePath: "/archive/1", AbsDestPath: first, SourceModTime: time.Unix(1, 0)},
		CopyFileAction{AbsSourcePath: "/archive/2", AbsDestPath: second, SourceModTime: time.Unix(1, 0)},
		CopyFileAction{AbsSourcePath: "/archive/3", AbsDestPath: third, SourceModTime: time.Unix(1, 0)},
	}

	SortByDestinationDir(actions)

	assert.Equal(t, first, actions[0].destinationPath())
	assert.Equal(t, second, actions[1].destinationPath())
	assert.Equal(t, third, actions[2].destinationPath())
}

func TestSortByDestinationDir_EmptyAndSingle(t *testing.T) {
	SortByDestinationDir(nil)
	single := []SyncAction{MakeDirectoryAction{AbsoluteDirPath: "/dest/only"}}
	SortByDestinationDir(single)
	assert.Equal(t, "/dest/only", single[0].destinationPath())
}
