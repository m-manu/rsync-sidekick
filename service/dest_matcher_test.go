package service

import (
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func digestFor(hash string) entity.FileDigest {
	return entity.FileDigest{FileExtension: "bin", FileSize: 10, FileFuzzyHash: hash}
}

// newTestMatcher builds a matcher over a real (empty) destination directory, so parent
// directory checks see what a run would see.
func newTestMatcher(t *testing.T, sourceFiles, destinationFiles map[string]entity.FileMeta, copyDuplicates bool) (*DestMatcher, string) {
	t.Helper()
	dst := t.TempDir()
	return NewDestMatcher("/src", sourceFiles, dst, destinationFiles, nil, copyDuplicates, false), dst
}

func actionKinds(actions []action.SyncAction) []string {
	kinds := make([]string, 0, len(actions))
	for _, a := range actions {
		switch a.(type) {
		case action.MoveFileAction:
			kinds = append(kinds, "mv")
		case action.CopyFileAction:
			kinds = append(kinds, "cp")
		case action.PropagateTimestampAction:
			kinds = append(kinds, "ts")
		case action.MakeDirectoryAction:
			kinds = append(kinds, "mkdir")
		}
	}
	return kinds
}

func TestDestMatcher_OrphanWaitsForItsCandidate(t *testing.T) {
	m, _ := newTestMatcher(t,
		map[string]entity.FileMeta{"new.bin": {Size: 10, ModifiedTimestamp: 1}},
		map[string]entity.FileMeta{"old.bin": {Size: 10, ModifiedTimestamp: 1}}, false)

	assert.Empty(t, m.AddOrphans(map[string]entity.FileDigest{"new.bin": digestFor("a")}),
		"no candidate known yet")
	actions := m.AddCandidates(map[string]entity.FileDigest{"old.bin": digestFor("a")})

	require.Equal(t, []string{"mv"}, actionKinds(actions))
	move := actions[0].(action.MoveFileAction)
	assert.Equal(t, "old.bin", move.RelativeFromPath)
	assert.Equal(t, "new.bin", move.RelativeToPath)
	assert.Equal(t, int64(10), m.Savings())
}

func TestDestMatcher_CandidateFirstThenOrphanWithTimestampBeforeMove(t *testing.T) {
	m, _ := newTestMatcher(t,
		map[string]entity.FileMeta{"sub/new.bin": {Size: 10, ModifiedTimestamp: 2}},
		map[string]entity.FileMeta{"old.bin": {Size: 10, ModifiedTimestamp: 1}}, false)

	assert.Empty(t, m.AddCandidates(map[string]entity.FileDigest{"old.bin": digestFor("a")}))
	actions := m.AddOrphans(map[string]entity.FileDigest{"sub/new.bin": digestFor("a")})

	assert.Equal(t, []string{"ts", "mkdir", "mv"}, actionKinds(actions),
		"actions come in the order they have to run in")
}

func TestDestMatcher_SecondOrphanOfAMovedCandidateIsCopiedOnlyWithCopyDuplicates(t *testing.T) {
	source := map[string]entity.FileMeta{"a.bin": {Size: 10}, "b.bin": {Size: 10}}
	destination := map[string]entity.FileMeta{"x.bin": {Size: 10}}
	orphans := map[string]entity.FileDigest{"a.bin": digestFor("a"), "b.bin": digestFor("a")}
	candidates := map[string]entity.FileDigest{"x.bin": digestFor("a")}

	withCopies, dst := newTestMatcher(t, source, destination, true)
	withCopies.AddCandidates(candidates)
	actions := withCopies.AddOrphans(orphans)
	assert.Equal(t, []string{"mv", "cp"}, actionKinds(actions))
	copyAction := actions[1].(action.CopyFileAction)
	assert.Equal(t, filepath.Join(dst, "x.bin"), copyAction.AbsSourcePath,
		"the copy names the old path; the applier redirects it to where the move put the file")

	movesOnly, _ := newTestMatcher(t, source, destination, false)
	movesOnly.AddCandidates(candidates)
	assert.Equal(t, []string{"mv"}, actionKinds(movesOnly.AddOrphans(orphans)))
}

func TestDestMatcher_CandidateNeededAtSourceIsCopiedNeverMoved(t *testing.T) {
	m, _ := newTestMatcher(t,
		map[string]entity.FileMeta{"new.bin": {Size: 10}, "keep.bin": {Size: 10}},
		map[string]entity.FileMeta{"keep.bin": {Size: 10}}, true)

	m.AddCandidates(map[string]entity.FileDigest{"keep.bin": digestFor("a")})
	actions := m.AddOrphans(map[string]entity.FileDigest{"new.bin": digestFor("a")})

	assert.Equal(t, []string{"cp"}, actionKinds(actions))
}

func TestDestMatcher_FailedDigestsNeverMatch(t *testing.T) {
	m, _ := newTestMatcher(t, map[string]entity.FileMeta{"a.bin": {Size: 10}},
		map[string]entity.FileMeta{"x.bin": {Size: 10}}, true)

	m.AddCandidates(map[string]entity.FileDigest{"x.bin": {}})
	assert.Empty(t, m.AddOrphans(map[string]entity.FileDigest{"a.bin": {}}))
	assert.Empty(t, m.OrphanDigests())
}

func TestStreamSyncActions_AppliesChunksWhileHashingGoesOn(t *testing.T) {
	old := destMatchChunkSize
	destMatchChunkSize = 1
	t.Cleanup(func() { destMatchChunkSize = old })

	source := map[string]entity.FileMeta{}
	destination := map[string]entity.FileMeta{}
	var orphans, candidates []string
	for _, name := range []string{"1", "2", "3", "4"} {
		source["new"+name+".bin"] = entity.FileMeta{Size: 10}
		destination["old"+name+".bin"] = entity.FileMeta{Size: 10}
		orphans = append(orphans, "new"+name+".bin")
		candidates = append(candidates, "old"+name+".bin")
	}
	m, _ := newTestMatcher(t, source, destination, false)
	firstApplied := make(chan struct{})
	var orphanChunks atomic.Int32
	// From the second chunk on, hashing orphans waits for the first applied actions: if
	// actions only came at the end, this would time out instead.
	hashOrphans := func(chunk []string) (map[string]entity.FileDigest, error) {
		if orphanChunks.Add(1) > 1 {
			select {
			case <-firstApplied:
			case <-time.After(5 * time.Second):
				return nil, errors.New("no actions applied while hashing")
			}
		}
		return map[string]entity.FileDigest{chunk[0]: digestFor(chunk[0][3:4])}, nil
	}
	hashCandidates := func(chunk []string) (map[string]entity.FileDigest, error) {
		return map[string]entity.FileDigest{chunk[0]: digestFor(chunk[0][3:4])}, nil
	}
	var appliedBatches int
	onActions := func(actions []action.SyncAction) error {
		if appliedBatches == 0 {
			close(firstApplied)
		}
		appliedBatches++
		return nil
	}

	all, err := StreamSyncActions(m, orphans, candidates, hashOrphans, hashCandidates, onActions)

	require.NoError(t, err)
	assert.Equal(t, []string{"mv", "mv", "mv", "mv"}, actionKinds(all))
	assert.Greater(t, appliedBatches, 1, "actions are handed over per chunk, not once at the end")
}

func TestStreamSyncActions_HashingErrorStopsWithoutHanging(t *testing.T) {
	old := destMatchChunkSize
	destMatchChunkSize = 1
	t.Cleanup(func() { destMatchChunkSize = old })
	m, _ := newTestMatcher(t, map[string]entity.FileMeta{}, map[string]entity.FileMeta{}, false)
	failing := func([]string) (map[string]entity.FileDigest, error) { return nil, errors.New("agent gone") }
	endless := func(chunk []string) (map[string]entity.FileDigest, error) { return map[string]entity.FileDigest{}, nil }

	_, err := StreamSyncActions(m, []string{"a", "b"}, []string{"x", "y", "z"}, failing, endless, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent gone")
}
