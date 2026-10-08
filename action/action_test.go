package action

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestSortByDestinationDirCreatesDirectoryBeforeMovingIntoIt(t *testing.T) {
	const base = "/dest"
	var actions []SyncAction
	// Moves into new directories come first and their mkdir actions last, the
	// order in which they can be generated: the mkdir must still run first.
	for i := 0; i < 50; i++ {
		actions = append(actions, MoveFileAction{
			BasePath:         base,
			RelativeFromPath: fmt.Sprintf("old/%02d/file.mkv", i),
			RelativeToPath:   fmt.Sprintf("new/%02d/file.mkv", i),
		})
	}
	for i := 0; i < 50; i++ {
		actions = append(actions, MakeDirectoryAction{
			AbsoluteDirPath: fmt.Sprintf("%s/new/%02d", base, i),
		})
	}

	SortByDestinationDir(actions)

	created := map[string]bool{}
	for i, a := range actions {
		switch act := a.(type) {
		case MakeDirectoryAction:
			created[act.AbsoluteDirPath] = true
		case MoveFileAction:
			dir := filepath.Dir(act.destinationPath())
			if !created[dir] {
				t.Fatalf("action %d moves into %s before it is created", i, dir)
			}
		}
	}
}

func TestSortByDestinationDirKeepsEveryAction(t *testing.T) {
	actions := []SyncAction{
		MakeDirectoryAction{AbsoluteDirPath: "/dest/b/x"},
		MoveFileAction{BasePath: "/dest", RelativeFromPath: "a/1", RelativeToPath: "b/x/1"},
		MakeDirectoryAction{AbsoluteDirPath: "/dest/a/y"},
		MoveFileAction{BasePath: "/dest", RelativeFromPath: "b/2", RelativeToPath: "a/y/2"},
	}
	want := map[string]bool{}
	for _, a := range actions {
		want[a.Uniqueness()] = true
	}

	SortByDestinationDir(actions)

	if len(actions) != len(want) {
		t.Fatalf("got %d actions, want %d", len(actions), len(want))
	}
	for _, a := range actions {
		if !want[a.Uniqueness()] {
			t.Fatalf("unexpected action after sort: %s", a)
		}
		delete(want, a.Uniqueness())
	}
}
