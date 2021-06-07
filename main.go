package main

import (
	_ "embed"
	"flag"
	"fmt"
	"github.com/m-manu/rsync-sidekick/filesutil"
	"github.com/m-manu/rsync-sidekick/fmte"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// Constants indicating return codes of this tool, when run from command line
const (
	ExitCodeMissingArguments = iota + 1
	ExitCodeSourceDirError
	ExitCodeDestinationDirError
	ExitCodeSyncError
	ExitCodeExclusionFilesError
)

func readExclusions(excludesFilePath string) (map[string]struct{}, error) {
	rawContents, err := os.ReadFile(excludesFilePath)
	if err != nil {
		return nil, err
	}
	contents := strings.ReplaceAll(string(rawContents), "\r\n", "\n") // Windows
	return lineSeparatedStrToMap(contents), nil
}

func lineSeparatedStrToMap(lineSeparatedString string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, e := range strings.Split(lineSeparatedString, "\n") {
		m[e] = struct{}{}
	}
	for e := range m {
		if strings.TrimSpace(e) == "" {
			delete(m, e)
		}
	}
	return m
}

func handlePanic() {
	err := recover()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Program exited unexpectedly due to an unknown error.\n"+
			"Please report the below eror to the author:\n"+
			"error message: %+v\n", err)
		_, _ = fmt.Fprintln(os.Stderr, string(debug.Stack()))
	}
}

func getExecutable() string {
	executable, _ := os.Executable()
	return filepath.Base(executable)
}

const (
	usageString = `usage:
	%s <flags> [source-dir] [destination-dir]
where:
	source-dir        Source directory
	destination-dir   Destination directory
flags: (all optional)
`
	exclusionsFlag = "exclusions"
	scriptGenFlag  = "shellscript"
	extraInfoFlag  = "extrainfo"
)

//go:embed default_exclusions.txt
var defaultExclusions string

// RunID is a unique id for a run of this tool
var RunID string

func init() {
	RunID = time.Now().Format("150405")
}

func main() {
	defer handlePanic()
	exclusionsPathFlagPtr := flag.String(exclusionsFlag, "",
		"path to a text file that contains ignorable file/directory names separated by new lines"+
			" (even without this flag, this tool ignores commonly ignorable names such as "+
			"'System Volume Information', 'Thumbs.db' etc.)",
	)
	scriptGenFlagPtr := flag.Bool(scriptGenFlag, false,
		"instead of applying changes directly, generate a shell script"+
			" (this flag is useful if you want run the shell script as a different user)",
	)
	extraInfoFlagPtr := flag.Bool(extraInfoFlag, false,
		"generate extra information (caution: makes it slow!)",
	)
	flag.Usage = func() {
		fmte.PrintfErr(usageString, getExecutable())
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 {
		fmte.PrintfErr("error: missing arguments!\n")
		flag.Usage()
		os.Exit(ExitCodeMissingArguments)
	}
	exclusionsPath := *exclusionsPathFlagPtr
	scriptGen := *scriptGenFlagPtr
	extraInfo := *extraInfoFlagPtr
	var exclusions map[string]struct{}
	var eErr error
	if exclusionsPath == "" {
		exclusions = lineSeparatedStrToMap(defaultExclusions)
	} else {
		exclusions, eErr = readExclusions(exclusionsPath)
		if eErr != nil {
			fmte.PrintfErr("error: couldn't read argument to flag %s due to: %+v\n", exclusionsFlag, eErr)
			os.Exit(ExitCodeExclusionFilesError)
		}
	}
	sourceDirPath, sourceDirErr := filepath.Abs(flag.Arg(0))
	if sourceDirErr != nil || !filesutil.IsReadableDirectory(sourceDirPath) {
		fmte.PrintfErr("error: path for source directory \"%s\" is not a readable directory \n",
			flag.Arg(0))
		os.Exit(ExitCodeSourceDirError)
	}
	destinationDirPath, destinationDirErr := filepath.Abs(flag.Arg(1))
	if destinationDirErr != nil || !filesutil.IsReadableDirectory(destinationDirPath) {
		fmte.PrintfErr("error: path for destination directory \"%s\" is not a readable directory \n",
			flag.Arg(1))
		os.Exit(ExitCodeDestinationDirError)
	}
	syncErr := rsyncSidekick(sourceDirPath, exclusions, destinationDirPath, scriptGen, extraInfo)
	if syncErr != nil {
		fmte.PrintfErr("error while syncing: %+v\n", syncErr)
		os.Exit(ExitCodeSyncError)
	}
}
