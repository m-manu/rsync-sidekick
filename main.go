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
	"github.com/m-manu/rsync-sidekick/v2/bytesutil"
	"github.com/m-manu/rsync-sidekick/v2/fmte"
	rsfs "github.com/m-manu/rsync-sidekick/v2/fs"
	"github.com/m-manu/rsync-sidekick/v2/lib"
	"github.com/m-manu/rsync-sidekick/v2/remote"
	"github.com/m-manu/rsync-sidekick/v2/service"
	flag "github.com/spf13/pflag"
)

const (
	applicationMajorVersion = 2
	applicationMinorVersion = 17
	applicationPatchVersion = 5
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
	exitCodeArchivePathError
	exitCodeInvalidMinSize
)

//go:embed default_exclusions.txt
var defaultExclusionsStr string

var flags struct {
	isHelp               func() bool
	getExcludedFiles     func() set.Set[string]
	isShellScriptMode    func() bool
	scriptOutputPath     func() string
	getListFilesDir      func() bool
	isVerbose            func() bool
	showVersion          func() bool
	isDryRun             func() bool
	progressFrequency    func() time.Duration
	sshKeyPath           func() string
	sidekickPath         func() string
	isSFTP               func() bool
	isAgent              func() bool
	syncDirTimestamps    func() bool
	copyDuplicates       func() bool
	useReflink           func() bool
	archivePaths         func() []string
	oneFileSystem        func() bool
	archiveOneFileSystem func() bool
	minSize              func() int64
	ignoreExtension      func() bool
	digestCache          func() digestCacheOptions
	copyPlan             func() copyPlanOutput
	applyPlanPath        func() string
	includeDirs          func() ([]string, error)
	hashMinSize          func() int64
	walkThreads          func() int
}

