package main

import (
	"github.com/m-manu/rsync-sidekick/v2/fmte"
	"github.com/m-manu/rsync-sidekick/v2/lib"
)

// progressBoard shows the progress of all phases that run at the same time in one line.
var progressBoard = lib.NewProgressBoard(func(line string) { fmte.Printf("%s...\n", line) })
