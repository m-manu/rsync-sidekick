package main

import (
	"fmt"
	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/action"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/stretchr/testify/assert"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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
	createDirectory(testCasesDir)
	cd(testCasesDir)
	createDirectoryAt(".", Both)
	// Files and folders in sync between source and destination:
	copyFromGoRootAs("bin/go", "/go1", Both)
	copyFromGoRootAs("bin/gofmt", "gofmt1", Both)
	createDirectoryAt("code", Both)
	copyFromGoRootAs("src/sort/sort.go", "code/sort.go.txt", Both)
	copyFromGoRootAs("src/unicode/tables.go", "code/tables.go.txt", Both)
	copyFromGoRootAs("src/sync/map.go", "map.go.txt", Both)
	createDirectoryAt(".Trashes", Both)
	copyFromGoRootAs("src/cmd/go.sum", ".Trashes/go.sum", Both)
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
	Both
)

func copyFromGoRootAs(pathInsideGoRoot string, relativePath string, place Where) {
	p := path.Join(runtime.GOROOT(), pathInsideGoRoot)
	if place == Source || place == Both {
		copyFile(p, atSrc(relativePath))
	}
	if place == Destination || place == Both {
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
	if where == Source || where == Both {
		createDirectory(atSrc(relativePath))
	}
	if where == Destination || where == Both {
		createDirectory(atDst(relativePath))
	}
}

func TestRSyncSidekick(t *testing.T) {
	setup()
	defer tearDown(t)
	fmte.Off()
	// Source and destination are in sync (base case)
	actions1, syncErr1 := getSyncActionsWithProgress(runID, srcPath, exclusionsForTests, dstPath, true)
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
	rsErr1 := rsyncSidekick(runID, srcPath, exclusionsForTests, dstPath, "", false)
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
	actions2, syncErr2 := getSyncActionsWithProgress(runID, srcPath, exclusionsForTests, dstPath, false)
	stopIfError(t, syncErr2)
	assert.Equal(t, []action.SyncAction{}, actions2)
	deleteFile(atSrc("/another_code/sort.go.txt"))
	actions3, syncErr3 := getSyncActionsWithProgress(runID, srcPath, exclusionsForTests, dstPath, true)
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
	err := os.RemoveAll(testCasesDir)
	stopIfError(t, err)
}

func stopIfError(t *testing.T, err error) {
	if err != nil {
		t.Fatalf("Failed due to error: %+v", err)
	}
}
