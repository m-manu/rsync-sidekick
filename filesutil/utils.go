package filesutil

import (
	"os"
	"path/filepath"
	"strings"
)

func IsReadableDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func GetFileExt(path string) string {
	ext := filepath.Ext(path)
	return strings.ToLower(ext)
}
