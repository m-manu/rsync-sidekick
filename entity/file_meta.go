package entity

import (
	"fmt"
	"time"
)

type FileMeta struct {
	Size              int64
	ModifiedTimestamp int64
}

func (f FileMeta) String() string {
	return fmt.Sprintf("{size: %d, modified: %v}", f.Size, time.Unix(f.ModifiedTimestamp, 0))
}
