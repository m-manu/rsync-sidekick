package service

import (
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
	"github.com/m-manu/rsync-sidekick/v2/lib"
)

// destMatchChunkSize is how many files one hashing step covers before its results are
// matched. Small enough that the first moves happen after seconds, large enough that the
// per-request overhead of a remote agent doesn't matter. A variable so tests can shrink it.
var destMatchChunkSize = 2_000

// DestMatcher matches orphans at source against candidates at destination while both
// sides are still being hashed. Digests arrive in chunks from either side; every orphan
// whose content a known candidate has gets its actions the moment both digests are known.
type DestMatcher struct {
	sourceDirPath, destDirPath    string
	sourceFiles, destinationFiles map[string]entity.FileMeta
	destFS                        rsfs.FileSystem
	copyDuplicates, useReflink    bool
	candidatesByDigest            map[entity.FileDigest][]string
	pendingOrphans                map[entity.FileDigest][]string // hashed, no candidate yet
	orphanDigests                 map[string]entity.FileDigest
	uniqueness, movedCandidates   set.Set[string]
	resolvedOrphans               set.Set[string]
	savings                       int64
	alwaysCreateParentDirs        bool
}

// AlwaysCreateParentDirs makes every move or copy come with a directory creation for its
// target's parent, for a destination whose directories can't be checked from here.
func (m *DestMatcher) AlwaysCreateParentDirs() {
	m.alwaysCreateParentDirs = true
}

func NewDestMatcher(sourceDirPath string, sourceFiles map[string]entity.FileMeta,
	destDirPath string, destinationFiles map[string]entity.FileMeta, destFS rsfs.FileSystem,
	copyDuplicates, useReflink bool,
) *DestMatcher {
	return &DestMatcher{
		sourceDirPath: sourceDirPath, destDirPath: destDirPath,
		sourceFiles: sourceFiles, destinationFiles: destinationFiles, destFS: destFS,
		copyDuplicates: copyDuplicates, useReflink: useReflink,
		candidatesByDigest: make(map[entity.FileDigest][]string),
		pendingOrphans:     make(map[entity.FileDigest][]string),
		orphanDigests:      make(map[string]entity.FileDigest),
		uniqueness:         set.NewThreadUnsafeSet[string](),
		movedCandidates:    set.NewThreadUnsafeSet[string](),
		resolvedOrphans:    set.NewThreadUnsafeSet[string](),
	}
}

// AddOrphans takes digests of orphans at source and returns the actions for those a
// known candidate can serve; the rest wait for candidates still to come.
func (m *DestMatcher) AddOrphans(digests map[string]entity.FileDigest) []action.SyncAction {
	var actions []action.SyncAction
	for _, orphan := range sortedKeys(digests) {
		digest := digests[orphan]
		if digest == (entity.FileDigest{}) {
			continue // hashing failed; rsync transfers the file
		}
		m.orphanDigests[orphan] = digest
		if candidates := m.candidatesByDigest[digest]; len(candidates) > 0 {
			actions = append(actions, m.actionsFor(orphan, candidates)...)
			continue
		}
		m.pendingOrphans[digest] = append(m.pendingOrphans[digest], orphan)
	}
	return actions
}

// AddCandidates takes digests of candidates at destination and returns the actions for
// waiting orphans they can serve.
func (m *DestMatcher) AddCandidates(digests map[string]entity.FileDigest) []action.SyncAction {
	var actions []action.SyncAction
	for _, candidate := range sortedKeys(digests) {
		digest := digests[candidate]
		if digest == (entity.FileDigest{}) {
			continue
		}
		m.candidatesByDigest[digest] = append(m.candidatesByDigest[digest], candidate)
	}
	for _, candidate := range sortedKeys(digests) {
		digest := digests[candidate]
		waiting := m.pendingOrphans[digest]
		if len(waiting) == 0 {
			continue
		}
		delete(m.pendingOrphans, digest)
		for _, orphan := range waiting {
			actions = append(actions, m.actionsFor(orphan, m.candidatesByDigest[digest])...)
		}
	}
	return actions
}

// OrphanDigests returns every orphan digest seen so far, for reuse by the archive scan.
func (m *DestMatcher) OrphanDigests() map[string]entity.FileDigest {
	return m.orphanDigests
}

// Savings is the size of all orphans an action took care of.
func (m *DestMatcher) Savings() int64 {
	return m.savings
}

