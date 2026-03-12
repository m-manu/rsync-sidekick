package service

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/action"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/m-manu/rsync-sidekick/lib"
)

const (
	indexBuildErrorCountTolerance = 20
)

// FindOrphans finds files at source that do not have corresponding files at destination.
// File at destination must exist and have same size and same modified timestamp.
func FindOrphans(sourceFiles, destinationFiles map[string]entity.FileMeta) []string {
	orphansAtSource := make([]string, 0, len(sourceFiles)/10)
	for sourcePath, sourceFileMeta := range sourceFiles {
		destinationFileMeta, existsAtDestination := destinationFiles[sourcePath]
		if !existsAtDestination || sourceFileMeta != destinationFileMeta {
			orphansAtSource = append(orphansAtSource, sourcePath)
		}
	}
	return orphansAtSource
}

func buildIndex(baseDirPath string, filesToScan []string, counter *int32,
	filesToDigests lib.SafeMap[string, entity.FileDigest], digestsToFiles lib.MultiMap[entity.FileDigest, string],
) error {
	errCount := 0
	for _, relativePath := range filesToScan {
		newValue := atomic.AddInt32(counter, 1)
		path := filepath.Join(baseDirPath, relativePath)
		fmte.PrintfV("Evaluating file (#%d): %s\n", newValue, path)
		digest, err := getDigest(path)
		if err != nil {
			errCount++
			fmte.PrintfErr("couldn't index file \"%s\" (skipping): %+v\n", path, err)
		}
		if errCount > indexBuildErrorCountTolerance {
			return fmt.Errorf("too many errors while building index")
		}
		filesToDigests.Set(relativePath, digest)
		digestsToFiles.Set(digest, relativePath)
	}
	return nil
}

