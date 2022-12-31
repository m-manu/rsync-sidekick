package main

import (
	_ "embed"
	"fmt"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/m-manu/rsync-sidekick/lib"
	"github.com/m-manu/rsync-sidekick/service"
	flag "github.com/spf13/pflag"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

const (
	applicationMajorVersion = 1
	applicationMinorVersion = 4
	applicationPatchVersion = 1
)

var applicationVersion = fmt.Sprintf("v%d.%d.%d",
	applicationMajorVersion, applicationMinorVersion, applicationPatchVersion,
)

// Constants indicating return codes of this tool, when run from command line
const (
	exitCodeSuccess = iota
	exitCodeInvalidNumArgs
	exitCodeSourceDirError
	exitCodeDestinationDirError
	exitCodeListFilesDirError
	exitCodeSyncError
	exitCodeExclusionFilesError
	exitCodeInvalidExclusions
)

//go:embed default_exclusions.txt
var defaultExclusionsStr string

var flags struct {
	isHelp            func() bool
	getExcludedFiles  func() lib.Set[string]
	isShellScriptMode func() bool
	getListFilesDir   func() bool
	isVerbose         func() bool
	showVersion       func() bool
}

func setupExclusionsOpt() {
	const exclusionsFlag = "exclusions"
	const exclusionsDefaultValue = ""
	defaultExclusions, defaultExclusionsExamples := lib.LineSeparatedStrToMap(defaultExclusionsStr)
	excludesListFilePathPtr := flag.StringP(exclusionsFlag, "x", exclusionsDefaultValue,
		fmt.Sprintf("path to file containing newline separated list of file/directory names to be excluded\n"+
			"(even if this is not set, files/directories such these will still be ignored: %s etc.)",
			strings.Join(defaultExclusionsExamples, ", ")))
	flags.getExcludedFiles = func() lib.Set[string] {
		excludesListFilePath := *excludesListFilePathPtr
		var exclusions lib.Set[string]
		if excludesListFilePath == exclusionsDefaultValue {
			exclusions = defaultExclusions
		} else {
			if !lib.IsReadableFile(excludesListFilePath) {
				fmte.PrintfErr("error: argument to flag --%s should be a file\n", exclusionsFlag)
				flag.Usage()
				os.Exit(exitCodeInvalidExclusions)
			}
			rawContents, err := os.ReadFile(excludesListFilePath)
			if err != nil {
				fmte.PrintfErr("error: argument to flag --%s isn't readable: %+v\n", exclusionsFlag, err)
				flag.Usage()
				os.Exit(exitCodeExclusionFilesError)
			}
			contents := strings.ReplaceAll(string(rawContents), "\r\n", "\n") // Windows
			exclusions, _ = lib.LineSeparatedStrToMap(contents)
		}
		return exclusions
	}
}

func handlePanic() {
	err := recover()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Program exited unexpectedly. "+
			"Please report the below eror to the author:\n"+
			"%+v\n", err)
		_, _ = fmt.Fprintln(os.Stderr, string(debug.Stack()))
	}
}

func setupUsage() {
	flag.Usage = func() {
		fmte.PrintfErr("Run \"rsync-sidekick --help\" for usage\n")
	}
}

func showHelpAndExit() {
	flag.CommandLine.SetOutput(os.Stdout)
	fmt.Printf(`rsync-sidekick is a tool to propagate file renames, movements and timestamp changes from ` +
		`a source directory to a destination directory.

Usage:
	 rsync-sidekick <flags> [source-dir] [destination-dir]

where,
	[source-dir]        Source directory
	[destination-dir]   Destination directory

flags: (all optional)
`)
	flag.PrintDefaults()
	fmt.Printf("\nMore details here: https://github.com/m-manu/rsync-sidekick\n")
	os.Exit(exitCodeSuccess)
}

func setupHelpOpt() {
	helpPtr := flag.BoolP("help", "h", false, "display help")
	flags.isHelp = func() bool {
		return *helpPtr
	}
}

func setupShowVersion() {
	showVersionPtr := flag.Bool("version", false, "show application version ("+applicationVersion+") and exit")
	flags.showVersion = func() bool {
		return *showVersionPtr
	}
}

func setupShellScriptOpt() {
	scriptGenFlagPtr := flag.BoolP("shellscript", "s", false,
		"instead of applying changes directly, generate a shell script\n"+
			"(this flag is useful if you want 'dry run' this tool or want to run the shell script as a different user)",
	)
	flags.isShellScriptMode = func() bool {
		return *scriptGenFlagPtr
	}
}

func setupVerboseOpt() {
	verbosePtr := flag.BoolP("verbose", "v", false,
		"generates extra information, even a file dump (caution: makes it slow!)",
	)
	flags.isVerbose = func() bool {
		return *verbosePtr
	}
}

func setupGetListFilesDir() {
	listFilesDirPtr := flag.Bool("list", false, "list files along their metadata for given directory")
	flags.getListFilesDir = func() bool {
		listFilesDir := *listFilesDirPtr
		return listFilesDir
	}
}

func readSourceAndDestination() (string, string) {
	sourceDirPath, sourceDirErr := filepath.Abs(flag.Arg(0))
	if sourceDirErr != nil || !lib.IsReadableDirectory(sourceDirPath) {
		fmte.PrintfErr("error: source path \"%s\" is not a readable directory\n", flag.Arg(0))
		flag.Usage()
		os.Exit(exitCodeSourceDirError)
	}
	destinationDirPath, destinationDirErr := filepath.Abs(flag.Arg(1))
	if destinationDirErr != nil || !lib.IsReadableDirectory(destinationDirPath) {
		fmte.PrintfErr("error: destination path \"%s\" is not a readable directory\n", flag.Arg(1))
		flag.Usage()
		os.Exit(exitCodeDestinationDirError)
	}
	return sourceDirPath, destinationDirPath
}

func setupFlags() {
	setupHelpOpt()
	setupExclusionsOpt()
	setupShellScriptOpt()
	setupVerboseOpt()
	setupGetListFilesDir()
	setupShowVersion()
	setupUsage()
}

func main() {
	defer handlePanic()
	setupFlags()
	flag.Parse()
	if flag.NArg() == 0 && flag.NFlag() == 0 {
		fmte.Printf("error: no input directories passed\n")
		flag.Usage()
		os.Exit(exitCodeInvalidNumArgs)
	}
	if flags.isHelp() {
		showHelpAndExit()
	}
	if flags.showVersion() {
		fmt.Println(applicationVersion)
		os.Exit(exitCodeSuccess)
	}
	if flag.NArg() != 2 {
		fmte.PrintfErr("error: two arguments expected: source directory path and destination directory path\n")
		flag.Usage()
		os.Exit(exitCodeInvalidNumArgs)
	}
	sourcePath, destinationPath := readSourceAndDestination()
	// List
	listFilesDir := flags.getListFilesDir()
	if listFilesDir {
		excludedFiles := flags.getExcludedFiles()
		err := service.FindDirectoryResultToCsv(sourcePath, excludedFiles, os.Stdout)
		if err == nil {
			os.Exit(exitCodeSuccess)
		} else {
			fmte.PrintfErr("error while creating list: %+v", err)
			os.Exit(exitCodeListFilesDirError)
		}
	}
	runID := time.Now().Format("150405")
	syncErr := rsyncSidekick(runID, sourcePath, flags.getExcludedFiles(), destinationPath, flags.isShellScriptMode(),
		flags.isVerbose())
	if syncErr != nil {
		fmte.PrintfErr("error while syncing: %+v\n", syncErr)
		os.Exit(exitCodeSyncError)
	}
}
