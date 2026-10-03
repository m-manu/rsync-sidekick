package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
	"github.com/m-manu/rsync-sidekick/v2/remote"
	"github.com/m-manu/rsync-sidekick/v2/service"
)

// includeDirs limits the source and destination walks to these directories, relative to
// the roots. Empty means the whole tree. Paths found below them keep the include
// directory as prefix, so every list stays relative to the roots.
var includeDirs []string

// treeWalk walks one directory and returns its files, its directories (only when they
// are walked at all) and the total file size.
type treeWalk func(dir string) (map[string]entity.FileMeta, map[string]int64, int64, error)

// walkIncluded walks root, or only what the include directories select below it. An
// include directory may hold shell wildcards (*, ?, [...]); the walk then starts at its
// last component without one, and the paths found there are matched against it.
// missingOK decides what a missing start directory means: nothing there yet on the
// destination side, a mistake on the source side.
func walkIncluded(root string, missingOK func(dir string) bool, walk treeWalk,
) (map[string]entity.FileMeta, map[string]int64, int64, error) {
	if len(includeDirs) == 0 {
		return walk(root)
	}
	files := make(map[string]entity.FileMeta)
	var dirs map[string]int64
	var size int64
	for _, base := range walkBases(includeDirs) {
		dir := filepath.Join(root, base)
		if missingOK != nil && missingOK(dir) {
			continue
		}
		f, d, _, err := walk(dir)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("include directory %q: %w", base, err)
		}
		for path, meta := range f {
			full := filepath.Join(base, path)
			if includedPath(full) {
				files[full] = meta
				size += meta.Size
			}
		}
		for path, modTime := range d {
			full := filepath.Join(base, path)
			if includedPath(full) {
				if dirs == nil {
					dirs = make(map[string]int64)
				}
				dirs[full] = modTime
			}
		}
	}
	return files, dirs, size, nil
}

// walkBase is the part of an include directory before its first wildcard component.
func walkBase(include string) string {
	parts := strings.Split(include, "/")
	for i, part := range parts {
		if strings.ContainsAny(part, "*?[") {
			return strings.Join(parts[:i], "/")
		}
	}
	return include
}

// walkBases returns the directories to walk, leaving out those inside another one.
func walkBases(includes []string) []string {
	var bases []string
	for _, include := range includes {
		base := walkBase(include)
		if !containsString(bases, base) {
			bases = append(bases, base)
		}
	}
	result := make([]string, 0, len(bases))
	for _, base := range bases {
		covered := false
		for _, other := range bases {
			if other != base && (other == "" || strings.HasPrefix(base, other+"/")) {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, base)
		}
	}
	return result
}

// includedPath reports whether path (relative to the root) lies in one of the include
// directories: its leading components must match one of them, wildcards included.
func includedPath(path string) bool {
	parts := strings.Split(path, "/")
	for _, include := range includeDirs {
		n := strings.Count(include, "/") + 1
		if len(parts) < n {
			continue
		}
		if ok, _ := filepath.Match(include, strings.Join(parts[:n], "/")); ok {
			return true
		}
	}
	return false
}

// filesWalk walks the files below a directory, through fsys when it is set (and then
// without directories), locally otherwise — with directories when withDirs is set.
func filesWalk(fsys rsfs.FileSystem, exclusions set.Set[string], counter *int32, withDirs bool) treeWalk {
	return func(dir string) (map[string]entity.FileMeta, map[string]int64, int64, error) {
		if fsys != nil {
			files, size, err := service.FindFilesFromDirectoryWithFS(fsys, dir, exclusions, counter)
			return files, nil, size, err
		}
		files, size, err := service.FindFilesFromDirectory(dir, exclusions, counter)
		if err != nil || !withDirs {
			return files, nil, size, err
		}
		dirs, err := service.FindDirsFromDirectory(dir, exclusions)
		return files, dirs, size, err
	}
}

// dirsWalk walks only the directories below a directory.
func dirsWalk(fsys rsfs.FileSystem, exclusions set.Set[string]) treeWalk {
	return func(dir string) (map[string]entity.FileMeta, map[string]int64, int64, error) {
		var dirs map[string]int64
		var err error
		if fsys != nil {
			dirs, err = service.FindDirsFromDirectoryWithFS(fsys, dir, exclusions)
		} else {
			dirs, err = service.FindDirsFromDirectory(dir, exclusions)
		}
		return nil, dirs, 0, err
	}
}

// agentWalk walks a directory on the remote host through the agent.
func agentWalk(agentClient *remote.AgentClient, excludedNames []string, counter *int32, intervalMs int64) treeWalk {
	return func(dir string) (map[string]entity.FileMeta, map[string]int64, int64, error) {
		return agentClient.Walk(dir, excludedNames, counter, intervalMs, rsfs.DefaultOneFileSystem)
	}
}

// missingLocally is the missingOK of a local destination: an include directory that
// isn't there yet simply contributes nothing.
func missingLocally(fsys rsfs.FileSystem) func(string) bool {
	if fsys != nil {
		return nil
	}
	return localDirMissing
}

// knownDestinationTree hands the destination walk to the archive walk for reuse — but
// only when it covers the whole destination: with include directories it is partial,
// and an archive path around it would otherwise miss everything outside them.
func knownDestinationTree(root string, files map[string]entity.FileMeta) *service.KnownTree {
	if len(includeDirs) > 0 {
		return nil
	}
	return &service.KnownTree{Root: root, Files: files}
}

// localDirMissing reports whether dir does not exist on this host.
func localDirMissing(dir string) bool {
	_, err := os.Stat(dir)
	return os.IsNotExist(err)
}

// normalizeIncludeDirs cleans the given paths and rejects those leaving the root.
// Nested entries are dropped, since their parent already covers them.
func normalizeIncludeDirs(dirs []string) ([]string, error) {
	cleaned := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		c := filepath.Clean(dir)
		if filepath.IsAbs(c) || c == "." || c == ".." || strings.HasPrefix(c, "../") {
			return nil, fmt.Errorf("include directory %q must be a path below the root", dir)
		}
		cleaned = append(cleaned, c)
	}
	result := make([]string, 0, len(cleaned))
	for _, dir := range cleaned {
		covered := false
		for _, other := range cleaned {
			if other != dir && strings.HasPrefix(dir, other+"/") {
				covered = true
				break
			}
		}
		if !covered && !containsString(result, dir) {
			result = append(result, dir)
		}
	}
	return result, nil
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// readIncludeFile reads one include directory per line; empty lines and lines starting
// with # are skipped.
func readIncludeFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var dirs []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		dirs = append(dirs, line)
	}
	return dirs, scanner.Err()
}
