package main

import (
	"testing"

	"github.com/m-manu/rsync-sidekick/v2/lib"
	"github.com/stretchr/testify/assert"
)

func TestDefaultExclusions_IgnoreSyncthingVersions(t *testing.T) {
	// Each Syncthing client keeps its own .stversions; syncing them between hosts makes no sense.
	defaults, _ := lib.LineSeparatedStrToMap(defaultExclusionsStr)
	assert.True(t, defaults.Contains(".stversions"), "defaults: %v", defaults)
	assert.True(t, defaults.Contains("System Volume Information"), "the entry before it stays intact: %v", defaults)
}