// ComputeSyncActions identifies the diff between source and destination directories that
// do not require actual file transfer. This is the core function of this tool.
func ComputeSyncActions(sourceDirPath string, sourceFiles map[string]entity.FileMeta, orphansAtSource []string,
	destinationDirPath string, destinationFiles map[string]entity.FileMeta, candidatesAtDestination []string,
	sourceCounter *int32, destinationCounter *int32, matchDuplicates bool,
) (actions []action.SyncAction, savings int64, err error) {
	orphanFilesToDigests := lib.NewSafeMap[string, entity.FileDigest]()
	candidateFilesToDigests := lib.NewSafeMap[string, entity.FileDigest]()
	orphanDigestsToFiles := lib.NewMultiMap[entity.FileDigest, string]()
	candidateDigestsToFiles := lib.NewMultiMap[entity.FileDigest, string]()
	var sourceIndexErrs, destinationIndexErrs []error
	parallelismForSource, parallelismForDestination := getParallelism(runtime.NumCPU())
	var wg sync.WaitGroup
	wg.Add(parallelismForSource + parallelismForDestination)
	for i := 0; i < parallelismForSource; i++ {
		go func(index int) {
			defer wg.Done()
			low := index * len(orphansAtSource) / parallelismForSource
			high := (index + 1) * len(orphansAtSource) / parallelismForSource
			sourceIndexErr := buildIndex(sourceDirPath, orphansAtSource[low:high], sourceCounter,
				orphanFilesToDigests, orphanDigestsToFiles,
			)
			if sourceIndexErr != nil {
				sourceIndexErrs = append(sourceIndexErrs, sourceIndexErr)
			}
		}(i)
	}
	for i := 0; i < parallelismForDestination; i++ {
		go func(index int) {
			defer wg.Done()
			low := index * len(candidatesAtDestination) / parallelismForDestination
			high := (index + 1) * len(candidatesAtDestination) / parallelismForDestination
			destinationIndexErr := buildIndex(destinationDirPath, candidatesAtDestination[low:high], destinationCounter,
				candidateFilesToDigests, candidateDigestsToFiles,
			)
			if destinationIndexErr != nil {
				destinationIndexErrs = append(destinationIndexErrs, destinationIndexErr)
			}
		}(i)
	}
	wg.Wait()
	if len(sourceIndexErrs) > 0 {
		return nil, 0, fmte.Errors("error(s) while building index on source directory: ",
			sourceIndexErrs)
	}
	if len(destinationIndexErrs) > 0 {
		return nil, 0, fmte.Errors("error(s) while building index on destination directory: ",
			destinationIndexErrs)
	}
	actions = make([]action.SyncAction, 0, orphanFilesToDigests.Len())
	uniqueness := set.NewSetWithSize[string](orphanFilesToDigests.Len())
	for orphanAtSource, orphanDigest := range orphanFilesToDigests.ForEach() {
		orphans := orphanDigestsToFiles.Get(orphanDigest)
		if len(orphans) > 1 && !matchDuplicates {
			// default behaviour when many orphans at source have the same digest
			fmte.PrintfV("Multiple orphans found (%d): skip matching for %s\n", len(orphans), orphanAtSource)
			continue
		}
		if !candidateDigestsToFiles.Exists(orphanDigest) {
			// let rsync handle this
			continue
		}
		matchesAtDestination := candidateDigestsToFiles.Get(orphanDigest)
		var candidateAtDestination string
		// Default conservative behavior (skipping duplicates)
		if !matchDuplicates {
			if len(matchesAtDestination) == 1 {
				candidateAtDestination = matchesAtDestination[0]
			} else {
				// If multiple files with same digest exist at destination,
				// choose a random one that does *not* exist at source
				for _, destinationPath := range matchesAtDestination {
					if _, existsAtSource := sourceFiles[destinationPath]; !existsAtSource {
						candidateAtDestination = destinationPath
						break
					}
				}
			}
		// Progressive approach: match duplicates as well (especially for media archives)
		} else {
			baseName := filepath.Base(orphanAtSource)
			srcMeta := sourceFiles[orphanAtSource]
			// Filename grouping for high accuracy
			candidates := []string{}
			for _, dst := range matchesAtDestination {
				if filepath.Base(dst) == baseName {
 					candidates = append(candidates, dst)
				}
			}
			// fallback if filename not unique
			if len(candidates) == 0 {
				candidates = matchesAtDestination
			}
			filtered := filterByTimestamp(candidates, srcMeta, destinationFiles)
			if len(filtered) == 1 {
				candidateAtDestination = filtered[0]
			} else if len(filtered) > 1 {
				bestScore := -1
				for _, c := range filtered {
					s := calculateSimilarityScore(orphanAtSource, c)
					// Note: similarity scoring is only used when files share the same digest.
					// Therefore any candidate selected represents identical file content.
					// Worst-case a wrong match may cause an unnecessary move operation,
					// but rsync will still produce the correct final state.
					if s > bestScore {
						bestScore = s
						candidateAtDestination = c
						fmte.PrintfV("Duplicate match: %s -> %s\n", orphanAtSource, candidateAtDestination)
					}
				}
			}
		}
		if candidateAtDestination == "" {
			continue
		}
		if destinationFiles[candidateAtDestination].ModifiedTimestamp != sourceFiles[orphanAtSource].ModifiedTimestamp {
			// Avoid propagating timestamp to a destination file that already matches its counterpart at source
			if srcMetaForCandidate, existsAtSourceForCandidate := sourceFiles[candidateAtDestination]; !(existsAtSourceForCandidate && srcMetaForCandidate == destinationFiles[candidateAtDestination]) {
				timestampAction := action.PropagateTimestampAction{
					SourceBaseDirPath:           sourceDirPath,
					DestinationBaseDirPath:      destinationDirPath,
					SourceFileRelativePath:      orphanAtSource,
					DestinationFileRelativePath: candidateAtDestination,
				}
				if !uniqueness.Contains(timestampAction.Uniqueness()) {
					actions = append(actions, timestampAction)
					uniqueness.Add(timestampAction.Uniqueness())
					savings += sourceFiles[orphanAtSource].Size
				}
			}
		}
		if _, existsAtSource := sourceFiles[candidateAtDestination]; !existsAtSource &&
			candidateAtDestination != orphanAtSource {
			parentDir := filepath.Dir(filepath.Join(destinationDirPath, orphanAtSource))
			if !lib.IsReadableDirectory(parentDir) {
				directoryAction := action.MakeDirectoryAction{
					AbsoluteDirPath: parentDir,
				}
				if !uniqueness.Contains(directoryAction.Uniqueness()) {
					actions = append(actions, directoryAction)
					uniqueness.Add(directoryAction.Uniqueness())
				}
			}
			moveFileAction := action.MoveFileAction{
				BasePath:         destinationDirPath,
				RelativeFromPath: candidateAtDestination,
				RelativeToPath:   orphanAtSource,
			}
			if !uniqueness.Contains(moveFileAction.Uniqueness()) {
				actions = append(actions, moveFileAction)
				uniqueness.Add(moveFileAction.Uniqueness())
				savings += sourceFiles[orphanAtSource].Size
			}
		}
	}
	return
}

