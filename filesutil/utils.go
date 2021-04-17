package filesutil

import (
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
