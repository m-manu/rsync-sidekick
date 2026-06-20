package fs

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// BTRFS ioctl and tree constants
const (
	btrfsMagic         = 0x94
	btrfsIocTreeSearch = 0xD0089411 // _IOWR(0x94, 17, 4096)

	btrfsInodeItemKey = 1
	btrfsDirIndexKey  = 96

	// btrfs_dir_item.type values
	btrfsFtRegFile = 1
	btrfsFtDir     = 2
)

type btrfsSearchKey struct {
	TreeID      uint64
	MinObjectID uint64
	MaxObjectID uint64
	MinOffset   uint64
	MaxOffset   uint64
	MinTransID  uint64
	MaxTransID  uint64
	MinType     uint32
	MaxType     uint32
	NrItems     uint32
	_unused     [36]byte
}

type btrfsSearchHeader struct {
	TransID  uint64
	ObjectID uint64
	Offset   uint64
	Type     uint32
	Len      uint32
}

type btrfsSearchArgs struct {
	Key btrfsSearchKey
	Buf [3992]byte
}

// btrfsDirEntry is a directory entry read from the BTRFS dir index.
type btrfsDirEntry struct {
	InodeID uint64
	Name    string
	Type    uint8
}

// btrfsInodeInfo holds the fields we need from a btrfs_inode_item.
type btrfsInodeInfo struct {
	Size    int64
	Mode    uint32
	MTimeSec int64
}

// mountFSTypeCache maps mountpoint → fs type, built once from /proc/self/mounts.
var mountFSTypeCache map[string]string
var mountFSTypeCacheOnce sync.Once

func buildMountCache() {
	mountFSTypeCache = make(map[string]string)
	data, err := syscall.Open("/proc/self/mounts", syscall.O_RDONLY, 0)
	if err != nil {
		return
	}
	defer syscall.Close(data)
	buf := make([]byte, 64*1024)
	n, _ := syscall.Read(data, buf)
	if n <= 0 {
		return
	}
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mountpoint := fields[1]
		// Unescape octal sequences (e.g. \040 for space)
		mountpoint = unescapeOctal(mountpoint)
		fstype := fields[2]
		mountFSTypeCache[mountpoint] = fstype
	}
}

func unescapeOctal(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i+3 < len(s) && s[i] == '\\' && s[i+1] >= '0' && s[i+1] <= '7' {
			val := (s[i+1]-'0')*64 + (s[i+2]-'0')*8 + (s[i+3] - '0')
			b.WriteByte(val)
			i += 3
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// IsBtrfs returns true if the given path is on a BTRFS filesystem,
// using a cached mount table with longest-prefix matching.
func IsBtrfs(path string) bool {
	return fsTypeForPath(path) == "btrfs"
}

func fsTypeForPath(path string) string {
	mountFSTypeCacheOnce.Do(buildMountCache)
	// Longest prefix match
	best := ""
	bestLen := 0
	for mp, ft := range mountFSTypeCache {
		if strings.HasPrefix(path, mp) && len(mp) > bestLen {
			best = ft
			bestLen = len(mp)
		}
	}
	return best
}

// getSubvolID returns the BTRFS subvolume tree ID for the given path
// by reading the inode's objectid via ioctl or using the root tree.
// For simplicity we use the mount's subvolid from /proc/self/mounts or
// fall back to FS_TREE (5).
func getSubvolID(path string) uint64 {
	// Use BTRFS_IOC_INO_LOOKUP to get the tree ID
	// struct btrfs_ioctl_ino_lookup_args { treeid u64, objectid u64, name [4080]byte }
	type inoLookupArgs struct {
		TreeID   uint64
		ObjectID uint64
		Name     [4080]byte
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return 5
	}
	defer syscall.Close(fd)

	var args inoLookupArgs
	args.ObjectID = 256 // BTRFS_FIRST_FREE_OBJECTID — gets the subvol info
	// BTRFS_IOC_INO_LOOKUP = _IOWR(0x94, 18, 4096)
	const iocInoLookup = 0xC0109412
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), iocInoLookup, uintptr(unsafe.Pointer(&args)))
	if errno != 0 {
		return 5
	}
	if args.TreeID == 0 {
		return 5
	}
	return args.TreeID
}

