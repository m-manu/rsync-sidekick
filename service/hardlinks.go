package service

import (
	"os"
	"sync"
)

// fileIdentity names a file independently of its path: hardlinks share it.
type fileIdentity struct {
	dev, ino uint64
}

// hardlinkHash is the hash of one inode, or the hashing still in progress for it.
type hardlinkHash struct {
	done chan struct{}
	hash string
	err  error
}

// hardlinkHashes remembers the hash of every hashed file that has more than one name, so
// its other names reuse it instead of reading the file again. Files with a single name
// never enter it, so it stays empty on trees without hardlinks.
var hardlinkHashes = struct {
	sync.Mutex
	byIdentity map[fileIdentity]*hardlinkHash
}{byIdentity: make(map[fileIdentity]*hardlinkHash)}

// hashOnce returns the hash of the file behind info, computing it with compute only if no
// other name of the same inode was hashed before. Workers asking for the same inode at
// the same time wait for the first one instead of reading the file twice.
func hashOnce(info os.FileInfo, compute func() (string, error)) (string, error) {
	identity, linked := hardlinkIdentity(info)
	if !linked {
		return compute()
	}
	hardlinkHashes.Lock()
	entry, known := hardlinkHashes.byIdentity[identity]
	if !known {
		entry = &hardlinkHash{done: make(chan struct{})}
		hardlinkHashes.byIdentity[identity] = entry
	}
	hardlinkHashes.Unlock()
	if known {
		<-entry.done
		if entry.err == nil {
			return entry.hash, nil
		}
		return compute() // the first attempt failed; this name may still be readable
	}
	entry.hash, entry.err = compute()
	if entry.err != nil {
		hardlinkHashes.Lock()
		delete(hardlinkHashes.byIdentity, identity)
		hardlinkHashes.Unlock()
	}
	close(entry.done)
	return entry.hash, entry.err
}
