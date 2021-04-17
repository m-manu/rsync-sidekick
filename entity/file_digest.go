package entity

import "fmt"

type FileDigest struct {
	FileExtension string
	FileSize      int64
	FileFuzzyHash string
}

func (f FileDigest) String() string {
	return fmt.Sprintf("%v/%v/%v", f.FileExtension, f.FileSize, f.FileFuzzyHash)
}
