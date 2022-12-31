package action

import "strings"

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

const cmdSeparator = "\u0001"

func escape(path string) string {
	escaped := path
	escaped = strings.ReplaceAll(escaped, "\\", "\\\\") // This replace should be first
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	escaped = strings.ReplaceAll(escaped, "!", "\\!")
	escaped = strings.ReplaceAll(escaped, "`", "\\`")
	escaped = strings.ReplaceAll(escaped, "$", "\\$")
	return escaped
}
