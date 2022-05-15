package main

import (
	_ "embed"
	"flag"
	"fmt"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/m-manu/rsync-sidekick/service"
	"github.com/m-manu/rsync-sidekick/utils"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
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
	getExcludedFiles  func() entity.StringSet
	isShellScriptMode func() bool
	getListFilesDir   func() string
	isExtraInfoOn     func() bool
}

func setupExclusionsOpt() {
	const exclusionsFlag = "exclusions"
	const exclusionsDefaultValue = ""
	defaultExclusions, defaultExclusionsExamples := utils.LineSeparatedStrToMap(defaultExclusionsStr)
	excludesListFilePathPtr := flag.String(exclusionsFlag, exclusionsDefaultValue,
		fmt.Sprintf("path to file containing newline separated list of file/directory names to be excluded\n"+
			"(if this is not set, by default these will be ignored: %s etc.)",
			strings.Join(defaultExclusionsExamples, ", ")))
	flags.getExcludedFiles = func() entity.StringSet {
		excludesListFilePath := *excludesListFilePathPtr
		var exclusions entity.StringSet
		if excludesListFilePath == exclusionsDefaultValue {
			exclusions = defaultExclusions
		} else {
			if !utils.IsReadableFile(excludesListFilePath) {
				fmte.PrintfErr("error: argument to flag -%s should be a file\n", exclusionsFlag)
				flag.Usage()
				os.Exit(exitCodeInvalidExclusions)
			}
			rawContents, err := os.ReadFile(excludesListFilePath)
			if err != nil {
				fmte.PrintfErr("error: argument to flag -%s isn't readable: %+v\n", exclusionsFlag, err)
				flag.Usage()
				os.Exit(exitCodeExclusionFilesError)
			}
			contents := strings.ReplaceAll(string(rawContents), "\r\n", "\n") // Windows
			exclusions, _ = utils.LineSeparatedStrToMap(contents)
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
		fmte.PrintfErr("Run \"rsync-sidekick -help\" for usage\n")
	}
}

func showHelpAndExit() {
	flag.CommandLine.SetOutput(os.Stdout)
	fmt.Printf(`rsync-sidekick is a tool to propagate file renames, movements and timestamp changes from ` +
		`a source directory to a destination directory.

Usage:
	 rsync-sidekick <flags> [source-dir] [destination-dir]

where,
	source-dir        Source directory
	destination-dir   Destination directory

flags: (all optional)
`)
	flag.PrintDefaults()
	fmt.Printf("\nMore details here: https://github.com/m-manu/rsync-sidekick\n")
	os.Exit(exitCodeSuccess)
}

func setupHelpOpt() {
	helpPtr := flag.Bool("help", false, "display help")
	flags.isHelp = func() bool {
		return *helpPtr
	}
}

func setupShellScriptOpt() {
	scriptGenFlagPtr := flag.Bool("shellscript", false,
		"instead of applying changes directly, generate a shell script\n"+
			"(this flag is useful if you want 'dry run' this tool or want to run the shell script as a different user)",
	)
	flags.isShellScriptMode = func() bool {
		return *scriptGenFlagPtr
	}
}

func setupIsExtraInfoOpt() {
	scriptGenFlagPtr := flag.Bool("extrainfo", false,
		"generate extra information (caution: makes it slow!)",
	)
	flags.isExtraInfoOn = func() bool {
		return *scriptGenFlagPtr
	}
}

func setupGetListFilesDir() {
	listFilesDirPtr := flag.String("list", "", "list files along their metadata for given directory")
	flags.getListFilesDir = func() string {
		listFilesDir := *listFilesDirPtr
		if listFilesDir == "" {
			return ""
		}
		listFilesDirPath, listFilesDirErr := filepath.Abs(listFilesDir)
		if listFilesDirErr != nil || !utils.IsReadableDirectory(listFilesDirPath) {
			fmte.PrintfErr("error: list files directory path \"%s\" is not a readable directory\n", listFilesDir)
			flag.Usage()
			os.Exit(exitCodeListFilesDirError)
		}
		return listFilesDirPath
	}
}

func readSourceAndDestination() (string, string) {
	sourceDirPath, sourceDirErr := filepath.Abs(flag.Arg(0))
	if sourceDirErr != nil || !utils.IsReadableDirectory(sourceDirPath) {
		fmte.PrintfErr("error: source path \"%s\" is not a readable directory\n", flag.Arg(0))
		flag.Usage()
		os.Exit(exitCodeSourceDirError)
	}
	destinationDirPath, destinationDirErr := filepath.Abs(flag.Arg(1))
	if destinationDirErr != nil || !utils.IsReadableDirectory(destinationDirPath) {
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
	setupIsExtraInfoOpt()
	setupGetListFilesDir()
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
	listFilesDir := flags.getListFilesDir()
	if listFilesDir != "" && flag.NArg() == 0 {
		file := os.Stdout
		excludedFiles := flags.getExcludedFiles()
		service.FindDirectoryResultToCsv(listFilesDir, excludedFiles, file)
		os.Exit(exitCodeSuccess)
	}
	if flag.NArg() != 2 {
		fmte.PrintfErr("error: two arguments expected: source directory path and destination directory path\n")
		flag.Usage()
		os.Exit(exitCodeInvalidNumArgs)
	}
	sourcePath, destinationPath := readSourceAndDestination()
	syncErr := rsyncSidekick(sourcePath, flags.getExcludedFiles(), destinationPath, flags.isShellScriptMode(),
		flags.isExtraInfoOn())
	if syncErr != nil {
		fmte.PrintfErr("error while syncing: %+v\n", syncErr)
		os.Exit(exitCodeSyncError)
	}
}
