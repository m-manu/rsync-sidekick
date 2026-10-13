package lib

import (
	"os"
	"path/filepath"
	"strings"
)

// HasGlobMeta reports whether path holds shell wildcards (*, ?, [...]).
func HasGlobMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

// GlobDirs returns the directories matching pattern, in lexical order. Files matching
// the pattern are left out: only directories can be scanned.
func GlobDirs(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(matches))
	for _, match := range matches {
		if info, statErr := os.Stat(match); statErr == nil && info.IsDir() {
			dirs = append(dirs, match)
		}
	}
	return dirs, nil
}

// GlobDirsAll is GlobDirs for several patterns at once, one result per pattern.
func GlobDirsAll(patterns []string) ([][]string, error) {
	results := make([][]string, len(patterns))
	for i, pattern := range patterns {
		dirs, err := GlobDirs(pattern)
		if err != nil {
			return nil, err
		}
		results[i] = dirs
	}
	return results, nil
}

// ExpandPaths replaces every path holding wildcards by the directories it matches, keeping
// the order of paths and, within a pattern, lexical order. A path that appears twice is
// kept at its first position. glob is only called when some path holds a wildcard, so
// plain paths never depend on it. Patterns matching nothing are returned in unmatched.
func ExpandPaths(paths []string, glob func(patterns []string) ([][]string, error),
) (expanded []string, unmatched []string, err error) {
	var patterns []string
	for _, path := range paths {
		if HasGlobMeta(path) {
			patterns = append(patterns, path)
		}
	}
	matchesByPattern := make(map[string][]string, len(patterns))
	if len(patterns) > 0 {
		results, globErr := glob(patterns)
		if globErr != nil {
			return nil, nil, globErr
		}
		for i, pattern := range patterns {
			if i < len(results) {
				matchesByPattern[pattern] = results[i]
			}
		}
	}
	seen := make(map[string]bool, len(paths))
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			expanded = append(expanded, path)
		}
	}
	for _, path := range paths {
		if !HasGlobMeta(path) {
			add(path)
			continue
		}
		matches := matchesByPattern[path]
		if len(matches) == 0 {
			unmatched = append(unmatched, path)
		}
		for _, match := range matches {
			add(match)
		}
	}
	return expanded, unmatched, nil
}
