package service

import (
	"fmt"
	"github.com/m-manu/rsync-sidekick/action"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/filesutil"
	"github.com/m-manu/rsync-sidekick/fmte"
	"path/filepath"
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

func buildIndex(baseDirPath string, filesToScan []string) (
	map[string]entity.FileDigest, map[entity.FileDigest][]string, error,
) {
	filesToDigests := make(map[string]entity.FileDigest, len(filesToScan))
	digestsToFiles := make(map[entity.FileDigest][]string, len(filesToScan))
	errCount := 0
	for _, relativePath := range filesToScan {
		path := filepath.Join(baseDirPath, relativePath)
		digest, err := GetDigest(path)
		if err != nil {
			errCount++
			fmte.PrintfErr("couldn't index file \"%s\" (skipping): %+v\n", path, err)
		}
		if errCount > indexBuildErrorCountTolerance {
			return nil, nil, fmt.Errorf("too many errors while building index")
		}
		filesToDigests[relativePath] = digest
		digestsToFiles[digest] = append(digestsToFiles[digest], relativePath)
	}
	return filesToDigests, digestsToFiles, nil
}

func ComputeSyncActions(sourceDirPath string, sourceFiles map[string]entity.FileMeta, orphansAtSource []string,
	destinationDirPath string, destinationFiles map[string]entity.FileMeta, candidatesAtDestination []string,
) ([]action.SyncAction, int64, error) {
	var savings int64
	orphanFilesToDigests, orphanDigestsToFiles, sourceIndexErr := buildIndex(sourceDirPath, orphansAtSource)
	if sourceIndexErr != nil {
		return nil, 0, fmt.Errorf("error while building index on source directory: %v", sourceIndexErr)
	}
	_, candidateDigestsToFiles, destinationIndexErr := buildIndex(destinationDirPath, candidatesAtDestination)
	if destinationIndexErr != nil {
		return nil, 0, fmt.Errorf("error while building index on destination directory: %v", destinationIndexErr)
	}
	actions := make([]action.SyncAction, 0, len(orphanFilesToDigests))
	uniquenessMap := make(map[string]struct{}, len(orphanFilesToDigests))
	for orphanAtSource, orphanDigest := range orphanFilesToDigests {
		if len(orphanDigestsToFiles[orphanDigest]) > 1 {
			// many orphans at source have the same digest
			continue
		}
		matchesAtDestination, existsAtDestination := candidateDigestsToFiles[orphanDigest]
		if !existsAtDestination {
			// let rsync handle this
			continue
		}
		var candidateAtDestination string
		if len(matchesAtDestination) == 1 {
			candidateAtDestination = matchesAtDestination[0]
		} else {
			// If multiple files with same digest exists at destination,
			// choose one that does not exist at source
			for _, path := range matchesAtDestination {
				if _, existsAtSource := sourceFiles[path]; !existsAtSource {
					candidateAtDestination = path
				}
			}
		}
		if candidateAtDestination == "" {
			continue
		}
		if destinationFiles[candidateAtDestination].ModifiedTimestamp != sourceFiles[orphanAtSource].ModifiedTimestamp {
			timestampAction := action.PropagateTimestampAction{
				SourceBaseDirPath:           sourceDirPath,
				DestinationBaseDirPath:      destinationDirPath,
				SourceFileRelativePath:      orphanAtSource,
				DestinationFileRelativePath: candidateAtDestination,
			}
			if _, exists := uniquenessMap[timestampAction.Uniqueness()]; !exists {
				actions = append(actions, timestampAction)
				uniquenessMap[timestampAction.Uniqueness()] = struct{}{}
				savings += sourceFiles[orphanAtSource].Size
			}
		}
		if _, existsAtSource := sourceFiles[candidateAtDestination]; !existsAtSource &&
			candidateAtDestination != orphanAtSource {
			parentDir := filepath.Dir(filepath.Join(destinationDirPath, orphanAtSource))
			if !filesutil.IsReadableDirectory(parentDir) {
				directoryAction := action.MakeDirectoryAction{
					AbsoluteDirPath: parentDir,
				}
				if _, exists := uniquenessMap[directoryAction.Uniqueness()]; !exists {
					actions = append(actions, directoryAction)
					uniquenessMap[directoryAction.Uniqueness()] = struct{}{}
				}
			}
			moveFileAction := action.MoveFileAction{
				BasePath:         destinationDirPath,
				RelativeFromPath: candidateAtDestination,
				RelativeToPath:   orphanAtSource,
			}
			if _, exists := uniquenessMap[moveFileAction.Uniqueness()]; !exists {
				actions = append(actions, moveFileAction)
				uniquenessMap[moveFileAction.Uniqueness()] = struct{}{}
				savings += sourceFiles[orphanAtSource].Size
			}

		}
	}
	return actions, savings, nil
}
