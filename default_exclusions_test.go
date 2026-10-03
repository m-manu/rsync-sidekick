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

func TestDefaultExclusionList_KeepsFileOrderAndSkipsBlankLines(t *testing.T) {
	assert.Equal(t, []string{"a", "b c", "d"}, defaultExclusionList("a\n\n b c \nd"))
}

func TestWrapList(t *testing.T) {
	names := []string{"alpha", "beta", "gamma", "a-very-long-name-on-its-own"}
	assert.Equal(t, "alpha, beta,\ngamma,\na-very-long-name-on-its-own", wrapList(names, 12))
	assert.Equal(t, "alpha, beta, gamma, a-very-long-name-on-its-own", wrapList(names, 100))
	assert.Equal(t, "", wrapList(nil, 10))
}

func TestHelpListsEveryDefaultExclusion(t *testing.T) {
	help := wrapList(defaultExclusionList(defaultExclusionsStr), 100)
	defaults, _ := lib.LineSeparatedStrToMap(defaultExclusionsStr)
	for _, name := range defaults.ToSlice() {
		assert.Contains(t, help, name)
	}
}
