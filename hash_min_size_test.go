package main

import (
	"os"
	"path/filepath"
	"testing"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withHashMinSize(t *testing.T, size int64) {
	t.Helper()
	old := hashMinSize
	hashMinSize = size
	t.Cleanup(func() { hashMinSize = old })
}

func TestHashableOrphans_LeavesOutSmallFilesAndSorts(t *testing.T) {
	files := map[string]entity.FileMeta{"b": {Size: 100}, "a": {Size: 100}, "tiny": {Size: 99}}

	withHashMinSize(t, 100)
	assert.Equal(t, []string{"a", "b"}, hashableOrphans([]string{"b", "tiny", "a"}, files))

	withHashMinSize(t, 0)
	assert.Equal(t, []string{"a", "b", "tiny"}, hashableOrphans([]string{"b", "tiny", "a"}, files))
}

func TestHashMinSize_SmallFilesAreNotHashedButLargeOnesAre(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	big := string(make([]byte, 40_000)) + "big renamed file"
	writeSame(t, big, filepath.Join(src, "big-new.bin"), filepath.Join(dst, "big-old.bin"))
	writeSame(t, "small renamed file", filepath.Join(src, "small-new.bin"), filepath.Join(dst, "small-old.bin"))
	withHashMinSize(t, 1024)

	actions, err := getSyncActionsWithProgress("test", src, set.NewSet[string](), dst, false, 0, false, false, nil, nil)

	require.NoError(t, err)
	var moves []string
	for _, a := range actions {
		if move, ok := a.(action.MoveFileAction); ok {
			moves = append(moves, move.RelativeFromPath+"->"+move.RelativeToPath)
		}
	}
	assert.Equal(t, []string{"big-old.bin->big-new.bin"}, moves, "the small file is left to rsync")
}

func TestWriteCopyPlan_SmallFilesGoStraightIntoTheListWithoutHashing(t *testing.T) {
	dir := t.TempDir()
	plan := copyPlanOutput{CopyListPath: filepath.Join(dir, "copy.txt"), PlanPath: filepath.Join(dir, "plan.jsonl")}
	sourceFiles := map[string]entity.FileMeta{
		"tiny1.txt": {Size: 10}, "tiny2.txt": {Size: 10}, // same size, would be hashed without the limit
		"big1.bin": {Size: 5000}, "big2.bin": {Size: 5000},
	}
	digest := entity.FileDigest{FileExtension: "bin", FileSize: 5000, FileFuzzyHash: "s1"}
	digestFn := func(paths []string) (map[string]entity.FileDigest, error) {
		for _, p := range paths {
			assert.NotContains(t, p, "tiny", "small files must never be hashed")
		}
		return map[string]entity.FileDigest{"big1.bin": digest, "big2.bin": digest}, nil
	}
	withHashMinSize(t, 1000)

	err := writeCopyPlan(plan, []string{"tiny1.txt", "tiny2.txt", "big1.bin", "big2.bin"}, nil,
		set.NewSet[string](), "/dst", sourceFiles, nil, digestFn)

	require.NoError(t, err)
	list, _ := os.ReadFile(plan.CopyListPath)
	assert.Equal(t, "big1.bin\ntiny1.txt\ntiny2.txt\n", string(list))
	planLines, _ := os.ReadFile(plan.PlanPath)
	assert.Contains(t, string(planLines), `"t":[{"p":"big2.bin"`)
}
