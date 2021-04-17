package entity

import "fmt"

type FileExtAndSize struct {
	FileExtension string
	FileSize      int64
}

func (f FileExtAndSize) String() string {
	return fmt.Sprintf("%v/%v", f.FileExtension, f.FileSize)
}
