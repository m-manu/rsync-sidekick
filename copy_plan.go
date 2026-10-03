package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/v2/action"
	"github.com/m-manu/rsync-sidekick/v2/bytesutil"
	"github.com/m-manu/rsync-sidekick/v2/entity"
	"github.com/m-manu/rsync-sidekick/v2/fmte"
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

func writeCopyPlan(plan copyPlanOutput, orphans []string, actions []action.SyncAction,
	archiveResolved set.Set[string], destDirPath string, sourceFiles map[string]entity.FileMeta,
	knownDigests map[string]entity.FileDigest, digestFn service.OrphanDigestFunc,
) error {
	unresolved := unresolvedOrphans(orphans, actions, destDirPath, archiveResolved)
	fmte.Printf("Building copy plan for %d files nothing at destination can serve...\n", len(unresolved))
	copyList, groups, err := service.BuildCopyPlan(unresolved, sourceFiles, knownDigests, digestFn)
	if err != nil {
		return err
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
		fmte.Printf("Copy list: %d files (%s) to transfer → %s\n",
			len(copyList), bytesutil.BinaryFormat(transferBytes), plan.CopyListPath)
	}
	if plan.PlanPath != "" {
		if err := service.WritePlan(plan.PlanPath, groups); err != nil {
			return err
		}
		fmte.Printf("Plan: %d duplicate groups, %d files (%s) to reflink afterwards → %s\n",
			len(groups), targets, bytesutil.BinaryFormat(savedBytes), plan.PlanPath)
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
		fmte.Printf("Checking %d plan groups against %s (dry run)...\n", len(groups), destDirPath)
	} else {
		fmte.Printf("Applying %d plan groups to %s...\n", len(groups), destDirPath)
	}
	start := time.Now()
	stats := service.ApplyPlan(groups, destDirPath, dryRun, runtime.NumCPU(), progressFrequency)
	verb := "reflinked"
	if dryRun {
		verb = "would reflink"
	}
	fmte.Printf("Done in %.1fs: %d groups applied, %d skipped; %s %d files (%s), %d already there, %d failed\n",
		time.Since(start).Seconds(), stats.GroupsDone, stats.GroupsSkipped, verb, stats.TargetsDone,
		bytesutil.BinaryFormat(stats.BytesSaved), stats.TargetsExisted, stats.TargetsFailed)
	if stats.TargetsFailed > 0 {
		return fmt.Errorf("%d target(s) failed", stats.TargetsFailed)
	}
	return nil
}
