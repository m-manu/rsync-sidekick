package main

import (
	"fmt"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/action"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/stretchr/testify/assert"
)

// These would be inside the test cases directory
const (
	srcPath = "./source"
	dstPath = "./destination"
)

const defaultFolderPerms = 0755

var exclusionsForTests set.Set[string]

var runID string

var testCasesDir string

var originalWorkingDir string

func init() {
	runID = time.Now().Format("150405")
	exclusionsForTests = set.NewSet[string]("Thumbs.db", "System Volume Information", ".Trashes")
	var cdErr error
	testCasesDir, cdErr = filepath.Abs("./test_cases_" + runID)
	if cdErr != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic(fmt.Errorf("error: Unable to change directory to %s due to: %+v", srcPath, cdErr))
	}
}

func copyFile(srcPath string, dstPath string) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic(fmt.Errorf("error: Unable to read file %s due to: %+v", srcPath, err))
	}
	err = os.WriteFile(dstPath, data, 0644)
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic(fmt.Errorf("error: Unable to write file %s due to: %+v", srcPath, err))
	}
}

func cd(path string) {
	err := os.Chdir(path)
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic(fmt.Errorf("error: Unable to change directory to %s due to: %+v", path, err))
	}
}

func setup() {
	// Remember current working directory so we can restore it in tearDown
	wd, err := os.Getwd()
	if err != nil {
		panic(fmt.Errorf("error: Unable to get current working directory: %+v", err))
	}
	originalWorkingDir = wd

	createDirectory(testCasesDir)
	cd(testCasesDir)
	createDirectoryAt(".", SourceAndDestination)
	// Files and folders in sync between source and destination:
	copyFromGoRootAs("bin/go", "/go1", SourceAndDestination)
	copyFromGoRootAs("bin/gofmt", "gofmt1", SourceAndDestination)
	createDirectoryAt("code", SourceAndDestination)
	copyFromGoRootAs("src/sort/sort.go", "code/sort.go.txt", SourceAndDestination)
	copyFromGoRootAs("src/unicode/tables.go", "code/tables.go.txt", SourceAndDestination)
	copyFromGoRootAs("src/sync/map.go", "map.go.txt", SourceAndDestination)
	createDirectoryAt(".Trashes", SourceAndDestination)
	copyFromGoRootAs("src/cmd/go.sum", ".Trashes/go.sum", SourceAndDestination)
	// Files and folders only in source:
	createDirectoryAt("random_empty_directory_at_source", Source)
	copyFromGoRootAs("VERSION", "some_file_at_source.txt", Source)
	copyFromGoRootAs("CONTRIBUTING.md", "Thumbs.db", Source)
	// Files and folders only in destination:
	createDirectoryAt("random_empty_directory_at_destination", Destination)
	copyFromGoRootAs("CONTRIBUTING.md", "some_file_at_destination.md", Destination)
	createDirectoryAt("System Volume Information", Destination)
	copyFromGoRootAs("VERSION", "System Volume Information/file_to_be_ignored.txt", Destination)
}

type Where int8

const (
	Source Where = iota
	Destination
	SourceAndDestination
)

func copyFromGoRootAs(pathInsideGoRoot string, relativePath string, place Where) {
	p := path.Join(os.Getenv("GOROOT"), pathInsideGoRoot)
	if place == Source || place == SourceAndDestination {
		copyFile(p, atSrc(relativePath))
	}
	if place == Destination || place == SourceAndDestination {
		copyFile(p, atDst(relativePath))
	}
}

func atSrc(relativePath string) string {
	return path.Join(srcPath, relativePath)
}

func atDst(relativePath string) string {
	return path.Join(dstPath, relativePath)
}

func createDirectory(path string) {
	err := os.MkdirAll(path, defaultFolderPerms)
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic("error: Couldn't create directory: " + path)
	}
}

func createDirectoryAt(relativePath string, where Where) {
	if where == Source || where == SourceAndDestination {
		createDirectory(atSrc(relativePath))
	}
	if where == Destination || where == SourceAndDestination {
		createDirectory(atDst(relativePath))
	}
}