// getInodeID returns the BTRFS inode number for the given directory.
func getInodeID(path string) uint64 {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 256
	}
	return st.Ino
}

func doTreeSearch(fd uintptr, args *btrfsSearchArgs) (uint32, error) {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(btrfsIocTreeSearch), uintptr(unsafe.Pointer(args)))
	if errno != 0 {
		return 0, errno
	}
	return args.Key.NrItems, nil
}

// readDirIndex reads all DIR_INDEX entries for a directory inode via TREE_SEARCH.
func readDirIndex(fd int, treeID uint64, dirInodeID uint64) ([]btrfsDirEntry, error) {
	var result []btrfsDirEntry
	var args btrfsSearchArgs

	args.Key.TreeID = treeID
	args.Key.MinObjectID = dirInodeID
	args.Key.MaxObjectID = dirInodeID
	args.Key.MinType = btrfsDirIndexKey
	args.Key.MaxType = btrfsDirIndexKey
	args.Key.MinOffset = 0
	args.Key.MaxOffset = ^uint64(0)
	args.Key.MinTransID = 0
	args.Key.MaxTransID = ^uint64(0)

	for {
		args.Key.NrItems = 4096
		nr, err := doTreeSearch(uintptr(fd), &args)
		if err != nil {
			return result, fmt.Errorf("dir index search: %w", err)
		}
		if nr == 0 {
			break
		}

		headerSize := int(unsafe.Sizeof(btrfsSearchHeader{}))
		offset := 0
		for i := uint32(0); i < nr; i++ {
			if offset+headerSize > len(args.Buf) {
				break
			}
			hdr := (*btrfsSearchHeader)(unsafe.Pointer(&args.Buf[offset]))
			offset += headerSize

			if hdr.Type == btrfsDirIndexKey && int(hdr.Len) >= 30 {
				// btrfs_dir_item: btrfs_disk_key(17) + transid(8) + data_len(2) + name_len(2) + type(1) = 30
				buf := args.Buf[offset : offset+int(hdr.Len)]
				// btrfs_disk_key: objectid(8) + type(1) + offset(8) = 17 bytes
				childInodeID := binary.LittleEndian.Uint64(buf[0:8])
				// skip transid at offset 17
				nameLen := binary.LittleEndian.Uint16(buf[25:27])
				dtype := buf[29]
				name := ""
				if 30+int(nameLen) <= len(buf) {
					name = string(buf[30 : 30+int(nameLen)])
				}
				result = append(result, btrfsDirEntry{
					InodeID: childInodeID,
					Name:    name,
					Type:    dtype,
				})
			}

			offset += int(hdr.Len)
			args.Key.MinOffset = hdr.Offset + 1
			if args.Key.MinOffset == 0 {
				break
			}
		}
	}
	return result, nil
}

