package entity

import "fmt"

// FileDigest contains properties of a file that makes the file unique to a very high degree of confidence
type FileDigest struct {
	FileExtension string
	FileSize      int64
	FileFuzzyHash string
}

func (f FileDigest) String() string {
	return fmt.Sprintf("%v/%v/%v", f.FileExtension, f.FileSize, f.FileFuzzyHash)
}
