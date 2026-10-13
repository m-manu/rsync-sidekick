package service

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
)

// KnownTree is a directory that has already been walked, with its files relative to Root.
// Archive walks reuse it instead of reading the same directories a second time.
type KnownTree struct {
	Root  string
	Files map[string]entity.FileMeta
}

// subPath reports whether path lies inside root (or is root) and returns the relative part.
func subPath(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// filesBelow returns the files of tree below rel, relative to rel.
func filesBelow(tree KnownTree, rel string) map[string]entity.FileMeta {
	if rel == "." {
		return tree.Files
	}
	prefix := rel + string(filepath.Separator)
	files := make(map[string]entity.FileMeta)
	for p, meta := range tree.Files {
		if strings.HasPrefix(p, prefix) {
			files[p[len(prefix):]] = meta
		}
	}
	return files
}

// walkAround walks root like FindFilesFromDirectoryWithFS but leaves out the subtree at
// skipRel. It descends only along the path to skipRel and walks every sibling in full,
// so the skipped subtree is never read.
func walkAround(root, skipRel string, exclusions set.Set[string], counter *int32) (map[string]entity.FileMeta, error) {
	files := make(map[string]entity.FileMeta)
	parts := strings.Split(skipRel, string(filepath.Separator))
	dir, prefix := root, ""
	for _, part := range parts {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			name := e.Name()
			if name == part || exclusions.Contains(name) {
				continue
			}
			path := filepath.Join(dir, name)
			relPath := filepath.Join(prefix, name)
			info, err := os.Lstat(path)
			if err != nil {
				continue
			}
			switch {
			case info.IsDir():
				fsys := rsfs.NewLocalFSForArchive()
				sub, _, err := FindFilesFromDirectoryWithFS(fsys, path, exclusions, counter)
				fsys.Close()
				if err != nil {
					return nil, err
				}
				for p, meta := range sub {
					files[filepath.Join(relPath, p)] = meta
				}
			case info.Mode().IsRegular() && !rsfs.SkipBySize(false, info.Size()):
				files[relPath] = entity.FileMeta{Size: info.Size(), ModifiedTimestamp: info.ModTime().Unix()}
				if counter != nil {
					atomic.AddInt32(counter, 1)
				}
			}
		}
		if exclusions.Contains(part) {
			return files, nil
		}
		dir, prefix = filepath.Join(dir, part), filepath.Join(prefix, part)
	}
	return files, nil
}

// canReuseWalks reports whether a tree walked once can stand in for walking a part of it
// again. Only for local archives, and only if both walks follow the same filesystem
// boundaries, so the reused list is exactly what a separate walk would have found.
func canReuseWalks(destFS rsfs.FileSystem) bool {
	return destFS == nil && rsfs.DefaultArchiveOneFileSystem == rsfs.DefaultOneFileSystem &&
		!rsfs.DefaultArchiveOneFileSystem
}

// walkArchivesReusing is WalkArchives for local archives that may overlap each other or
// the destination (known). Outer paths are walked first, so every path nested in one
// already walked is taken from that walk, and a destination inside an archive path is
// skipped by the walk and filled in from known.
func walkArchivesReusing(archivePaths []string, exclusions set.Set[string], counter *int32,
	known *KnownTree, readable func(string) bool,
) ([]ArchiveWalk, error) {
	type item struct {
		index int
		path  string
		abs   string
	}
	items := make([]item, 0, len(archivePaths))
	for i, p := range archivePaths {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = filepath.Clean(p)
		}
		items = append(items, item{i, p, abs})
	}
	slices.SortStableFunc(items, func(a, b item) int { return len(a.abs) - len(b.abs) })

	var trees []KnownTree
	var knownAbs *KnownTree
	if known != nil {
		if abs, err := filepath.Abs(known.Root); err == nil {
			knownAbs = &KnownTree{Root: abs, Files: known.Files}
			trees = append(trees, *knownAbs)
		}
	}
	results := make([]*ArchiveWalk, len(archivePaths))
	for _, it := range items {
		if !readable(it.path) {
			continue
		}
		var files map[string]entity.FileMeta
		for _, tree := range trees {
			if rel, inside := subPath(tree.Root, it.abs); inside {
				files = filesBelow(tree, rel)
				if counter != nil {
					atomic.AddInt32(counter, int32(len(files)))
				}
				break
			}
		}
		if files == nil {
			var err error
			skipRel, destInside := "", false
			if knownAbs != nil {
				skipRel, destInside = subPath(it.abs, knownAbs.Root)
				destInside = destInside && skipRel != "."
			}
			if destInside {
				files, err = walkAround(it.abs, skipRel, exclusions, counter)
				if err == nil && !pathHasExcludedPart(skipRel, exclusions) {
					for p, meta := range knownAbs.Files {
						files[filepath.Join(skipRel, p)] = meta
					}
					if counter != nil {
						atomic.AddInt32(counter, int32(len(knownAbs.Files)))
					}
				}
			} else {
				fsys := rsfs.NewLocalFSForArchive()
				files, _, err = FindFilesFromDirectoryWithFS(fsys, it.path, exclusions, counter)
				fsys.Close()
			}
			if err != nil {
				return nil, archiveWalkError(it.path, err)
			}
		}
		trees = append(trees, KnownTree{Root: it.abs, Files: files})
		results[it.index] = &ArchiveWalk{Path: it.path, Files: files}
	}
	walks := make([]ArchiveWalk, 0, len(results))
	for _, w := range results {
		if w != nil {
			walks = append(walks, *w)
		}
	}
	return walks, nil
}

func pathHasExcludedPart(rel string, exclusions set.Set[string]) bool {
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if exclusions.Contains(part) {
			return true
		}
	}
	return false
}
