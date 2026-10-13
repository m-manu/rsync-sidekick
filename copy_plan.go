package main

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/bytesutil"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/m-manu/rsync-sidekick/v2/fmte"
	"github.com/m-manu/rsync-sidekick/v2/lib"
	"github.com/m-manu/rsync-sidekick/v2/service"
)

// copyPlanOutput names the files a run writes for transferring each content only once:
// the list for rsync --files-from and the reflink plan for --apply-plan.
type copyPlanOutput struct {
	CopyListPath string
	PlanPath     string
}

func (p copyPlanOutput) enabled() bool {
	return p.CopyListPath != "" || p.PlanPath != ""
}

// recordResolvedOrphans wraps an archive action callback so the orphans it serves are
// remembered: streamed actions are applied right away and never returned to the caller.
func recordResolvedOrphans(onAction service.ArchiveActionFunc, destDirPath string,
	resolved set.Set[string],
) service.ArchiveActionFunc {
	return func(a action.SyncAction) error {
		if err := onAction(a); err != nil {
			return err
		}
		if copyAction, ok := a.(action.CopyFileAction); ok {
			if rel, ok := relativeTo(destDirPath, copyAction.AbsDestPath); ok {
				resolved.Add(rel)
			}
		}
		return nil
	}
}

func relativeTo(base, path string) (string, bool) {
	prefix := strings.TrimSuffix(base, "/") + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	return path[len(prefix):], true
}

// unresolvedOrphans returns the orphans no action takes care of — what rsync still has
// to transfer. An orphan is resolved by a move or copy to its path, or by a timestamp
// fix on the file already sitting at its path.
func unresolvedOrphans(orphans []string, actions []action.SyncAction, destDirPath string,
	alsoResolved set.Set[string],
) []string {
	resolved := alsoResolved.Clone()
	for _, a := range actions {
		switch act := a.(type) {
		case action.MoveFileAction:
			resolved.Add(act.RelativeToPath)
		case action.CopyFileAction:
			if rel, ok := relativeTo(destDirPath, act.AbsDestPath); ok {
				resolved.Add(rel)
			}
		case action.PropagateTimestampAction:
			if act.DestinationFileRelativePath == act.SourceFileRelativePath {
				resolved.Add(act.SourceFileRelativePath)
			}
		}
	}
	unresolved := make([]string, 0, len(orphans))
	for _, orphan := range orphans {
		if !resolved.Contains(orphan) {
			unresolved = append(unresolved, orphan)
		}
	}
	return unresolved
}

// hashMinSize keeps files smaller than this away from hashing (--hash-min-size); zero
// hashes every size. Such files stay orphans: rsync transfers them, and with --copy-list
// they go straight into the list.
var hashMinSize int64

// hashableOrphans returns the orphans of at least hashMinSize, in path order.
func hashableOrphans(orphans []string, sourceFiles map[string]entity.FileMeta) []string {
	hashable := make([]string, 0, len(orphans))
	for _, orphan := range orphans {
		if sourceFiles[orphan].Size >= hashMinSize {
			hashable = append(hashable, orphan)
		}
	}
	slices.Sort(hashable)
	return hashable
}

// skippedSmallNote names the orphans that hashableOrphans left out, for the archive scan line.
func skippedSmallNote(orphans, hashable []string) string {
	if n := len(orphans) - len(hashable); n > 0 {
		return fmt.Sprintf(" (%s below --hash-min-size skipped)", lib.GroupThousands(n))
	}
	return ""
}

func writeCopyPlan(plan copyPlanOutput, orphans []string, actions []action.SyncAction,
	archiveResolved set.Set[string], destDirPath string, sourceFiles map[string]entity.FileMeta,
	knownDigests map[string]entity.FileDigest, digestFn service.OrphanDigestFunc,
) error {
	unresolved := unresolvedOrphans(orphans, actions, destDirPath, archiveResolved)
	toPlan := hashableOrphans(unresolved, sourceFiles)
	small := len(unresolved) - len(toPlan)
	fmte.Printf("Building copy plan for %s files nothing at destination can serve (%s below --hash-min-size go straight to the list)...\n",
		lib.GroupThousands(len(unresolved)), lib.GroupThousands(small))
	copyList, groups, err := service.BuildCopyPlan(toPlan, sourceFiles, knownDigests, digestFn)
	if err != nil {
		return err
	}
	if small > 0 {
		for _, orphan := range unresolved {
			if sourceFiles[orphan].Size < hashMinSize {
				copyList = append(copyList, orphan)
			}
		}
		slices.Sort(copyList)
	}
	var transferBytes, savedBytes int64
	targets := 0
	for _, path := range copyList {
		transferBytes += sourceFiles[path].Size
	}
	for _, group := range groups {
		targets += len(group.Targets)
		savedBytes += group.Size * int64(len(group.Targets))
	}
	if plan.CopyListPath != "" {
		if err := service.WriteCopyList(plan.CopyListPath, copyList); err != nil {
			return err
		}
		fmte.Printf("Copy list: %s files (%s) to transfer → %s\n",
			lib.GroupThousands(len(copyList)), bytesutil.BinaryFormat(transferBytes), plan.CopyListPath)
	}
	if plan.PlanPath != "" {
		if err := service.WritePlan(plan.PlanPath, groups); err != nil {
			return err
		}
		fmte.Printf("Plan: %s duplicate groups, %s files (%s) to reflink afterwards → %s\n",
			lib.GroupThousands(len(groups)), lib.GroupThousands(targets), bytesutil.BinaryFormat(savedBytes), plan.PlanPath)
	}
	return nil
}

// runApplyPlan reflinks the duplicates of a plan below destDirPath.
func runApplyPlan(planPath, destDirPath string, dryRun bool, progressFrequency time.Duration) error {
	groups, err := service.ReadPlan(planPath)
	if err != nil {
		return err
	}
	if dryRun {
		fmte.Printf("Checking %s plan groups against %s (dry run)...\n", lib.GroupThousands(len(groups)), destDirPath)
	} else {
		fmte.Printf("Applying %s plan groups to %s...\n", lib.GroupThousands(len(groups)), destDirPath)
	}
	start := time.Now()
	stats := service.ApplyPlan(groups, destDirPath, dryRun, runtime.NumCPU(), progressFrequency)
	verb := "reflinked"
	if dryRun {
		verb = "would reflink"
	}
	fmte.Printf("Done in %.1fs: %s groups applied, %s skipped; %s %s files (%s), %s already there, %s failed\n",
		time.Since(start).Seconds(), lib.GroupThousands(stats.GroupsDone), lib.GroupThousands(stats.GroupsSkipped),
		verb, lib.GroupThousands(stats.TargetsDone), bytesutil.BinaryFormat(stats.BytesSaved),
		lib.GroupThousands(stats.TargetsExisted), lib.GroupThousands(stats.TargetsFailed))
	if stats.TargetsFailed > 0 {
		return fmt.Errorf("%s target(s) failed", lib.GroupThousands(stats.TargetsFailed))
	}
	return nil
}
