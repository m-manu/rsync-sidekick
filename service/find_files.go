package service

import (
	"fmt"

	set "github.com/deckarep/golang-set/v2"
	rsfs "github.com/m-manu/rsync-sidekick/fs"

	"github.com/m-manu/rsync-sidekick/entity"
)

const numFilesGuess = 10_000

// FindFilesFromDirectory finds all regular files in a given directory
// (Very similar to `find` command on unix-like operating systems)
func FindFilesFromDirectory(dirPath string, excludedFiles set.Set[string]) (
	files map[string]entity.FileMeta,
	totalSizeOfFiles int64,
	findFilesErr error,
) {
	return FindFilesFromDirectoryWithFS(rsfs.NewLocalFS(), dirPath, excludedFiles, nil)
}

// FindFilesFromDirectoryWithFS is like FindFilesFromDirectory but uses the given FileSystem.
// counter, if non-nil, is incremented atomically for each regular file found during the walk.
func FindFilesFromDirectoryWithFS(fsys rsfs.FileSystem, dirPath string, excludedFiles set.Set[string], counter *int32) (
	files map[string]entity.FileMeta,
	totalSizeOfFiles int64,
	findFilesErr error,
) {
	entries, err := walkWithExclusions(fsys, dirPath, excludedFiles, counter)
	if err != nil {
		return map[string]entity.FileMeta{}, 0, err
	}
	allFiles := make(map[string]entity.FileMeta, len(entries))
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		allFiles[e.RelativePath] = entity.FileMeta{
			Size:              e.Size,
			ModifiedTimestamp: e.ModTime,
		}
		totalSizeOfFiles += e.Size
	}
	return allFiles, totalSizeOfFiles, nil
}

// FindDirsFromDirectory returns a map of relative directory paths to their
// modification timestamps for the given directory tree.
func FindDirsFromDirectory(dirPath string, excludedFiles set.Set[string]) (map[string]int64, error) {
	return FindDirsFromDirectoryWithFS(rsfs.NewLocalFS(), dirPath, excludedFiles)
}

// FindDirsFromDirectoryWithFS is like FindDirsFromDirectory but uses the given FileSystem.
func FindDirsFromDirectoryWithFS(fsys rsfs.FileSystem, dirPath string, excludedFiles set.Set[string]) (map[string]int64, error) {
	entries, err := walkWithExclusions(fsys, dirPath, excludedFiles, nil)
	if err != nil {
		return nil, err
	}
	dirs := make(map[string]int64)
	for _, e := range entries {
		if e.IsDir {
			dirs[e.RelativePath] = e.ModTime
		}
	}
	return dirs, nil
}

func walkWithExclusions(fsys rsfs.FileSystem, dirPath string, excludedFiles set.Set[string], counter *int32) ([]rsfs.DirEntry, error) {
	excludedMap := make(map[string]struct{}, excludedFiles.Cardinality())
	excludedFiles.Each(func(s string) bool {
		excludedMap[s] = struct{}{}
		return false
	})
	entries, err := fsys.Walk(dirPath, excludedMap, counter)
	if err != nil {
		return nil, fmt.Errorf("couldn't scan directory %s: %v", dirPath, err)
	}
	return entries, nil
}