// defaultExclusionList returns the names of the default exclusions in file order.
func defaultExclusionList(contents string) []string {
	var names []string
	for _, line := range strings.Split(contents, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// wrapList joins names with ", " into lines of at most width characters (a longer name
// gets a line of its own).
func wrapList(names []string, width int) string {
	var lines []string
	line := ""
	for i, name := range names {
		item := name
		if i < len(names)-1 {
			item += ","
		}
		switch {
		case line == "":
			line = item
		case len(line)+1+len(item) <= width:
			line += " " + item
		default:
			lines = append(lines, line)
			line = item
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func setupExclusionsOpt() {
	const exclusionsFlag = "exclusions"
	const exclusionsDefaultValue = ""
	defaultExclusions, _ := lib.LineSeparatedStrToMap(defaultExclusionsStr)
	excludesListFilePathPtr := flag.StringP(exclusionsFlag, "x", exclusionsDefaultValue,
		"path to file containing newline separated list of file/directory names to be excluded\n"+
			"(names are matched anywhere in the tree). Without this flag these are ignored:\n"+
			wrapList(defaultExclusionList(defaultExclusionsStr), 100)+"\n"+
			"A list file replaces them - copy them into it to keep ignoring them.")
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
		"logs every single action performed, plus extra information (caution: makes it slow!)",
	)
	flags.isVerbose = func() bool {
		return *verbosePtr
	}
}

func setupProgressFrequencyOpt() {
	progressFrequencyPtr := flag.DurationP("progress-frequency", "f", 5*time.Second, "frequency of progress reporting e.g. '5s', '1m'")
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
	_ = flag.CommandLine.MarkHidden("agent")
}

func setupIgnoreExtensionOpt() {
	ignoreExtPtr := flag.Bool("ignore-extension", false,
		"match files by size and content only, not by file extension\n"+
			"(finds copies whose names differ, e.g. an archive that names files by their hash)")
	flags.ignoreExtension = func() bool {
		return *ignoreExtPtr
	}
}

func setupMinSizeOpt() {
	const minSizeFlag = "min-size"
	minSizePtr := flag.String(minSizeFlag, "",
		"ignore files smaller than this size, e.g. '1M', '512k', '2g'\n"+
			"(they are left out of every directory scan, so they are never hashed;\n"+
			"rsync transfers them normally - useful when small files dominate the count)")
	flags.minSize = func() int64 {
		if *minSizePtr == "" {
			return 0
		}
		size, err := bytesutil.ParseBinarySize(*minSizePtr)
		if err != nil {
			fmte.PrintfErr("error: argument to flag --%s is invalid: %+v\n", minSizeFlag, err)
			flag.Usage()
			os.Exit(exitCodeInvalidMinSize)
		}
		return size
	}
}

func setupHashMinSizeOpt() {
	const hashMinSizeFlag = "hash-min-size"
	hashMinSizePtr := flag.String(hashMinSizeFlag, "",
		"never hash files smaller than this size, e.g. '512k' - unlike --min-size they stay in the scans:\n"+
			"rsync transfers them, and with --copy-list they go straight into the list (no second rsync pass)")
	flags.hashMinSize = func() int64 {
		if *hashMinSizePtr == "" {
			return 0
		}
		size, err := bytesutil.ParseBinarySize(*hashMinSizePtr)
		if err != nil {
			fmte.PrintfErr("error: argument to flag --%s is invalid: %+v\n", hashMinSizeFlag, err)
			flag.Usage()
			os.Exit(exitCodeInvalidMinSize)
		}
		return size
	}
}

func setupWalkThreadsOpt() {
	const walkThreadsFlag = "walk-threads"
	walkThreadsPtr := flag.Int(walkThreadsFlag, rsfs.DefaultWalkThreads,
		"directories read at once per scan (local and remote) - several outstanding\n"+
			"requests keep all disks of an array busy; 1 reads one directory after the other")
	flags.walkThreads = func() int {
		if *walkThreadsPtr < 1 {
			fmte.PrintfErr("error: argument to flag --%s must be at least 1\n", walkThreadsFlag)
			flag.Usage()
			os.Exit(exitCodeInvalidNumArgs)
		}
		return *walkThreadsPtr
	}
}

func setupDigestCacheOpt() {
	const digestCacheFlag = "digest-cache"
	modePtr := flag.String(digestCacheFlag, "off",
		"reuse digests from earlier runs: --digest-cache (both sides), --digest-cache=src,\n"+
			"--digest-cache=dst (destination and archive paths) or --digest-cache=off; a file is hashed\n"+
			"again only when its size, mtime, ctime or inode changed (one cache per host, default\n"+
			"~/.cache/rsync-sidekick/digests.tsv; a read-only cache file is used without saving new digests)")
	flag.Lookup(digestCacheFlag).NoOptDefVal = "on"
	pathPtr := flag.String("digest-cache-path", "",
		"digest cache file on this host - never used on the remote host (without --digest-cache it\n"+
			"turns the cache on for the sides on this host)")
	remotePathPtr := flag.String("remote-digest-cache-path", "",
		"digest cache file on the remote host, remote-exec only (without --digest-cache it turns the\n"+
			"cache on for the remote side)")
	flags.digestCache = func() digestCacheOptions {
		switch *modePtr {
		case "on", "src", "dst", "off":
		default:
			fmte.PrintfErr("error: argument to flag --%s must be one of on, src, dst, off\n", digestCacheFlag)
			flag.Usage()
			os.Exit(exitCodeInvalidNumArgs)
		}
		return digestCacheOptions{mode: *modePtr, modeSet: flag.CommandLine.Changed(digestCacheFlag),
			localPath: *pathPtr, remotePath: *remotePathPtr}
	}
}

// digestCacheOptions are the digest cache flags as given.
type digestCacheOptions struct {
	mode                  string
	modeSet               bool
	localPath, remotePath string
}

// sides decides which sides keep their digests across runs. --digest-cache names them by
// role; without it, a path flag turns the cache on for the sides on its host. A path for
// a host whose sides are all off is reported as ignored.
func (o digestCacheOptions) sides(sourceIsLocal, destIsLocal bool) (source, dest bool, warnings []string) {
	if o.modeSet {
		source = o.mode == "on" || o.mode == "src"
		dest = o.mode == "on" || o.mode == "dst"
	} else {
		onHost := func(isLocal bool) bool {
			return (isLocal && o.localPath != "") || (!isLocal && o.remotePath != "")
		}
		source, dest = onHost(sourceIsLocal), onHost(destIsLocal)
	}
	localOn := (source && sourceIsLocal) || (dest && destIsLocal)
	remoteOn := (source && !sourceIsLocal) || (dest && !destIsLocal)
	if o.localPath != "" && !localOn {
		warnings = append(warnings, "--digest-cache-path is ignored: the cache is off for the sides on this host")
	}
	if o.remotePath != "" && !remoteOn {
		warnings = append(warnings, "--remote-digest-cache-path is ignored: the cache is off for the remote side")
	}
	return source, dest, warnings
}

// openDigestCache opens the digest cache of this host, keeping digests across runs only
// below persistentRoots; without any it caches within this run only.
func openDigestCache(persistentRoots []string) {
	if len(persistentRoots) == 0 {
		service.SetDigestCache(service.NewMemoryDigestCache())
		return
	}
	path := flags.digestCache().localPath
	if path == "" {
		defaultPath, err := service.DefaultDigestCachePath()
		if err != nil {
			fmte.PrintfErr("warning: no default location for the digest cache (%+v) - digests are not cached\n", err)
			service.SetDigestCache(service.NewMemoryDigestCache())
			return
		}
		path = defaultPath
	}
	c, err := service.OpenDigestCache(path, persistentRoots)
	if err != nil {
		fmte.PrintfErr("warning: %+v - digests are not cached\n", err)
		service.SetDigestCache(service.NewMemoryDigestCache())
		return
	}
	service.SetDigestCache(c)
}

func closeDigestCache() {
	c := service.ActiveDigestCache()
	if c == nil {
		return
	}
	service.SetDigestCache(nil)
	hits, misses := c.Stats()
	if !c.Persistent() {
		if hits > 0 {
			fmte.Printf("Digests reused within this run: %s (computed: %s)\n",
				lib.GroupThousands(hits), lib.GroupThousands(misses))
		}
		return
	}
	fmte.Printf("Digest cache %s: %s reused, %s computed\n", c.Path(),
		lib.GroupThousands(hits), lib.GroupThousands(misses))
	if err := c.Close(); err != nil {
		fmte.PrintfErr("warning: couldn't save digest cache %s: %+v\n", c.Path(), err)
	}
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
			"implies --copy-duplicates; shell wildcards like '/snapshots/*' are resolved on the\n"+
			"destination host, matches in lexical order - quote them so the local shell leaves them alone)")
	flags.archivePaths = func() []string {
		return *archivePathsPtr
	}
}

func setupOneFileSystemOpt() {
	oneFileSystemPtr := flag.Bool("one-file-system", false,
		"don't cross filesystem boundaries when scanning source and destination (like rsync -x)\n"+
			"(works locally and with remote-exec, not with SFTP)")
	flags.oneFileSystem = func() bool {
		return *oneFileSystemPtr
	}
	archiveOneFileSystemPtr := flag.Bool("archive-one-file-system", false,
		"don't cross filesystem boundaries when scanning archive paths\n"+
			"(by default archives DO cross boundaries, e.g. into btrfs snapshot subvols)\n"+
			"(works locally and with remote-exec, not with SFTP)")
	flags.archiveOneFileSystem = func() bool {
		return *archiveOneFileSystemPtr
	}
}

func setupCopyPlanOpt() {
	copyListPtr := flag.String("copy-list", "",
		"write the files rsync still has to transfer to this file, one per distinct content\n"+
			"(for rsync --files-from; duplicates at source go to --plan-out instead; remote-exec with remote source only)")
	planOutPtr := flag.String("plan-out", "",
		"write the duplicate groups of the --copy-list files to this file (JSON lines),\n"+
			"to be reflinked with --apply-plan once rsync transferred the originals")
	applyPlanPtr := flag.String("apply-plan", "",
		"reflink the duplicates of a plan written by --plan-out; takes the destination directory as only argument")
	flags.copyPlan = func() copyPlanOutput {
		return copyPlanOutput{CopyListPath: *copyListPtr, PlanPath: *planOutPtr}
	}
	flags.applyPlanPath = func() string {
		return *applyPlanPtr
	}
}

func setupIncludeDirOpt() {
	includeDirPtr := flag.StringArray("include-dir", nil,
		"only scan this directory below source and destination root (relative path; can be specified multiple\n"+
			"times; shell wildcards like 'Backup*' or 'Backup/*/data' are allowed; archive paths are not limited)")
	includeFromPtr := flag.String("include-from", "",
		"read --include-dir entries from this file, one per line (empty lines and lines starting with # are ignored)")
	flags.includeDirs = func() ([]string, error) {
		dirs := append([]string{}, *includeDirPtr...)
		if *includeFromPtr != "" {
			fromFile, err := readIncludeFile(*includeFromPtr)
			if err != nil {
				return nil, fmt.Errorf("cannot read --include-from file: %w", err)
			}
			dirs = append(dirs, fromFile...)
		}
		return normalizeIncludeDirs(dirs)
	}
}

// expandArchivePaths resolves wildcards in archive paths with glob and exits on failure.
// A pattern matching nothing is an error like an unreadable archive path: either way a
// typo would silently turn local copies into network transfers.
func expandArchivePaths(paths []string, glob func([]string) ([][]string, error)) []string {
	expanded, unmatched, err := lib.ExpandPaths(paths, glob)
	if err != nil {
		fmte.PrintfErr("error: cannot resolve --archive-path wildcards: %+v\n", err)
		os.Exit(exitCodeArchivePathError)
	}
	if len(unmatched) > 0 {
		fmte.PrintfErr("error: --archive-path \"%s\" matches no directory\n", unmatched[0])
		os.Exit(exitCodeArchivePathError)
	}
	if len(expanded) != len(paths) {
		fmte.Printf("Archive paths: %s after resolving wildcards\n", lib.GroupThousands(len(expanded)))
		fmte.PrintfV("Archive paths: %s\n", strings.Join(expanded, ", "))
	}
	return expanded
}

func setupFlags() {
	setupHelpOpt()
	setupExclusionsOpt()
	setupShellScriptOpt()
	setupShellScriptWithNameOpt()
	setupVerboseOpt()
	setupProgressFrequencyOpt()
	setupMinSizeOpt()
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
	setupOneFileSystemOpt()
	setupDigestCacheOpt()
	setupIgnoreExtensionOpt()
	setupCopyPlanOpt()
	setupIncludeDirOpt()
	setupHashMinSizeOpt()
	setupWalkThreadsOpt()
	setupUsage()
}

func main() {
	defer handlePanic()
	setupFlags()
	flag.Parse()

	// Agent mode: run as remote agent (reads from stdin, writes to stdout)
	if flags.isAgent() {
		if err := remote.RunAgent(applicationVersion); err != nil {
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
	if planPath := flags.applyPlanPath(); planPath != "" {
		if flag.NArg() != 1 || !lib.IsReadableDirectory(flag.Arg(0)) {
			fmte.PrintfErr("error: --apply-plan expects the destination directory as only argument\n")
			os.Exit(exitCodeInvalidNumArgs)
		}
		if err := runApplyPlan(planPath, flag.Arg(0), flags.isDryRun(), flags.progressFrequency()); err != nil {
			fmte.PrintfErr("error while applying plan: %+v\n", err)
			os.Exit(exitCodeSyncError)
		}
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

	// Archive paths belong to the destination side, so they can only be checked up front
	// when that side is local. Checking here — before any scanning starts — is what makes
	// a typo visible at all: silently skipping one turns instant local reflink copies into
	// a full transfer over the network, which is not something to bury in a warning.
	archivePaths := flags.archivePaths()
	if !destLoc.IsRemote {
		archivePaths = expandArchivePaths(archivePaths, lib.GlobDirsAll)
		for _, archivePath := range archivePaths {
			if !lib.IsReadableDirectory(archivePath) {
				fmte.PrintfErr("error: --archive-path \"%s\" is not a readable directory\n", archivePath)
				os.Exit(exitCodeArchivePathError)
			}
		}
	}

	if flags.ignoreExtension() {
		lib.IgnoreFileExtension = true
	}

	dirs, includeErr := flags.includeDirs()
	if includeErr != nil {
		fmte.PrintfErr("error: %+v\n", includeErr)
		os.Exit(exitCodeInvalidNumArgs)
	}
	includeDirs = dirs
	if len(includeDirs) > 0 {
		fmte.Printf("Scanning only: %s\n", strings.Join(includeDirs, ", "))
	}

	// --min-size applies to every walk, local and remote alike.
	if minSize := flags.minSize(); minSize > 0 {
		rsfs.DefaultMinSize = minSize
		fmte.Printf("Ignoring files smaller than %s\n", bytesutil.BinaryFormat(minSize))
	}
	rsfs.DefaultWalkThreads = flags.walkThreads()
	if size := flags.hashMinSize(); size > 0 {
		hashMinSize = size
		fmte.Printf("Not hashing files smaller than %s\n", bytesutil.BinaryFormat(size))
	}

	// Set --one-file-system defaults for LocalFS instances
	if flags.oneFileSystem() {
		rsfs.DefaultOneFileSystem = true
	}
	if flags.archiveOneFileSystem() {
		rsfs.DefaultArchiveOneFileSystem = true
	}

	// If both are local, use the original flow
	if !sourceLoc.IsRemote && !destLoc.IsRemote {
		if flags.copyPlan().enabled() {
			fmte.PrintfErr("error: --copy-list and --plan-out need a remote source (remote-exec mode)\n")
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

		copyDup := flags.copyDuplicates() || len(archivePaths) > 0
		cacheSource, cacheDest, cacheWarnings := flags.digestCache().sides(true, true)
		for _, w := range cacheWarnings {
			fmte.PrintfErr("warning: %s\n", w)
		}
		var cacheRoots []string
		if cacheSource {
			cacheRoots = append(cacheRoots, sourcePath)
		}
		if cacheDest {
			cacheRoots = append(append(cacheRoots, destinationPath), archivePaths...)
		}
		openDigestCache(cacheRoots)
		syncErr := rsyncSidekick(runID, sourcePath, flags.getExcludedFiles(), destinationPath, scriptOutputPath,
			flags.isVerbose(), flags.isDryRun(), flags.syncDirTimestamps(), flags.progressFrequency(),
			copyDup, flags.useReflink(), archivePaths)
		closeDigestCache()
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

	// Wildcards in archive paths of a remote destination are resolved on that host.
	if destLoc.IsRemote {
		archivePaths = expandArchivePaths(archivePaths, func(patterns []string) ([][]string, error) {
			if agentClient == nil {
				return nil, fmt.Errorf("wildcards in --archive-path need remote-execution mode (not SFTP)")
			}
			return agentClient.Glob(patterns)
		})
	}

	// Warn if --one-file-system is used with SFTP (forced or fallback)
	if flags.oneFileSystem() && agentClient == nil {
		fmte.PrintfErr("warning: --one-file-system has no effect on SFTP paths\n")
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

	copyDup := flags.copyDuplicates() || len(archivePaths) > 0
	localRoots := []string{absLocalPath}
	remoteRoots := []string{remoteLoc.Path}
	if sourceLoc.IsRemote {
		localRoots = append(localRoots, archivePaths...)
	} else {
		remoteRoots = append(remoteRoots, archivePaths...)
	}
	cacheSource, cacheDest, cacheWarnings := flags.digestCache().sides(!sourceLoc.IsRemote, !destLoc.IsRemote)
	for _, w := range cacheWarnings {
		fmte.PrintfErr("warning: %s\n", w)
	}
	cacheLocal, cacheRemote := cacheDest, cacheSource
	if !sourceLoc.IsRemote {
		cacheLocal, cacheRemote = cacheSource, cacheDest
	}
	if !cacheLocal {
		localRoots = nil
	}
	openDigestCache(localRoots)
	if cacheRemote {
		if agentClient != nil {
			agentClient.DigestCache = &remote.DigestCacheSpec{Path: flags.digestCache().remotePath, Roots: remoteRoots}
		} else {
			fmte.PrintfErr("warning: the digest cache only covers the local side in SFTP mode\n")
		}
	}
	var syncErr error
	if sourceLoc.IsRemote {
		syncErr = rsyncSidekickRemote(runID, remoteLoc, absLocalPath, true,
			flags.sshKeyPath(), agentClient, flags.getExcludedFiles(), scriptOutputPath,
			flags.isVerbose(), flags.isDryRun(), flags.syncDirTimestamps(), flags.progressFrequency(),
			copyDup, flags.useReflink(), archivePaths, flags.copyPlan())
	} else {
		syncErr = rsyncSidekickRemote(runID, remoteLoc, absLocalPath, false,
			flags.sshKeyPath(), agentClient, flags.getExcludedFiles(), scriptOutputPath,
			flags.isVerbose(), flags.isDryRun(), flags.syncDirTimestamps(), flags.progressFrequency(),
			copyDup, flags.useReflink(), archivePaths, flags.copyPlan())
	}
	closeDigestCache()
	if syncErr != nil {
		fmte.PrintfErr("error while syncing: %+v\n", syncErr)
		os.Exit(exitCodeSyncError)
	}
}
