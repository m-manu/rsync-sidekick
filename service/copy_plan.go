package service

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/m-manu/rsync-sidekick/v2/fmte"
	"github.com/m-manu/rsync-sidekick/v2/lib"
)

// PlanFile is one file of a copy plan group. Keys are short on purpose: a plan for a
// large tree has millions of lines.
type PlanFile struct {
	Path    string `json:"p"`
	ModTime int64  `json:"mt"`
}

// PlanGroup is one line of a copy plan: files at source sharing the same content. The
// original is transferred by rsync, every target is then reflinked from it locally.
type PlanGroup struct {
	Digest   string     `json:"dg"`
	Size     int64      `json:"sz"`
	Original PlanFile   `json:"o"`
	Targets  []PlanFile `json:"t"`
}

// BuildCopyPlan splits the orphans nothing at destination could serve into the files
// that have to cross the network (copyList, one per distinct content) and the duplicate
// groups that can be reflinked from those once they arrived.
//
// Only orphans sharing size (and extension, unless extensions are ignored) with another
// orphan can be duplicates, so only those are hashed: knownDigests first, digestFn for
// the rest. An orphan whose digest stays unknown is transferred on its own.
func BuildCopyPlan(unresolvedOrphans []string, sourceFiles map[string]entity.FileMeta,
	knownDigests map[string]entity.FileDigest, digestFn OrphanDigestFunc,
) (copyList []string, groups []PlanGroup, err error) {
	type sizeKey struct {
		ext  string
		size int64
	}
	bySize := make(map[sizeKey][]string)
	for _, orphan := range unresolvedOrphans {
		size := sourceFiles[orphan].Size
		if size == 0 {
			copyList = append(copyList, orphan) // nothing to save on an empty file
			continue
		}
		k := sizeKey{ext: lib.GetFileExt(orphan), size: size}
		bySize[k] = append(bySize[k], orphan)
	}

	var needDigest []string
	for _, orphans := range bySize {
		if len(orphans) == 1 {
			copyList = append(copyList, orphans[0])
			continue
		}
		for _, orphan := range orphans {
			if _, known := knownDigests[orphan]; !known {
				needDigest = append(needDigest, orphan)
			}
		}
	}
	digests := make(map[string]entity.FileDigest, len(knownDigests)+len(needDigest))
	for path, digest := range knownDigests {
		digests[path] = digest
	}
	if len(needDigest) > 0 && digestFn != nil {
		slices.Sort(needDigest)
		fresh, digestErr := digestFn(needDigest)
		if digestErr != nil {
			return nil, nil, fmt.Errorf("error computing digests of %s duplicate candidate(s): %w",
				lib.GroupThousands(len(needDigest)), digestErr)
		}
		for path, digest := range fresh {
			digests[path] = digest
		}
	}

	for _, orphans := range bySize {
		if len(orphans) == 1 {
			continue
		}
		byDigest := make(map[entity.FileDigest][]string)
		for _, orphan := range orphans {
			digest, ok := digests[orphan]
			if !ok || digest == (entity.FileDigest{}) {
				copyList = append(copyList, orphan)
				continue
			}
			byDigest[digest] = append(byDigest[digest], orphan)
		}
		for digest, paths := range byDigest {
			slices.Sort(paths)
			copyList = append(copyList, paths[0])
			if len(paths) == 1 {
				continue
			}
			group := PlanGroup{
				Digest:   digest.FileFuzzyHash,
				Size:     digest.FileSize,
				Original: PlanFile{Path: paths[0], ModTime: sourceFiles[paths[0]].ModifiedTimestamp},
			}
			for _, target := range paths[1:] {
				group.Targets = append(group.Targets,
					PlanFile{Path: target, ModTime: sourceFiles[target].ModifiedTimestamp})
			}
			groups = append(groups, group)
		}
	}

	slices.Sort(copyList)
	slices.SortFunc(groups, func(a, b PlanGroup) int {
		if a.Original.Path < b.Original.Path {
			return -1
		}
		if a.Original.Path > b.Original.Path {
			return 1
		}
		return 0
	})
	return copyList, groups, nil
}

// WriteCopyList writes one path per line, ready for rsync --files-from.
func WriteCopyList(path string, copyList []string) error {
	return writeLines(path, len(copyList), func(w *bufio.Writer, i int) error {
		_, err := w.WriteString(copyList[i] + "\n")
		return err
	})
}

// WritePlan writes one JSON object per line, one line per duplicate group.
func WritePlan(path string, groups []PlanGroup) error {
	return writeLines(path, len(groups), func(w *bufio.Writer, i int) error {
		line, err := json.Marshal(groups[i])
		if err != nil {
			return err
		}
		line = append(line, '\n')
		_, err = w.Write(line)
		return err
	})
}

