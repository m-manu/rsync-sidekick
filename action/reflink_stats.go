package action

import "sync/atomic"

// reflinkFallbacks counts reflink copies that had to be done as a full copy because the
// destination filesystem doesn't support FICLONE.
var reflinkFallbacks atomic.Int64

// ReflinkFallbacks reports how many requested reflinks ended up as full copies. A caller
// that counts reflinks by intent needs this to report them honestly.
func ReflinkFallbacks() int64 {
	return reflinkFallbacks.Load()
}

// movesAsCopies counts moves that rename couldn't do and that were copied instead.
var movesAsCopies atomic.Int64

// MovesAsCopies reports how many moves were done as copies, the original kept.
func MovesAsCopies() int64 {
	return movesAsCopies.Load()
}
