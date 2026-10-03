package main

import (
	"path/filepath"
	"strings"

	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/m-manu/rsync-sidekick/v2/service"
)

// movedFiles maps the old absolute path of every file moved at the destination to its new
// one. Destination actions run before the archive scan, whose file lists were taken
// earlier: a copy from a path that has since been moved away is redirected through this.
type movedFiles map[string]string

// record remembers where a successfully performed move put its file.
func (m movedFiles) record(a action.SyncAction) {
	if move, ok := a.(action.MoveFileAction); ok {
		m[filepath.Join(move.BasePath, move.RelativeFromPath)] = filepath.Join(move.BasePath, move.RelativeToPath)
	}
}

// rebaseWalks updates archive file lists taken before the moves: an entry that was moved
// within the archive path is listed at its new path, one moved out of it is dropped.
// Walks without such entries are returned as they are; the others get a fresh map, since
// a walk can share its map with the destination's file list.
func (m movedFiles) rebaseWalks(walks []service.ArchiveWalk) []service.ArchiveWalk {
	if len(m) == 0 {
		return walks
	}
	result := make([]service.ArchiveWalk, len(walks))
	for i, walk := range walks {
		result[i] = walk
		root, err := filepath.Abs(walk.Path)
		if err != nil {
			continue
		}
		var files map[string]entity.FileMeta
		for oldAbs, newAbs := range m {
			oldRel, inside := relBelow(root, oldAbs)
			if !inside {
				continue
			}
			meta, listed := walk.Files[oldRel]
			if !listed {
				continue
			}
			if files == nil {
				files = make(map[string]entity.FileMeta, len(walk.Files))
				for path, fileMeta := range walk.Files {
					files[path] = fileMeta
				}
			}
			delete(files, oldRel)
			if newRel, stillInside := relBelow(root, newAbs); stillInside {
				files[newRel] = meta
			}
		}
		if files != nil {
			result[i].Files = files
		}
	}
	return result
}

func relBelow(root, path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// redirect points an action at the current location of a file that was moved: the source
// of a copy, or the target of a timestamp fix — that one is aimed at a candidate's old
// path, and sorting by destination directory can put its move first.
func (m movedFiles) redirect(a action.SyncAction) action.SyncAction {
	switch act := a.(type) {
	case action.CopyFileAction:
		if newPath, wasMoved := m[act.AbsSourcePath]; wasMoved {
			act.AbsSourcePath = newPath
			return act
		}
	case action.PropagateTimestampAction:
		if newPath, wasMoved := m[act.DestinationPath()]; wasMoved {
			if rel, err := filepath.Rel(act.DestinationBaseDirPath, newPath); err == nil {
				act.DestinationFileRelativePath = rel
				return act
			}
		}
	}
	return a
}
