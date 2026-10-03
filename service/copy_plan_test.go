package service

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func digestOf(size int64, hash string) entity.FileDigest {
	return entity.FileDigest{FileExtension: "mkv", FileSize: size, FileFuzzyHash: hash}
}

func TestBuildCopyPlan_GroupsDuplicatesAndListsOneOriginalEach(t *testing.T) {
	sourceFiles := map[string]entity.FileMeta{
		"b/movie.mkv":  {Size: 100, ModifiedTimestamp: 2},
		"a/movie.mkv":  {Size: 100, ModifiedTimestamp: 1},
		"c/movie.mkv":  {Size: 100, ModifiedTimestamp: 3},
		"d/other.mkv":  {Size: 100, ModifiedTimestamp: 4},
		"e/single.mkv": {Size: 555, ModifiedTimestamp: 5},
	}
	digests := map[string]entity.FileDigest{
		"a/movie.mkv": digestOf(100, "s1"),
		"b/movie.mkv": digestOf(100, "s1"),
		"c/movie.mkv": digestOf(100, "s1"),
		"d/other.mkv": digestOf(100, "s2"),
	}
	var requested []string
	digestFn := func(orphans []string) (map[string]entity.FileDigest, error) {
		requested = append(requested, orphans...)
		result := make(map[string]entity.FileDigest)
		for _, o := range orphans {
			result[o] = digests[o]
		}
		return result, nil
	}

	copyList, groups, err := BuildCopyPlan(
		[]string{"b/movie.mkv", "a/movie.mkv", "c/movie.mkv", "d/other.mkv", "e/single.mkv"},
		sourceFiles, nil, digestFn)
	require.NoError(t, err)

	assert.Equal(t, []string{"a/movie.mkv", "d/other.mkv", "e/single.mkv"}, copyList)
	assert.NotContains(t, requested, "e/single.mkv", "a file without a same-size partner needs no digest")
	require.Len(t, groups, 1, "groups: %+v", groups)
	assert.Equal(t, PlanGroup{
		Digest: "s1", Size: 100,
		Original: PlanFile{Path: "a/movie.mkv", ModTime: 1},
		Targets:  []PlanFile{{Path: "b/movie.mkv", ModTime: 2}, {Path: "c/movie.mkv", ModTime: 3}},
	}, groups[0])
}

func TestBuildCopyPlan_UsesKnownDigestsAndTransfersUnhashableFiles(t *testing.T) {
	sourceFiles := map[string]entity.FileMeta{
		"x.mkv":      {Size: 10},
		"y.mkv":      {Size: 10},
		"broken.mkv": {Size: 10},
		"empty1":     {Size: 0},
		"empty2":     {Size: 0},
	}
	known := map[string]entity.FileDigest{"x.mkv": digestOf(10, "s9"), "y.mkv": digestOf(10, "s9")}
	digestFn := func(orphans []string) (map[string]entity.FileDigest, error) {
		assert.Equal(t, []string{"broken.mkv"}, orphans, "only unknown digests are requested")
		return map[string]entity.FileDigest{}, nil // hashing failed
	}

	copyList, groups, err := BuildCopyPlan([]string{"x.mkv", "y.mkv", "broken.mkv", "empty1", "empty2"},
		sourceFiles, known, digestFn)
	require.NoError(t, err)

	assert.Equal(t, []string{"broken.mkv", "empty1", "empty2", "x.mkv"}, copyList)
	require.Len(t, groups, 1)
	assert.Equal(t, "x.mkv", groups[0].Original.Path)
	assert.Equal(t, []PlanFile{{Path: "y.mkv"}}, groups[0].Targets)
}

func TestWritePlan_ReadPlanRoundTripWithShortKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.jsonl")
	groups := []PlanGroup{{Digest: "s1", Size: 7, Original: PlanFile{Path: "a b/ü.txt", ModTime: 11},
		Targets: []PlanFile{{Path: "c\"d.txt", ModTime: 12}}}}

	require.NoError(t, WritePlan(path, groups))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"dg":"s1","sz":7,"o":{"p":"a b/ü.txt","mt":11},"t":[{"p":"c\"d.txt","mt":12}]}`+"\n", string(raw))

	read, err := ReadPlan(path)
	require.NoError(t, err)
	assert.Equal(t, groups, read)
}

func writeFileWithMtime(t *testing.T, path, content string, mtime int64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o640))
	require.NoError(t, os.Chtimes(path, time.Unix(mtime, 0), time.Unix(mtime, 0)))
}

func TestApplyPlan_ReflinksTargetsAndSkipsWhatDoesNotFit(t *testing.T) {
	base := t.TempDir()
	writeFileWithMtime(t, filepath.Join(base, "orig/ok.bin"), "hello", 1000)
	writeFileWithMtime(t, filepath.Join(base, "orig/changed.bin"), "hello", 2000)
	writeFileWithMtime(t, filepath.Join(base, "dup/exists.bin"), "other", 3000)

	groups := []PlanGroup{
		{Digest: "s1", Size: 5, Original: PlanFile{Path: "orig/ok.bin", ModTime: 1000},
			Targets: []PlanFile{{Path: "dup/new/ok.bin", ModTime: 1500}, {Path: "dup/exists.bin", ModTime: 1}}},
		{Digest: "s2", Size: 5, Original: PlanFile{Path: "orig/changed.bin", ModTime: 1999},
			Targets: []PlanFile{{Path: "dup/changed.bin", ModTime: 1}}},
		{Digest: "s3", Size: 5, Original: PlanFile{Path: "orig/missing.bin", ModTime: 1},
			Targets: []PlanFile{{Path: "dup/missing.bin", ModTime: 1}}},
	}

	stats := ApplyPlan(groups, base, false, 2, 0)

	assert.Equal(t, ApplyPlanStats{GroupsDone: 1, GroupsSkipped: 2, TargetsDone: 1, TargetsExisted: 1, BytesSaved: 5}, stats)
	content, err := os.ReadFile(filepath.Join(base, "dup/new/ok.bin"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(content))
	info, err := os.Stat(filepath.Join(base, "dup/new/ok.bin"))
	require.NoError(t, err)
	assert.Equal(t, int64(1500), info.ModTime().Unix(), "target gets its own mtime from the plan")
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "mode comes from the original")
	existing, _ := os.ReadFile(filepath.Join(base, "dup/exists.bin"))
	assert.Equal(t, "other", string(existing), "an existing target is never overwritten")
	assert.NoFileExists(t, filepath.Join(base, "dup/changed.bin"))
	assert.NoFileExists(t, filepath.Join(base, "dup/missing.bin"))
}

func TestApplyPlan_DryRunChangesNothing(t *testing.T) {
	base := t.TempDir()
	writeFileWithMtime(t, filepath.Join(base, "o.bin"), "abc", 10)
	groups := []PlanGroup{{Size: 3, Original: PlanFile{Path: "o.bin", ModTime: 10},
		Targets: []PlanFile{{Path: "t.bin", ModTime: 10}}}}

	stats := ApplyPlan(groups, base, true, 1, 0)

	assert.Equal(t, int64(1), stats.TargetsDone)
	assert.NoFileExists(t, filepath.Join(base, "t.bin"))
	entries, _ := os.ReadDir(base)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.True(t, slices.Equal([]string{"o.bin"}, names), "entries: %v", names)
}
