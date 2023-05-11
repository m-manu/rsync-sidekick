package lib

import (
	set "github.com/deckarep/golang-set/v2"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// IsReadableDirectory checks whether a readable directory exists at given path
func IsReadableDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// GetFileExt gets file extension in lower case
func GetFileExt(path string) string {
	ext := filepath.Ext(path)
	return strings.ToLower(ext)
}

// IsReadableFile checks whether argument is a readable file
func IsReadableFile(path string) bool {
	fileInfo, statErr := os.Stat(path)
	if statErr != nil {
		return false
	}
	return fileInfo.Mode().IsRegular()
}

// WriteSliceToFile writes a slice of strings to a file
func WriteSliceToFile(slice []string, fileName string) {
	sliceAsString := strings.Join(slice, "\n")
	_ = os.WriteFile(fileName, []byte(sliceAsString), fs.ModePerm)
}

// LineSeparatedStrToMap converts a line-separated string to a Set of values
func LineSeparatedStrToMap(lineSeparatedString string) (entries set.Set[string], firstFew []string) {
	entries = set.NewThreadUnsafeSetWithSize[string](20)
	firstFew = []string{}
	for _, e := range strings.Split(lineSeparatedString, "\n") {
		entries.Add(strings.TrimSpace(e))
		firstFew = append(firstFew, e)
	}
	if len(firstFew) > 3 {
		firstFew = firstFew[0:3]
	}
	entries.Remove("")
	return
}
