//go:build windows

package service

import "os"

// hardlinkIdentity is not available here; every file is hashed on its own.
func hardlinkIdentity(os.FileInfo) (fileIdentity, bool) {
	return fileIdentity{}, false
}