func TestRSyncSidekick(t *testing.T) {
	setup()
	defer tearDown(t)
	fmte.Off()
	// Source and destination are in sync (base case)
	actions1, syncErr1 := getSyncActionsWithProgress(runID, srcPath, exclusionsForTests, dstPath, true, 2*time.Second)
	stopIfError(t, syncErr1)
	assert.Equal(t, []action.SyncAction{}, actions1)
	// Do series of changes at source:
	someTime1 := randomTime()
	someTime2 := randomTime()
	// Case 1: Rename:
	moveFile(atSrc("go1"), atSrc("/go1_renamed"))
	// Case 2: Timestamp change:
	changeFileTimestamp(atSrc("/gofmt1"), someTime1)
	// Case 3: Move to another (new) directory:
	createDirectoryAt("another_code", Source)
	moveFile(atSrc("/code/sort.go.txt"), atSrc("/another_code/sort.go.txt"))
	// Case 4: Rename + Timestamp change + Move to another directory:
	createDirectory(atSrc("/yet_another_code"))
	moveFile(atSrc("/code/tables.go.txt"), atSrc("/yet_another_code/tables11.go.txt"))
	changeFileTimestamp(atSrc("/yet_another_code/tables11.go.txt"), someTime2)
	// Case 5: Modify content + rename (this should NOT sync)
	overwriteFileAtWith(atSrc("map.go.txt"), 10, "blah blah blah")
	moveFile(atSrc("map.go.txt"), atSrc("map1.go.txt"))
	// Case 6: Rename file inside ignored directory
	moveFile(atSrc(".Trashes/go.sum"), atSrc(".Trashes/go1.sum"))
	// Propagate these changes to destination and verify:
	rsErr1 := rsyncSidekick(runID, srcPath, exclusionsForTests, dstPath, "", false, false, false, 2*time.Second)
	stopIfError(t, rsErr1)
	// Assert at destination:
	assert.FileExists(t, atDst("/go1_renamed"))
	assert.Equal(t, someTime1.Unix(), modifiedTime(atDst("/gofmt1")))
	assert.FileExists(t, atDst("/another_code/sort.go.txt"))
	assert.FileExists(t, atDst("/yet_another_code/tables11.go.txt"))
	assert.Equal(t, someTime2.Unix(), modifiedTime(atDst("/yet_another_code/tables11.go.txt")))
	assert.FileExists(t, atSrc("map1.go.txt"))
	assert.NoFileExists(t, atDst("map1.go.txt"))
	assert.NoFileExists(t, atDst(".Trashes/go1.sum"))
	// Source and destination are back in sync
	actions2, syncErr2 := getSyncActionsWithProgress(runID, srcPath, exclusionsForTests, dstPath, false, 2*time.Second)
	stopIfError(t, syncErr2)
	assert.Equal(t, []action.SyncAction{}, actions2)
	deleteFile(atSrc("/another_code/sort.go.txt"))
	actions3, syncErr3 := getSyncActionsWithProgress(runID, srcPath, exclusionsForTests, dstPath, true, 2*time.Second)
	stopIfError(t, syncErr3)
	assert.Equal(t, []action.SyncAction{}, actions3)
}

func deleteFile(path string) {
	err := os.Remove(path)
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic(fmt.Sprintf("error: couldn't remove file %s due to: %+v", path, err))
	}
}

func changeFileTimestamp(path string, t time.Time) {
	err := os.Chtimes(path, t, t)
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic(fmt.Sprintf("error: couldn't change timestamp of file %s to %+v due to: %+v", path, t, err))
	}
}

func moveFile(fromPath, toPath string) {
	err := os.Rename(fromPath, toPath)
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic(fmt.Sprintf("error: couldn't move file from %s to %s due to: %+v", fromPath, toPath, err))
	}
}

func randomTime() time.Time {
	return time.Now().Add(-1 * time.Duration(rand.Intn(86400_00)) * time.Second)
}

func modifiedTime(path string) int64 {
	info, err := os.Lstat(path)
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic(fmt.Sprintf("error: couldn't get file modification timestamp of %s due to: %+v", path, err))
	}
	return info.ModTime().Unix()
}

func overwriteFileAtWith(filename string, position int, content string) {
	file, err := os.OpenFile(filename, os.O_WRONLY, 0644)
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic(fmt.Sprintf("error: couldn't open file %s due to: %+v", filename, err))
	}
	defer file.Close()
	_, err = file.Seek(int64(position), 0)
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic(fmt.Sprintf("error: couldn't seek file %s to position %d due to: %+v", filename, position, err))
	}
	_, err = file.Write([]byte(content))
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic(fmt.Sprintf("error: couldn't write content %#v to file %s at position %d due to: %+v",
			content, filename, position, err))
	}
}

