package action

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// SyncAction is implemented by any action that propagates action at source to action at destination
type SyncAction interface {
	// sourcePath is path at source
	sourcePath() string
	// destinationPath is path at destination where an operation is to be performed
	destinationPath() string
	// UnixCommand must generate a unix command
	UnixCommand() string
	// Perform must perform the actual action
	Perform() error
	// Uniqueness should define a string that's unique with an action
	Uniqueness() string
}

// SortByDestinationDir sorts actions by destination directory path,
// grouping files in the same directory together for better cache locality.
// A directory's path sorts before the paths inside it, so a MakeDirectoryAction
// (keyed by its parent) runs before the moves and copies into that directory.
func SortByDestinationDir(actions []SyncAction) {
	keys := make([]string, len(actions))
	order := make([]int, len(actions))
	for i, a := range actions {
		keys[i] = filepath.Dir(a.destinationPath())
		order[i] = i
	}
	// Sort a permutation, not actions itself: the less function reads keys by
	// index, and keys would not follow the actions as they are swapped.
	sort.SliceStable(order, func(i, j int) bool {
		return keys[order[i]] < keys[order[j]]
	})
	sorted := make([]SyncAction, len(actions))
	for i, k := range order {
		sorted[i] = actions[k]
	}
	copy(actions, sorted)
}

const cmdSeparator = "\u0001"

// sanitizePath replaces C0/C1 control characters with their Unicode escape representation
// for safe terminal output. This prevents terminal escape injection from filenames
// containing characters like U+0090 (DCS).
func sanitizePath(path string) string {
	var b strings.Builder
	for _, r := range path {
		if unicode.IsControl(r) && r != '\t' {
			fmt.Fprintf(&b, "\\u%04X", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escape escapes the path for use in a unix command
func escape(path string) string {
	escaped := path
	escaped = strings.ReplaceAll(escaped, "\\", "\\\\") // This replace should be first
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	escaped = strings.ReplaceAll(escaped, "!", "\\!")
	escaped = strings.ReplaceAll(escaped, "`", "\\`")
	escaped = strings.ReplaceAll(escaped, "$", "\\$")
	return escaped
}
