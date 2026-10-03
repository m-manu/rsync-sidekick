package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDigestCacheOptions_Sides(t *testing.T) {
	type sides struct{ source, dest bool }
	cases := []struct {
		name                       string
		opts                       digestCacheOptions
		sourceIsLocal, destIsLocal bool
		want                       sides
		warnings                   int
	}{
		{"default is off", digestCacheOptions{mode: "off"}, true, true, sides{false, false}, 0},
		{"bare flag is on", digestCacheOptions{mode: "on", modeSet: true}, true, false, sides{true, true}, 0},
		{"src only", digestCacheOptions{mode: "src", modeSet: true}, false, true, sides{true, false}, 0},
		{"dst only", digestCacheOptions{mode: "dst", modeSet: true}, false, true, sides{false, true}, 0},
		{"explicit off beats a path", digestCacheOptions{mode: "off", modeSet: true, localPath: "/c"},
			true, true, sides{false, false}, 1},
		{"local path alone: both sides when all is local", digestCacheOptions{mode: "off", localPath: "/c"},
			true, true, sides{true, true}, 0},
		{"local path alone: only the local side", digestCacheOptions{mode: "off", localPath: "/c"},
			false, true, sides{false, true}, 0},
		{"remote path alone: only the remote side", digestCacheOptions{mode: "off", remotePath: "/c"},
			false, true, sides{true, false}, 0},
		{"both paths: both sides", digestCacheOptions{mode: "off", localPath: "/a", remotePath: "/b"},
			true, false, sides{true, true}, 0},
		{"remote path without a remote side is ignored", digestCacheOptions{mode: "off", remotePath: "/c"},
			true, true, sides{false, false}, 1},
		{"mode turns off the side of a given path", digestCacheOptions{mode: "src", modeSet: true, remotePath: "/c"},
			true, false, sides{true, false}, 1},
	}
	for _, c := range cases {
		source, dest, warnings := c.opts.sides(c.sourceIsLocal, c.destIsLocal)
		assert.Equal(t, c.want, sides{source, dest}, c.name)
		assert.Len(t, warnings, c.warnings, "%s: %v", c.name, warnings)
	}
}
