package main

import (
	"fmt"
	"github.com/m-manu/rsync-sidekick/action"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/m-manu/rsync-sidekick/lib"
	"github.com/stretchr/testify/assert"
	"math/rand"
	"os"
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

var exclusionsForTests lib.Set[string]

var runID string

var testCasesDir string

func init() {
	rand.Seed(time.Now().UTC().UnixNano())
	runID = time.Now().Format("150405")
	exclusionsForTests = lib.SetOf[string]("Thumbs.db", "System Volume Information")
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
	createDirectory(srcPath)
	createDirectory(dstPath)
	// Files and folders in sync between source and destination:
	copyFromGoRootAs("bin/go", "/go1", toBoth)
	copyFromGoRootAs("bin/gofmt", "gofmt1", toBoth)
	createDirectory(srcPath + "/code")
	createDirectory(dstPath + "/code")
	copyFromGoRootAs("src/sort/sort.go", "code/sort.go.txt", toBoth)
	copyFromGoRootAs("src/unicode/tables.go", "code/tables.go.txt", toBoth)
	// Files and folders only in source:
	createDirectory(srcPath + "/random_empty_directory_at_source")
	copyFromGoRootAs("CONTRIBUTORS", "some_file_at_source.txt", toSrc)
	copyFromGoRootAs("CONTRIBUTING.md", "Thumbs.db", toSrc)
	// Files and folders only in destination:
	createDirectory(dstPath + "/random_empty_directory_at_destination")
	copyFromGoRootAs("CONTRIBUTING.md", "some_file_at_destination.md", toDst)
	createDirectory(dstPath + "/System Volume Information")
	copyFromGoRootAs("CONTRIBUTORS", "System Volume Information/file_to_be_ignored.txt", toDst)
}

type SrcOrDstOrBoth int8

const (
	toSrc SrcOrDstOrBoth = iota
	toDst
	toBoth
)

func copyFromGoRootAs(pathInsideGoRoot string, pathInSrcOrDstOrBoth string, srcOrDstOrBoth SrcOrDstOrBoth) {
	path := runtime.GOROOT() + "/" + pathInsideGoRoot
	if srcOrDstOrBoth == toSrc || srcOrDstOrBoth == toBoth {
		copyFile(path, srcPath+"/"+pathInSrcOrDstOrBoth)
	}
	if srcOrDstOrBoth == toDst || srcOrDstOrBoth == toBoth {
		copyFile(path, dstPath+"/"+pathInSrcOrDstOrBoth)
	}
}

func createDirectory(path string) {
	err := os.MkdirAll(path, defaultFolderPerms)
	if err != nil {
		// This shouldn't happen, unless there is a bug in test case
		panic("error: Couldn't create directory: " + path)
	}
}

func TestRSyncSidekick(t *testing.T) {
	setup()
	//defer tearDown(t)
	fmte.Off()
	// Source and destination are in sync (base case)
	actions1, syncErr1 := getSyncActionsWithProgress(runID, srcPath, exclusionsForTests, dstPath, true)
	stopIfError(t, syncErr1)
	assert.Equal(t, []action.SyncAction{}, actions1)
	// Do series of changes at source:
	var err error
	someTime1 := randomTime()
	someTime2 := randomTime()
	// Case 1: Rename:
	moveFile(srcPath+"/go1", srcPath+"/go1_renamed")
	// Case 2: Timestamp change:
	changeFileTimestamp(srcPath+"/gofmt1", someTime1)
	// Case 3: Move to another (new) directory:
	createDirectory(srcPath + "/another_code")
	err = os.Rename(srcPath+"/code/sort.go.txt", srcPath+"/another_code/sort.go.txt")
	stopIfError(t, err)
	// Case 4: Rename + Timestamp change + Move to another directory:
	createDirectory(srcPath + "/yet_another_code")
	moveFile(srcPath+"/code/tables.go.txt", srcPath+"/yet_another_code/tables11.go.txt")
	changeFileTimestamp(srcPath+"/yet_another_code/tables11.go.txt", someTime2)
	stopIfError(t, err)
	// Propagate these changes to destination and verify:
	rsErr1 := rsyncSidekick(runID, srcPath, exclusionsForTests, dstPath, false, false)
	stopIfError(t, rsErr1)
	assert.FileExists(t, dstPath+"/go1_renamed")
	assert.Equal(t, someTime1.Unix(), modifiedTime(dstPath+"/gofmt1"))
	assert.FileExists(t, dstPath+"/another_code/sort.go.txt")
	assert.FileExists(t, dstPath+"/yet_another_code/tables11.go.txt")
	assert.Equal(t, someTime2.Unix(), modifiedTime(dstPath+"/yet_another_code/tables11.go.txt"))
	// Source and destination are back in sync
	actions2, syncErr2 := getSyncActionsWithProgress(runID, srcPath, exclusionsForTests, dstPath, false)
	stopIfError(t, syncErr2)
	assert.Equal(t, []action.SyncAction{}, actions2)
	deleteFile(srcPath + "/another_code/sort.go.txt")
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

func tearDown(t *testing.T) {
	err := os.RemoveAll(testCasesDir)
	stopIfError(t, err)
}

func stopIfError(t *testing.T, err error) {
	if err != nil {
		t.Fatalf("Failed due to error: %+v", err)
	}
}