// readInodeItems batch-reads INODE_ITEM entries for a range of inode IDs.
func readInodeItems(fd int, treeID uint64, minInode, maxInode uint64) (map[uint64]btrfsInodeInfo, error) {
	result := make(map[uint64]btrfsInodeInfo)
	var args btrfsSearchArgs

	args.Key.TreeID = treeID
	args.Key.MinObjectID = minInode
	args.Key.MaxObjectID = maxInode
	args.Key.MinType = btrfsInodeItemKey
	args.Key.MaxType = btrfsInodeItemKey
	args.Key.MinOffset = 0
	args.Key.MaxOffset = 0 // INODE_ITEM offset is always 0
	args.Key.MinTransID = 0
	args.Key.MaxTransID = ^uint64(0)

	for {
		args.Key.NrItems = 4096
		nr, err := doTreeSearch(uintptr(fd), &args)
		if err != nil {
			return result, fmt.Errorf("inode search: %w", err)
		}
		if nr == 0 {
			break
		}

		headerSize := int(unsafe.Sizeof(btrfsSearchHeader{}))
		offset := 0
		for i := uint32(0); i < nr; i++ {
			if offset+headerSize > len(args.Buf) {
				break
			}
			hdr := (*btrfsSearchHeader)(unsafe.Pointer(&args.Buf[offset]))
			offset += headerSize

			if hdr.Type == btrfsInodeItemKey && hdr.Len >= 160 {
				buf := args.Buf[offset : offset+160]
				size := int64(binary.LittleEndian.Uint64(buf[16:24]))
				mode := binary.LittleEndian.Uint32(buf[52:56])
				mtimeSec := int64(binary.LittleEndian.Uint64(buf[136:144]))
				result[hdr.ObjectID] = btrfsInodeInfo{
					Size:     size,
					Mode:     mode,
					MTimeSec: mtimeSec,
				}
			}

			offset += int(hdr.Len)
			args.Key.MinObjectID = hdr.ObjectID + 1
			if args.Key.MinObjectID == 0 {
				break
			}
		}
	}
	return result, nil
}

// BtrfsWalk performs a recursive directory walk using BTRFS_IOC_TREE_SEARCH
// to batch-read directory entries and inode metadata. If a subdirectory is on
// a different device (submount), it falls back to standard stat-based walk for
// that subtree. Returns ErrNotBtrfs if the root path is not on BTRFS.
var ErrNotBtrfs = fmt.Errorf("not a btrfs filesystem")

func BtrfsWalk(dirPath string, excludedNames map[string]struct{}, counter *int32) ([]DirEntry, error) {
	if !IsBtrfs(dirPath) {
		return nil, ErrNotBtrfs
	}

	treeID := getSubvolID(dirPath)
	rootInode := getInodeID(dirPath)

	fd, err := syscall.Open(dirPath, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dirPath, err)
	}
	defer syscall.Close(fd)

	var result []DirEntry

	type walkItem struct {
		inodeID      uint64
		relativePath string
		absPath      string
		treeID       uint64
	}

	queue := []walkItem{{inodeID: rootInode, relativePath: "", absPath: dirPath, treeID: treeID}}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		// Check if this directory is still on BTRFS (not a submount with different fs)
		if item.relativePath != "" && !IsBtrfs(item.absPath) {
			// Different filesystem — fall back to stat-based walk for this subtree
			fallbackEntries, err := fallbackWalkDir(item.absPath, item.relativePath, excludedNames, counter)
			if err == nil {
				result = append(result, fallbackEntries...)
			}
			continue
		}

		// Detect subvolume boundary: child dir may be a different subvolume
		curTreeID := item.treeID
		if item.relativePath != "" {
			newTreeID := getSubvolID(item.absPath)
			if newTreeID != curTreeID {
				curTreeID = newTreeID
				// Re-open fd for the new subvolume's inode space
				item.inodeID = getInodeID(item.absPath)
			}
		}

		// Read directory entries via ioctl
		dirEntries, err := readDirIndex(fd, curTreeID, item.inodeID)
		if err != nil {
			// ioctl failed — fall back for this directory
			fallbackEntries, err := fallbackWalkDir(item.absPath, item.relativePath, excludedNames, counter)
			if err == nil {
				result = append(result, fallbackEntries...)
			}
			continue
		}

		// Collect inode IDs for batch lookup
		var minIno, maxIno uint64
		type childInfo struct {
			relPath string
			absPath string
			dtype   uint8
		}
		children := make(map[uint64]childInfo, len(dirEntries))
		for _, de := range dirEntries {
			if _, excluded := excludedNames[de.Name]; excluded {
				continue
			}
			if strings.HasPrefix(de.Name, "._") {
				continue
			}
			if de.Type != btrfsFtRegFile && de.Type != btrfsFtDir {
				continue
			}
			relPath := de.Name
			if item.relativePath != "" {
				relPath = item.relativePath + "/" + de.Name
			}
			absPath := item.absPath + "/" + de.Name
			children[de.InodeID] = childInfo{relPath: relPath, absPath: absPath, dtype: de.Type}
			if minIno == 0 || de.InodeID < minIno {
				minIno = de.InodeID
			}
			if de.InodeID > maxIno {
				maxIno = de.InodeID
			}
		}

		if len(children) == 0 {
			continue
		}

		// Batch-read inode items
		inodeInfos, err := readInodeItems(fd, curTreeID, minIno, maxIno)
		if err != nil {
			continue
		}

		for ino, child := range children {
			info, ok := inodeInfos[ino]
			if !ok {
				// Inode not found in batch — may be in a different subvolume, stat individually
				var st syscall.Stat_t
				if syscall.Stat(child.absPath, &st) != nil {
					continue
				}
				info = btrfsInodeInfo{Size: st.Size, Mode: st.Mode, MTimeSec: st.Mtim.Sec}
			}

			if child.dtype == btrfsFtDir {
				result = append(result, DirEntry{
					RelativePath: child.relPath,
					Size:         0,
					ModTime:      info.MTimeSec,
					IsDir:        true,
				})
				queue = append(queue, walkItem{
					inodeID:      ino,
					relativePath: child.relPath,
					absPath:      child.absPath,
					treeID:       curTreeID,
				})
			} else {
				result = append(result, DirEntry{
					RelativePath: child.relPath,
					Size:         info.Size,
					ModTime:      info.MTimeSec,
					IsDir:        false,
				})
				if counter != nil {
					atomic.AddInt32(counter, 1)
				}
			}
		}
	}

	return result, nil
}

