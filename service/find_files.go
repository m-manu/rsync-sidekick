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
	return FindFilesFromDirectoryWithFS(rsfs.NewLocalFS(), dirPath, excludedFiles)
}

// FindFilesFromDirectoryWithFS is like FindFilesFromDirectory but uses the given FileSystem.
func FindFilesFromDirectoryWithFS(fsys rsfs.FileSystem, dirPath string, excludedFiles set.Set[string]) (
	files map[string]entity.FileMeta,
	totalSizeOfFiles int64,
	findFilesErr error,
) {
	excludedMap := make(map[string]struct{}, excludedFiles.Cardinality())
	excludedFiles.Each(func(s string) bool {
		excludedMap[s] = struct{}{}
		return false
	})
	entries, err := fsys.Walk(dirPath, excludedMap)
	if err != nil {
		return map[string]entity.FileMeta{}, 0, fmt.Errorf("couldn't scan directory %s: %v", dirPath, err)
	}
	allFiles := make(map[string]entity.FileMeta, len(entries))
	for _, e := range entries {
		allFiles[e.RelativePath] = entity.FileMeta{
			Size:              e.Size,
			ModifiedTimestamp: e.ModTime,
		}
		totalSizeOfFiles += e.Size
	}
	return allFiles, totalSizeOfFiles, nil
}
