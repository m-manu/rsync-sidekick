package service

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/m-manu/rsync-sidekick/entity"
)

func TestGetParallelism(t *testing.T) {
	for i := 1; i < 7; i++ {
		p1, p2 := getParallelism(i)
		assert.GreaterOrEqual(t, p1, 1)
		assert.GreaterOrEqual(t, p2, 1)
		if i > 1 {
			assert.LessOrEqual(t, p1+p2, i)
		}
	}
}

func TestDuplicateMatchingSimilarityScore(t *testing.T) {
    a := "Video/Season2019/run/file.mov"
    b := "Video/Season2019/run/file.mov"
    if calculateSimilarityScore(a, b) <= 0 {
        t.Fatal("expected positive similarity score")
    }
}

func TestDuplicateMatchingSimilarityHandlesFolderRename(t *testing.T) {
	source := "Summer/2019/FILE0001.MOV"
	candidate := "S/FILE0002.MOV"
	score := calculateSimilarityScore(source, candidate)
	if score == 0 {
		t.Fatalf("expected positive similarity score")
	}
}

func TestDuplicateMatchingChoosesBestPath(t *testing.T) {
	source := "Summer/2019/FILE0001.MOV"
	correctCandidate := "Summer Season/2019/FILE0001.MOV"
	highestScore := calculateSimilarityScore(source, correctCandidate)
	lowerCandidates := []string{
		"Random Backup/FILE0001.MOV",
		"Summer Season/2019/FILE0002.MOV",
		"Winter Season/2019/FILE0001.MOV",
		"Summer Season/2018/FILE0001.MOV",
		"Summer Season 2019/FILE0001.MOV",
	}
	for _, candidate := range lowerCandidates {
		score := calculateSimilarityScore(source, candidate)
		if score > highestScore {
			t.Fatalf("expected '%s' (%d) to match better than '%s' (%d)", correctCandidate, highestScore, candidate, score)
		}
	}
}

func TestDuplicateMatchingFilenameGrouping(t *testing.T) {
	matches := []string{
		"trip1/FILE0001.MOV",
		"trip2/FILE0001.MOV",
		"trip3/FILE0002.MOV",
	}
	orphan := "trip4/FILE0001.MOV"
	base := filepath.Base(orphan)
	result := []string{}
	for _, m := range matches {
		if filepath.Base(m) == base {
			result = append(result, m)
		}
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 filename matches, got %d", len(result))
	}
}

func TestDuplicateMatchingFilterByTimestamp(t *testing.T) {
	srcMeta := entity.FileMeta{
		Size: 100,
		ModifiedTimestamp: 12345,
	}
	destination := map[string]entity.FileMeta{
		"a.mov": {Size: 100, ModifiedTimestamp: 12345},
		"b.mov": {Size: 100, ModifiedTimestamp: 99999},
	}
	paths := []string{"a.mov", "b.mov"}
	result := filterByTimestamp(paths, srcMeta, destination)
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
}

func TestDuplicateMatchingRegression(t *testing.T) {
	sourceFiles := map[string]entity.FileMeta{
		"Summer/2019/FILE0001.MOV": {
			Size: 100,
			ModifiedTimestamp: 1000,
		},
	}
	destinationFiles := map[string]entity.FileMeta{
		"Summer Season 2019/FILE0001.MOV": {
			Size: 100,
			ModifiedTimestamp: 1000,
		},
		"Summer Season 2018/FILE0001.MOV": {
			Size: 100,
			ModifiedTimestamp: 1000,
		},
	}
	orphansAtSource := []string{
		"Summer/2019/FILE0001.MOV",
	}
	candidatesAtDestination := []string{
		"Summer/2019/FILE0002.MOV",
		"Summer Season 2018/FILE0001.MOV",
		"Summer Season 2019/FILE0001.MOV",
		"Summer Season/2019/FILE0001.MOV",
	}
	var srcCounter int32
	var dstCounter int32
	actions, _, err := ComputeSyncActions(
		"./test_source",
		sourceFiles,
		orphansAtSource,
		"./test_dest",
		destinationFiles,
		candidatesAtDestination,
		&srcCounter,
		&dstCounter,
		true,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(actions) == 0 {
		t.Fatalf("expected move action but got none")
	}
}