// fallbackWalkDir does a standard stat-based walk for a subtree that's not on BTRFS.
func fallbackWalkDir(absDir, relPrefix string, excludedNames map[string]struct{}, counter *int32) ([]DirEntry, error) {
	var result []DirEntry
	f, err := syscall.Open(absDir, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(f)

	// Read directory entries via getdents, then stat each
	entries, err := readDir(absDir)
	if err != nil {
		return nil, err
	}
	for _, name := range entries {
		if _, excluded := excludedNames[name]; excluded {
			continue
		}
		if strings.HasPrefix(name, "._") {
			continue
		}
		fullPath := absDir + "/" + name
		var st syscall.Stat_t
		if syscall.Lstat(fullPath, &st) != nil {
			continue
		}
		relPath := name
		if relPrefix != "" {
			relPath = relPrefix + "/" + name
		}
		mode := st.Mode & syscall.S_IFMT
		if mode == syscall.S_IFREG {
			result = append(result, DirEntry{
				RelativePath: relPath,
				Size:         st.Size,
				ModTime:      st.Mtim.Sec,
				IsDir:        false,
			})
			if counter != nil {
				atomic.AddInt32(counter, 1)
			}
		} else if mode == syscall.S_IFDIR {
			result = append(result, DirEntry{
				RelativePath: relPath,
				Size:         0,
				ModTime:      st.Mtim.Sec,
				IsDir:        true,
			})
			// Recurse
			subEntries, err := fallbackWalkDir(fullPath, relPath, excludedNames, counter)
			if err == nil {
				result = append(result, subEntries...)
			}
		}
	}
	return result, nil
}

// readDir reads directory entry names using os.ReadDir.
func readDir(path string) ([]string, error) {
	d, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(d)

	var names []string
	buf := make([]byte, 8192)
	for {
		n, err := syscall.ReadDirent(d, buf)
		if err != nil {
			return names, err
		}
		if n <= 0 {
			break
		}
		offset := 0
		for offset < n {
			dirent := (*syscall.Dirent)(unsafe.Pointer(&buf[offset]))
			offset += int(dirent.Reclen)
			nameBytes := (*[256]byte)(unsafe.Pointer(&dirent.Name[0]))
			nameLen := 0
			for nameLen < len(nameBytes) && nameBytes[nameLen] != 0 {
				nameLen++
			}
			name := string(nameBytes[:nameLen])
			if name == "." || name == ".." {
				continue
			}
			names = append(names, name)
		}
	}
	return names, nil
}
