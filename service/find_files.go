package service

import (
	"fmt"
	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/fmte"
	"io/fs"
	"path/filepath"
	"strings"
)

const numFilesGuess = 10_000

// FindFilesFromDirectory finds all regular files in a given directory
// (Very similar to `find` command on unix-like operating systems)
func FindFilesFromDirectory(dirPath string, excludedFiles set.Set[string]) (
	files map[string]entity.FileMeta,
	totalSizeOfFiles int64,
	findFilesErr error,
) {
	allFiles := make(map[string]entity.FileMeta, numFilesGuess)
	err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			fmte.PrintfErr("skipping \"%s\": %+v\n", path, err)
		}
		// If the file/directory is in excluded files list, ignore it
		if excludedFiles.Contains(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Ignore dot files (Mac)
		if strings.HasPrefix(d.Name(), "._") {
			return nil
		}
		if d.Type().IsRegular() {
			info, infoErr := d.Info()
			if infoErr != nil {
				fmte.PrintfErr("couldn't get metadata of \"%s\": %+v\n", path, infoErr)
				return nil
			}
			relativePath, relErr := filepath.Rel(dirPath, path)
			if relErr != nil {
				fmte.PrintfErr("couldn't comprehend path \"%s\": %+v\n", path, relErr)
				return nil
			}
			allFiles[relativePath] = entity.FileMeta{
				Size:              info.Size(),
				ModifiedTimestamp: info.ModTime().Unix(),
			}
			totalSizeOfFiles += info.Size()
		}
		return nil
	})
	if err != nil {
		return map[string]entity.FileMeta{}, 0, fmt.Errorf("couldn't scan directory %s: %v", dirPath, err)
	}
	return allFiles, totalSizeOfFiles, nil
}