func writeLines(path string, n int, writeLine func(*bufio.Writer, int) error) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("cannot create %q: %w", path, err)
	}
	w := bufio.NewWriter(file)
	for i := 0; i < n; i++ {
		if err := writeLine(w, i); err != nil {
			file.Close()
			return fmt.Errorf("cannot write %q: %w", path, err)
		}
	}
	if err := w.Flush(); err != nil {
		file.Close()
		return fmt.Errorf("cannot write %q: %w", path, err)
	}
	return file.Close()
}

// ReadPlan reads a plan written by WritePlan.
func ReadPlan(path string) ([]PlanGroup, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open plan %q: %w", path, err)
	}
	defer file.Close()
	var groups []PlanGroup
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 256*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var group PlanGroup
		if err := json.Unmarshal(scanner.Bytes(), &group); err != nil {
			return nil, fmt.Errorf("plan %q line %d: %w", path, lineNo, err)
		}
		groups = append(groups, group)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cannot read plan %q: %w", path, err)
	}
	return groups, nil
}

// ApplyPlanStats counts what ApplyPlan did. Groups are skipped as a whole when their
// original at destination is missing or differs from what the plan recorded.
type ApplyPlanStats struct {
	GroupsDone     int64
	GroupsSkipped  int64
	TargetsDone    int64
	TargetsExisted int64
	TargetsFailed  int64
	BytesSaved     int64
}

// ApplyPlan reflinks every target of every group from its original below baseDir. The
// original must exist with the recorded size and modification time — rsync -a carries
// the mtime over, so a mismatch means the source changed after the plan was made.
// Existing targets are left alone, so an interrupted run can simply be repeated.
func ApplyPlan(groups []PlanGroup, baseDir string, dryRun bool, parallelism int,
	progressFrequency time.Duration,
) ApplyPlanStats {
	var stats ApplyPlanStats
	var next atomic.Int64
	stop := make(chan struct{})
	if progressFrequency > 0 {
		go func() {
			progress := lib.NewProgress("Applying plan", time.Now())
			ticker := time.NewTicker(progressFrequency)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					fmte.Printf("%s...\n", progress.Line(time.Now(),
						lib.ProgressPart{Count: min(next.Load(), int64(len(groups))), Total: int64(len(groups)),
							Label: "groups"},
						lib.ProgressPart{Count: atomic.LoadInt64(&stats.TargetsDone), Label: "reflinked"},
						lib.ProgressPart{Count: atomic.LoadInt64(&stats.TargetsFailed), Label: "failed"}))
				}
			}
		}()
	}
	var wg sync.WaitGroup
	wg.Add(parallelism)
	for w := 0; w < parallelism; w++ {
		go func() {
			defer wg.Done()
			for {
				i := next.Add(1) - 1
				if i >= int64(len(groups)) {
					return
				}
				applyGroup(groups[i], baseDir, dryRun, &stats)
			}
		}()
	}
	wg.Wait()
	close(stop)
	return stats
}

func applyGroup(group PlanGroup, baseDir string, dryRun bool, stats *ApplyPlanStats) {
	originalPath := filepath.Join(baseDir, group.Original.Path)
	info, err := os.Lstat(originalPath)
	if err != nil || !info.Mode().IsRegular() {
		fmte.PrintfErr("skipping group: original \"%s\" not found at destination\n", group.Original.Path)
		atomic.AddInt64(&stats.GroupsSkipped, 1)
		return
	}
	if info.Size() != group.Size || info.ModTime().Unix() != group.Original.ModTime {
		fmte.PrintfErr("skipping group: original \"%s\" differs from plan (size %s/%s, mtime %d/%d)\n",
			group.Original.Path, lib.GroupThousands(info.Size()), lib.GroupThousands(group.Size), info.ModTime().Unix(), group.Original.ModTime)
		atomic.AddInt64(&stats.GroupsSkipped, 1)
		return
	}
	for _, target := range group.Targets {
		targetPath := filepath.Join(baseDir, target.Path)
		if _, err := os.Lstat(targetPath); err == nil {
			atomic.AddInt64(&stats.TargetsExisted, 1)
			continue
		}
		if dryRun {
			atomic.AddInt64(&stats.TargetsDone, 1)
			atomic.AddInt64(&stats.BytesSaved, group.Size)
			continue
		}
		if err := reflinkTarget(originalPath, info, targetPath, target.ModTime); err != nil {
			fmte.PrintfErr("failed to reflink \"%s\" to \"%s\": %+v\n", group.Original.Path, target.Path, err)
			atomic.AddInt64(&stats.TargetsFailed, 1)
			continue
		}
		atomic.AddInt64(&stats.TargetsDone, 1)
		atomic.AddInt64(&stats.BytesSaved, group.Size)
	}
	atomic.AddInt64(&stats.GroupsDone, 1)
}
