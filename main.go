package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/fmte"
	"github.com/m-manu/rsync-sidekick/lib"
	"github.com/m-manu/rsync-sidekick/remote"
	"github.com/m-manu/rsync-sidekick/service"
	flag "github.com/spf13/pflag"
)

const (
	applicationMajorVersion = 1
	applicationMinorVersion = 10
	applicationPatchVersion = 8
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
	exitCodeScriptPathError
	exitCodeSSHError
)

//go:embed default_exclusions.txt
var defaultExclusionsStr string

var flags struct {
	isHelp            func() bool
	getExcludedFiles  func() set.Set[string]
	isShellScriptMode func() bool
	scriptOutputPath  func() string
	getListFilesDir   func() bool
	isVerbose         func() bool
	showVersion       func() bool
	isDryRun          func() bool
	progressFrequency func() time.Duration
	sshKeyPath        func() string
	sidekickPath      func() string
	isSFTP            func() bool
	isAgent           func() bool
	syncDirTimestamps func() bool
	copyDuplicates    func() bool
	useReflink        func() bool
	archivePaths      func() []string
}

func setupExclusionsOpt() {
	const exclusionsFlag = "exclusions"
	const exclusionsDefaultValue = ""
	defaultExclusions, defaultExclusionsExamples := lib.LineSeparatedStrToMap(defaultExclusionsStr)
	excludesListFilePathPtr := flag.StringP(exclusionsFlag, "x", exclusionsDefaultValue,
		fmt.Sprintf("path to file containing newline separated list of file/directory names to be excluded\n"+
			"(even if this is not set, files/directories such these will still be ignored: %s etc.)",
			strings.Join(defaultExclusionsExamples, ", ")))
	flags.getExcludedFiles = func() set.Set[string] {
		excludesListFilePath := *excludesListFilePathPtr
		var exclusions set.Set[string]
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
	 rsync-sidekick <flags> [source] [destination]

where,
	[source]        Source directory (local path or user@host:/path)
	[destination]   Destination directory (local path or user@host:/path)

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

const (
	shellScript       = "shellscript"
	shellScriptAtPath = "shellscript-at-path"
)

func setupShellScriptOpt() {
	scriptGenFlagPtr := flag.BoolP(shellScript, "s", false,
		"instead of applying changes directly, generate a shell script\n"+
			"(this flag is useful if you want to run the shell script as a different user)",
	)
	flags.isShellScriptMode = func() bool {
		return *scriptGenFlagPtr
	}
}

func setupShellScriptWithNameOpt() {
	scriptOutputPathPtr := flag.StringP(shellScriptAtPath, "p", "",
		"similar to --"+shellScript+" option but you can specify output script path\n"+
			"(this flag cannot be specified if --"+shellScript+" option is specified)",
	)
	flags.scriptOutputPath = func() string {
		return *scriptOutputPathPtr
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

func setupProgressFrequencyOpt() {
	progressFrequencyPtr := flag.DurationP("progress-frequency", "f", 2*time.Second, "frequency of progress reporting e.g. '5s', '1m'")
	flags.progressFrequency = func() time.Duration {
		return *progressFrequencyPtr
	}
}

func setupGetListFilesDir() {
	listFilesDirPtr := flag.Bool("list", false, "list files along their metadata for given directory")
	flags.getListFilesDir = func() bool {
		listFilesDir := *listFilesDirPtr
		return listFilesDir
	}
}

func setupSSHKeyOpt() {
	sshKeyPtr := flag.StringP("ssh-key", "i", "", "path to SSH private key for remote connections")
	flags.sshKeyPath = func() string {
		return *sshKeyPtr
	}
}

func setupSidekickPathOpt() {
	sidekickPathPtr := flag.String("sidekick-path", "rsync-sidekick",
		"remote rsync-sidekick command (e.g. \"sudo rsync-sidekick\")")
	flags.sidekickPath = func() string {
		return *sidekickPathPtr
	}
}

func setupSFTPOpt() {
	sftpPtr := flag.Bool("sftp", false, "force SFTP mode (don't try remote-execution)")
	flags.isSFTP = func() bool {
		return *sftpPtr
	}
}

func setupAgentOpt() {
	agentPtr := flag.Bool("agent", false, "run in agent mode (used internally for remote-execution)")
	flags.isAgent = func() bool {
		return *agentPtr
	}
	flag.CommandLine.MarkHidden("agent")
}

func setupSyncDirTimestampsOpt() {
	syncDirTsPtr := flag.BoolP("sync-dir-timestamps", "d", false,
		"also propagate directory timestamps from source to destination")
	flags.syncDirTimestamps = func() bool {
		return *syncDirTsPtr
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

func setupDryRunOpt() {
	dryRunPtr := flag.BoolP("dry-run", "n", false,
		"show what would be done, but don't actually perform any actions",
	)
	flags.isDryRun = func() bool {
		return *dryRunPtr
	}
}

func setupCopyDuplicatesOpt() {
	copyDupPtr := flag.BoolP("copy-duplicates", "c", false,
		"copy files locally at destination when content already exists there\n"+
			"(avoids re-transfer of duplicate-content files via rsync)")
	flags.copyDuplicates = func() bool {
		return *copyDupPtr
	}
}

func setupReflinkOpt() {
	reflinkPtr := flag.Bool("reflink", false,
		"use cp --reflink=auto for copy actions (instant on CoW filesystems like btrfs/XFS)\n"+
			"(only effective when copies are performed via --copy-duplicates or --archive-path)")
	flags.useReflink = func() bool {
		return *reflinkPtr
	}
}

func setupArchivePathOpt() {
	archivePathsPtr := flag.StringArrayP("archive-path", "a", nil,
		"additional directory on the destination side to scan for copy sources\n"+
			"(can be specified multiple times; files are copied from archive, never moved;\n"+
			"implies --copy-duplicates)")
	flags.archivePaths = func() []string {
		return *archivePathsPtr
	}
}

func setupFlags() {
	setupHelpOpt()
	setupExclusionsOpt()
	setupShellScriptOpt()
	setupShellScriptWithNameOpt()
	setupVerboseOpt()
	setupProgressFrequencyOpt()
	setupGetListFilesDir()
	setupShowVersion()
	setupDryRunOpt()
	setupSSHKeyOpt()
	setupSidekickPathOpt()
	setupSFTPOpt()
	setupAgentOpt()
	setupSyncDirTimestampsOpt()
	setupCopyDuplicatesOpt()
	setupReflinkOpt()
	setupArchivePathOpt()
	setupUsage()
}

func main() {
	defer handlePanic()
	setupFlags()
	flag.Parse()

	// Agent mode: run as remote agent (reads from stdin, writes to stdout)
	if flags.isAgent() {
		if err := remote.RunAgent(); err != nil {
			fmte.PrintfErr("agent error: %+v\n", err)
			os.Exit(exitCodeSyncError)
		}
		os.Exit(exitCodeSuccess)
	}

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

	// Parse locations (local or remote)
	sourceLoc, srcErr := remote.ParseLocation(flag.Arg(0))
	if srcErr != nil {
		fmte.PrintfErr("error: invalid source: %+v\n", srcErr)
		os.Exit(exitCodeSourceDirError)
	}
	destLoc, dstErr := remote.ParseLocation(flag.Arg(1))
	if dstErr != nil {
		fmte.PrintfErr("error: invalid destination: %+v\n", dstErr)
		os.Exit(exitCodeDestinationDirError)
	}
	if sourceLoc.IsRemote && destLoc.IsRemote {
		fmte.PrintfErr("error: only one of source or destination can be remote\n")
		os.Exit(exitCodeInvalidNumArgs)
	}

	// If both are local, use the original flow
	if !sourceLoc.IsRemote && !destLoc.IsRemote {
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
		if flags.isShellScriptMode() && flags.scriptOutputPath() != "" {
			fmte.PrintfErr("error: flags --%s and --%s are both specified (you can only specify one of them)", shellScript, shellScriptAtPath)
			os.Exit(exitCodeScriptPathError)
		}

		runID := time.Now().Format("060102_150405")

		var scriptOutputPath string
		if flags.isShellScriptMode() {
			scriptOutputPath = fmt.Sprintf("./sync_actions_%s.sh", runID)
		} else if flags.scriptOutputPath() != "" {
			scriptOutputPath = flags.scriptOutputPath()
		}

		copyDup := flags.copyDuplicates() || len(flags.archivePaths()) > 0
		syncErr := rsyncSidekick(runID, sourcePath, flags.getExcludedFiles(), destinationPath, scriptOutputPath,
			flags.isVerbose(), flags.isDryRun(), flags.syncDirTimestamps(), flags.progressFrequency(),
			copyDup, flags.useReflink(), flags.archivePaths())
		if syncErr != nil {
			fmte.PrintfErr("error while syncing: %+v\n", syncErr)
			os.Exit(exitCodeSyncError)
		}
		return
	}

	// Remote mode
	remoteLoc := sourceLoc
	if destLoc.IsRemote {
		remoteLoc = destLoc
	}

	fmte.Printf("Connecting to %s...\n", remoteLoc.SSHSpec())

	// Determine mode: remote-execution or SFTP
	agentClient, setupErr := remote.SetupRemote(remoteLoc, flags.sshKeyPath(), flags.sidekickPath(), flags.isSFTP())
	if setupErr != nil {
		fmte.PrintfErr("error: %+v\n", setupErr)
		os.Exit(exitCodeSSHError)
	}
	if agentClient != nil {
		defer agentClient.Close()
	}

	// Validate local side
	localPath := sourceLoc.Path
	if sourceLoc.IsRemote {
		localPath = destLoc.Path
	}
	absLocalPath, absErr := filepath.Abs(localPath)
	if absErr != nil || !lib.IsReadableDirectory(absLocalPath) {
		fmte.PrintfErr("error: local path \"%s\" is not a readable directory\n", localPath)
		if sourceLoc.IsRemote {
			os.Exit(exitCodeDestinationDirError)
		}
		os.Exit(exitCodeSourceDirError)
	}

	if flags.isShellScriptMode() && flags.scriptOutputPath() != "" {
		fmte.PrintfErr("error: flags --%s and --%s are both specified (you can only specify one of them)", shellScript, shellScriptAtPath)
		os.Exit(exitCodeScriptPathError)
	}

	runID := time.Now().Format("060102_150405")

	var scriptOutputPath string
	if flags.isShellScriptMode() {
		scriptOutputPath = fmt.Sprintf("./sync_actions_%s.sh", runID)
	} else if flags.scriptOutputPath() != "" {
		scriptOutputPath = flags.scriptOutputPath()
	}

	copyDup := flags.copyDuplicates() || len(flags.archivePaths()) > 0
	var syncErr error
	if sourceLoc.IsRemote {
		syncErr = rsyncSidekickRemote(runID, remoteLoc, absLocalPath, true,
			flags.sshKeyPath(), agentClient, flags.getExcludedFiles(), scriptOutputPath,
			flags.isVerbose(), flags.isDryRun(), flags.syncDirTimestamps(), flags.progressFrequency(),
			copyDup, flags.useReflink(), flags.archivePaths())
	} else {
		syncErr = rsyncSidekickRemote(runID, remoteLoc, absLocalPath, false,
			flags.sshKeyPath(), agentClient, flags.getExcludedFiles(), scriptOutputPath,
			flags.isVerbose(), flags.isDryRun(), flags.syncDirTimestamps(), flags.progressFrequency(),
			copyDup, flags.useReflink(), flags.archivePaths())
	}
	if syncErr != nil {
		fmte.PrintfErr("error while syncing: %+v\n", syncErr)
		os.Exit(exitCodeSyncError)
	}
}
