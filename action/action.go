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

// SortByDestinationDir sorts actions by destination directory path, grouping files in the
// same directory together for better cache locality. Directory creations come first, so a
// copy or move always finds its target directory in place.
//
// The key has to travel with its action: computing keys into a separate slice and letting
// sort swap only the actions leaves the comparison reading keys of unrelated entries, and
// the result is an arbitrary permutation rather than a sorted list.
func SortByDestinationDir(actions []SyncAction) {
	type keyedAction struct {
		directoriesFirst int
		destinationDir   string
		action           SyncAction
	}
	items := make([]keyedAction, len(actions))
	for i, a := range actions {
		rank := 1
		if _, isMkdir := a.(MakeDirectoryAction); isMkdir {
			rank = 0
		}
		items[i] = keyedAction{
			directoriesFirst: rank,
			destinationDir:   filepath.Dir(a.destinationPath()),
			action:           a,
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].directoriesFirst != items[j].directoriesFirst {
			return items[i].directoriesFirst < items[j].directoriesFirst
		}
		return items[i].destinationDir < items[j].destinationDir
	})
	for i := range items {
		actions[i] = items[i].action
	}
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