func sortedKeys(digests map[string]entity.FileDigest) []string {
	keys := make([]string, 0, len(digests))
	for key := range digests {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// actionsFor builds the actions that serve one orphan from one of the candidates sharing
// its content: a timestamp fix on the candidate where needed, then a move if the
// candidate isn't needed at source and hasn't moved yet, otherwise a copy. Actions come
// in the order they have to run in.
func (m *DestMatcher) actionsFor(orphan string, candidates []string) []action.SyncAction {
	candidate := PickBestCandidate(candidates, orphan, m.sourceFiles)
	if candidate == "" {
		return nil
	}
	var actions []action.SyncAction
	add := func(a action.SyncAction) bool {
		if m.uniqueness.Contains(a.Uniqueness()) {
			return false
		}
		m.uniqueness.Add(a.Uniqueness())
		actions = append(actions, a)
		return true
	}
	orphanMeta := m.sourceFiles[orphan]
	_, candidateExistsAtSource := m.sourceFiles[candidate]

	// Timestamp propagation — skipped for already-moved candidates (no longer at that path)
	if !m.movedCandidates.Contains(candidate) &&
		m.destinationFiles[candidate].ModifiedTimestamp != orphanMeta.ModifiedTimestamp {
		// Leave a candidate alone that already matches its own counterpart at source
		if srcMeta, ok := m.sourceFiles[candidate]; !(ok && srcMeta == m.destinationFiles[candidate]) {
			if add(action.PropagateTimestampAction{
				SourceBaseDirPath:           m.sourceDirPath,
				DestinationBaseDirPath:      m.destDirPath,
				SourceFileRelativePath:      orphan,
				DestinationFileRelativePath: candidate,
				SourceModTime:               time.Unix(orphanMeta.ModifiedTimestamp, 0),
				FS:                          m.destFS,
			}) {
				m.resolve(orphan, orphanMeta, candidate == orphan)
			}
		}
	}
	if candidate == orphan {
		return actions
	}
	shouldMove := !candidateExistsAtSource && !m.movedCandidates.Contains(candidate)
	if !shouldMove && !m.copyDuplicates {
		return actions
	}
	m.addParentDir(orphan, add)
	if shouldMove {
		m.movedCandidates.Add(candidate)
		if add(action.MoveFileAction{
			BasePath: m.destDirPath, RelativeFromPath: candidate, RelativeToPath: orphan, FS: m.destFS,
		}) {
			m.resolve(orphan, orphanMeta, true)
		}
		return actions
	}
	// The candidate stays where it is (needed at source) or has moved already; a copy from
	// its old path is redirected when the action runs.
	if add(action.CopyFileAction{
		AbsSourcePath: filepath.Join(m.destDirPath, candidate),
		AbsDestPath:   filepath.Join(m.destDirPath, orphan),
		SourceModTime: time.Unix(orphanMeta.ModifiedTimestamp, 0),
		UseReflink:    m.useReflink,
	}) {
		m.resolve(orphan, orphanMeta, true)
	}
	return actions
}

func (m *DestMatcher) addParentDir(orphan string, add func(action.SyncAction) bool) {
	parentDir := filepath.Dir(filepath.Join(m.destDirPath, orphan))
	readable := false
	if m.alwaysCreateParentDirs {
		readable = false
	} else if m.destFS != nil {
		readable = m.destFS.IsReadableDirectory(parentDir)
	} else {
		readable = lib.IsReadableDirectory(parentDir)
	}
	if !readable {
		add(action.MakeDirectoryAction{AbsoluteDirPath: parentDir, FS: m.destFS})
	}
}

// resolve counts an orphan as served once; a timestamp fix only serves the orphan when it
// is aimed at the orphan's own path.
func (m *DestMatcher) resolve(orphan string, meta entity.FileMeta, served bool) {
	if served && !m.resolvedOrphans.Contains(orphan) {
		m.resolvedOrphans.Add(orphan)
		m.savings += meta.Size
	}
}

// ChunkDigestFunc hashes one chunk of files and returns their digests; files that cannot
// be hashed are simply missing from the result.
type ChunkDigestFunc func(chunk []string) (map[string]entity.FileDigest, error)

// StreamSyncActions hashes orphans and candidates chunk by chunk — both sides at the
// same time — and matches every chunk as soon as it is hashed. With onActions set, the
// actions of each chunk are handed over right away, so they can be applied while hashing
// goes on. All actions are returned as well; callers need them to know which orphans are
// taken care of.
func StreamSyncActions(m *DestMatcher, orphans, candidates []string,
	hashOrphans, hashCandidates ChunkDigestFunc, onActions func([]action.SyncAction) error,
) ([]action.SyncAction, error) {
	type chunkResult struct {
		orphans bool
		digests map[string]entity.FileDigest
		err     error
	}
	results := make(chan chunkResult)
	stop := make(chan struct{})
	var producers sync.WaitGroup
	produce := func(paths []string, hash ChunkDigestFunc, isOrphans bool) {
		defer producers.Done()
		for start := 0; start < len(paths); start += destMatchChunkSize {
			chunk := paths[start:min(start+destMatchChunkSize, len(paths))]
			digests, err := hash(chunk)
			select {
			case results <- chunkResult{orphans: isOrphans, digests: digests, err: err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}
	producers.Add(2)
	go produce(orphans, hashOrphans, true)
	go produce(candidates, hashCandidates, false)
	go func() {
		producers.Wait()
		close(results)
	}()

	var all []action.SyncAction
	fail := func(err error) ([]action.SyncAction, error) {
		close(stop)
		for range results { // let the producers finish their current chunk
		}
		return nil, err
	}
	for result := range results {
		if result.err != nil {
			side := "destination"
			if result.orphans {
				side = "source"
			}
			return fail(fmt.Errorf("error while hashing files at %s: %w", side, result.err))
		}
		var actions []action.SyncAction
		if result.orphans {
			actions = m.AddOrphans(result.digests)
		} else {
			actions = m.AddCandidates(result.digests)
		}
		if len(actions) == 0 {
			continue
		}
		all = append(all, actions...)
		if onActions != nil {
			if err := onActions(actions); err != nil {
				return fail(err)
			}
		}
	}
	return all, nil
}