func getParallelism(n int) (int, int) {
	if n > 3 {
		if n%2 == 0 {
			return n/2 - 1, n / 2
		} else {
			return n / 2, n / 2
		}
	}
	return 1, 1
}

func FindDirectoryResultToCsv(dirPath string, excludedFiles set.Set[string], file *os.File) error {
	files, _, fErr := FindFilesFromDirectory(dirPath, excludedFiles)
	if fErr != nil {
		return fErr
	}
	cw := csv.NewWriter(file)
	for f, fileMeta := range files {
		record := []string{f, strconv.FormatInt(fileMeta.Size, 10),
			strconv.FormatInt(fileMeta.ModifiedTimestamp, 10)}
		wErr := cw.Write(record)
		if wErr != nil {
			return fmt.Errorf("error while writing record %+v: %+v", record, wErr)
		}
	}
	cw.Flush()
	return nil
}

// Ranks how similar two file paths are, in order to detect renamed folders
//
// Use case:     multiple destination files share the same digest,
//               we must choose the most likely candidate to move
// Precondition: both files have identical digests (guaranteed identical content)
//
// Example: the highest score is found for "Summer" and "Summer Season 2019"
//   source:      Summer/2019/FILE0001.MOV
//   candidates:  Summer Season 2018/FILE0001.MOV
//                Summer Season 2019/FILE0001.MOV
//                Winter Season 2019/FILE0001.MOV
//
// The algorithm is heuristic but deterministic and uses 4 signals:
// 1. Filename equality: +50
//   Camera/media filenames are typically unique within an archive.
//   Matching filenames therefore strongly indicates the correct file.
// 2. Directory equality: +5
//   Shared folder structure indicate a strong signal.
// 3. Substring directory match: +3
//   Handles common changes to folder names.
// 4. Word overlap between directories: +1
//   Weak signal when folders share common words.
//
// What matters is the ratio: the exact numeric values are not critical. 
// Weights are intentionally hierarchical and must ensure following ordering:
//   filename match (50)  >> directory match (5)
//   directory match (5)  > substring match (3)
//   substring match (3)  > word overlap (1)
//
// This ensures filename equality dominates the decision while directory
// similarity resolves ambiguities when multiple candidates exist.
//
func calculateSimilarityScore(a, b string) int {
	score := 0
	// MATCH: identical filename
	if strings.ToLower(filepath.Base(a)) == strings.ToLower(filepath.Base(b)) {
		score += 50
	}
	// Compare directory segments (ignore filename)
	aParts := strings.Split(filepath.ToSlash(a), "/")
	bParts := strings.Split(filepath.ToSlash(b), "/")
	aDir := aParts[:len(aParts)-1]
	bDir := bParts[:len(bParts)-1]
	for _, ap := range aDir {
		ap = strings.ToLower(ap)
		for _, bp := range bDir {
			bp = strings.ToLower(bp)
			// MATCH: identical folder name
			if ap == bp {
				score += 5
				continue
			}
			// MATCH: full folder name (as substring of the other name)
			if strings.Contains(ap, bp) || strings.Contains(bp, ap) {
				score += 3
				continue
			}
			// MATCH: partial folder name (word-level overlap)
			aWords := strings.Fields(ap)
			bWords := strings.Fields(bp)
			for _, aw := range aWords {
				for _, bw := range bWords {
					if aw == bw {
						score++
					}
				}
			}
		}
	}
	return score
}

func filterByTimestamp(paths []string, sourceMeta entity.FileMeta, destinationFiles map[string]entity.FileMeta) []string {
	result := []string{}
	for _, p := range paths {
		if destinationFiles[p].ModifiedTimestamp == sourceMeta.ModifiedTimestamp {
			result = append(result, p)
		}
	}
	return result
}
