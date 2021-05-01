package filesutil

import (
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

// WriteSliceToFile writes a slice to a file
func WriteSliceToFile(slice []string, fileName string) {
	sliceAsString := strings.Join(slice, "\n")
	_ = os.WriteFile(fileName, []byte(sliceAsString), fs.ModePerm)
}