func tearDown(t *testing.T) {
	// Switch back to the original working directory before deleting the test directory.
	// If we delete the current working directory, subsequent calls to filepath.Abs/os.Getwd()
	// in later tests can fail with "getwd: no such file or directory".
	if originalWorkingDir != "" {
		_ = os.Chdir(originalWorkingDir)
	}
	err := os.RemoveAll(testCasesDir)
	stopIfError(t, err)
}

func stopIfError(t *testing.T, err error) {
	if err != nil {
		t.Fatalf("Failed due to error: %+v", err)
	}
}

// TestTimestampPropagationBug reproduces the bug where rsync-sidekick incorrectly propagates
// the timestamp of a source file to a destination file with the same content, even though
// the destination file is already in sync with another source file.
func TestTimestampPropagationBug(t *testing.T) {
	// Create a test directory with source and destination subdirectories
	testDir, err := filepath.Abs("./test_timestamp_bug_" + runID)
	stopIfError(t, err)
	createDirectory(testDir)
	defer func() {
		errRemDir := os.RemoveAll(testDir)
		if errRemDir != nil {
			panic(errRemDir)
		}
	}()

	// Create source and destination directories
	srcDir := filepath.Join(testDir, "source")
	dstDir := filepath.Join(testDir, "destination")
	createDirectory(srcDir)
	createDirectory(dstDir)

	// Create file-1 with the same content and timestamp in source and destination
	fileContent := []byte("The quick brown fox jumps over the lazy dog")
	file1SrcPath := filepath.Join(srcDir, "file-1")
	file1DstPath := filepath.Join(dstDir, "file-1")
	err = os.WriteFile(file1SrcPath, fileContent, 0644)
	stopIfError(t, err)
	err = os.WriteFile(file1DstPath, fileContent, 0644)
	stopIfError(t, err)

	// Ensure file-1 has the same timestamp in source and destination
	file1Time := time.Now().Add(-1 * time.Hour)
	err = os.Chtimes(file1SrcPath, file1Time, file1Time)
	stopIfError(t, err)
	err = os.Chtimes(file1DstPath, file1Time, file1Time)
	stopIfError(t, err)

	// Create file-2 with different content in destination
	file2DstPath := filepath.Join(dstDir, "file-2")
	err = os.WriteFile(file2DstPath, []byte("b"), 0644)
	stopIfError(t, err)
	file2DstTime := time.Now().Add(-30 * time.Minute)
	err = os.Chtimes(file2DstPath, file2DstTime, file2DstTime)
	stopIfError(t, err)

	// Create file-2 in source with the same content as file-1
	file2SrcPath := filepath.Join(srcDir, "file-2")
	err = os.WriteFile(file2SrcPath, fileContent, 0644)
	stopIfError(t, err)
	file2SrcTime := time.Now().Add(-15 * time.Minute)
	err = os.Chtimes(file2SrcPath, file2SrcTime, file2SrcTime)
	stopIfError(t, err)

	// Verify the initial setup
	assert.Equal(t, file1Time.Unix(), modifiedTime(file1SrcPath))
	assert.Equal(t, file1Time.Unix(), modifiedTime(file1DstPath))
	assert.Equal(t, file2SrcTime.Unix(), modifiedTime(file2SrcPath))
	assert.Equal(t, file2DstTime.Unix(), modifiedTime(file2DstPath))

	// Run rsync-sidekick
	fmte.Off()
	exclusions := set.NewSet[string]()
	actions, syncErr := getSyncActionsWithProgress(runID, srcDir, exclusions, dstDir, true, 0)
	stopIfError(t, syncErr)

	// The bug is that rsync-sidekick will incorrectly create an action to propagate
	// the timestamp of source/file-2 to destination/file-1

	// Check if there's an action to propagate timestamp from source/file-2 to destination/file-1
	foundIncorrectAction := false
	for _, a := range actions {
		if propAction, ok := a.(action.PropagateTimestampAction); ok {
			if propAction.SourceFileRelativePath == "file-2" &&
				propAction.DestinationFileRelativePath == "file-1" {
				foundIncorrectAction = true
				break
			}
		}
	}

	// This assertion should fail when the bug is present
	assert.False(t, foundIncorrectAction, "Bug detected: rsync-sidekick incorrectly propagates timestamp from source/file-2 to destination/file-1")
}
