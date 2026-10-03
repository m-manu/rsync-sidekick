# rsync-sidekick

[![build-and-test](https://github.com/m-manu/rsync-sidekick/actions/workflows/build-and-test.yml/badge.svg)](https://github.com/m-manu/rsync-sidekick/actions/workflows/build-and-test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/m-manu/rsync-sidekick/v2)](https://goreportcard.com/report/github.com/m-manu/rsync-sidekick/v2)
[![Go Reference](https://pkg.go.dev/badge/github.com/m-manu/rsync-sidekick/v2.svg)](https://pkg.go.dev/github.com/m-manu/rsync-sidekick/v2)
[![License](https://img.shields.io/badge/License-Apache%202-blue.svg)](./LICENSE)

## Why?

`rsync` is a fantastic tool. Yet, by itself, it's a pain to use for repeated backing up of media files (videos, music,
photos, etc.) _that are reorganized frequently_. Why? Because, reorganized files get retransferred by `rsync`, wasting a
lot of time and infra costs.

`rsync-sidekick` is a safe and simple tool that is designed to run **before** `rsync` is run. `rsync-sidekick` is *not*
a replacement for `rsync` doesn't intend to be one.

## What?

`rsync-sidekick` propagates following changes (or any combination) from _source directory_ to _destination directory_:

1. Change in file modification timestamp
2. Rename of file/directory
3. Moving a file from one directory to another

Additionally, it does the following things:

1. Directory timestamp synchronization (with `-d` flag)
2. Local copying of duplicate-content files at the destination (with `-c` flag)
3. Copying from archive/backup directories on the destination side (with `-a` flag)

It works with **local directories**, **remote hosts via SSH** (using a remote agent or SFTP fallback) and even inside
**Docker containers**! 🙂

## What this isn't

* This tool **does not delete** any files or folders (under any circumstances) — that's why it's safe to use 😌
    * Your files are just _moved around_
    * Now, if you're uncomfortable with this tool even moving your files around, consider using the `--dry-run` option
* This tool **does not** actually **transfer** files — that's for `rsync` to do 🙂
* Since you'd run `rsync` after this tool is run, any changes that this tool couldn't propagate would just be propagated
  by `rsync`
    * So the most that you might lose is some time with `rsync` doing more work than it could have — Which is likely
      still much less than not using this tool at all 😄

## How to install?

1. Install Go version at least **1.25**
    * On Mac: `brew install go`
    * On Ubuntu: `snap install go`
    * For anything else: [Go downloads page](https://go.dev/dl/)
2. Run command:
   ```bash
   go install github.com/m-manu/rsync-sidekick/v2@latest
   ```
3. Add following line in your `.bashrc`/`.zshrc` file:
   ```bash
   export PATH="$PATH:$HOME/go/bin"
   ```

## How to use?

Just two simple steps:

**Step 1**: Run this tool

```bash
# Local to local:
rsync-sidekick /Users/manu/Photos/ /Volumes/Portable/Photos/

# Local to remote (faster if rsync-sidekick is also installed on remote host):
rsync-sidekick /Users/manu/Photos/ user@server:/backup/Photos/

# Remote to local:
rsync-sidekick user@server:/data/Photos/ /Users/manu/Photos/
```

**Step 2**: Run `rsync` as you would normally do

```bash
# Note the trailing slashes below. Without them, rsync's behavior is different!
rsync -av /Users/manu/Photos/ /Volumes/Portable/Photos/
```

## Command line options

Running `rsync-sidekick --help` displays following information:

```
rsync-sidekick is a tool to propagate file renames, movements and timestamp changes from a source directory to a destination directory.

Usage:
	 rsync-sidekick <flags> [source] [destination]

where,
	[source]        Source directory (local path or user@host:/path)
	[destination]   Destination directory (local path or user@host:/path)

flags: (all optional)
      --apply-plan string                 reflink the duplicates of a plan written by --plan-out; takes the destination directory as only argument
      --archive-one-file-system           don't cross filesystem boundaries when scanning archive paths
                                          (by default archives DO cross boundaries, e.g. into btrfs snapshot subvols)
                                          (works locally and with remote-exec, not with SFTP)
  -a, --archive-path stringArray          additional directory on the destination side to scan for copy sources
                                          (can be specified multiple times; files are copied from archive, never moved;
                                          implies --copy-duplicates; shell wildcards like '/snapshots/*' are resolved on the
                                          destination host, matches in lexical order - quote them so the local shell leaves them alone)
  -c, --copy-duplicates                   copy files locally at destination when content already exists there
                                          (avoids re-transfer of duplicate-content files via rsync)
      --copy-list string                  write the files rsync still has to transfer to this file, one per distinct content
                                          (for rsync --files-from; duplicates at source go to --plan-out instead; remote-exec with remote source only)
      --digest-cache                      reuse digests from earlier runs; a file is hashed again only when its size, mtime,
                                          ctime or inode changed (one cache per host, default ~/.cache/rsync-sidekick/digests.tsv;
                                          a read-only cache file is used without saving new digests)
      --digest-cache-path string          digest cache file on this host (implies --digest-cache)
  -n, --dry-run                           show what would be done, but don't actually perform any actions
  -x, --exclusions string                 path to file containing newline separated list of file/directory names to be excluded
                                          (names are matched anywhere in the tree; always ignored, even without this flag: $RECYCLE.BIN, desktop.ini, Thumbs.db etc.)
      --hash-min-size string              never hash files smaller than this size, e.g. '512k' - unlike --min-size they stay in the scans:
                                          rsync transfers them, and with --copy-list they go straight into the list (no second rsync pass)
  -h, --help                              display help
      --ignore-extension                  match files by size and content only, not by file extension
                                          (finds copies whose names differ, e.g. an archive that names files by their hash)
      --include-dir stringArray           only scan this directory below source and destination root (relative path; can be specified multiple
                                          times; shell wildcards like 'Backup*' or 'Backup/*/data' are allowed; archive paths are not limited)
      --include-from string               read --include-dir entries from this file, one per line (empty lines and lines starting with # are ignored)
      --list                              list files along their metadata for given directory
      --min-size string                   ignore files smaller than this size, e.g. '1M', '512k', '2g'
                                          (they are left out of every directory scan, so they are never hashed;
                                          rsync transfers them normally - useful when small files dominate the count)
      --one-file-system                   don't cross filesystem boundaries when scanning source and destination (like rsync -x)
                                          (works locally and with remote-exec, not with SFTP)
      --plan-out string                   write the duplicate groups of the --copy-list files to this file (JSON lines),
                                          to be reflinked with --apply-plan once rsync transferred the originals
  -f, --progress-frequency duration       frequency of progress reporting e.g. '5s', '1m' (default 5s)
      --reflink                           use cp --reflink=auto for copy actions (instant on CoW filesystems like btrfs/XFS)
                                          (only effective when copies are performed via --copy-duplicates or --archive-path)
      --remote-digest-cache-path string   digest cache file on the remote host (implies --digest-cache, remote-exec only)
      --sftp                              force SFTP mode (don't try remote-execution)
  -s, --shellscript                       instead of applying changes directly, generate a shell script
                                          (this flag is useful if you want to run the shell script as a different user)
  -p, --shellscript-at-path string        similar to --shellscript option but you can specify output script path
                                          (this flag cannot be specified if --shellscript option is specified)
      --sidekick-path string              remote rsync-sidekick command (e.g. "sudo rsync-sidekick") (default "rsync-sidekick")
  -i, --ssh-key string                    path to SSH private key for remote connections
  -d, --sync-dir-timestamps               also propagate directory timestamps from source to destination
  -v, --verbose                           logs every single action performed, plus extra information (caution: makes it slow!)
      --version                           show application version (v2.12.0) and exit

More details here: https://github.com/m-manu/rsync-sidekick
```

## Advanced options

### Syncing remote paths (via SSH)

`rsync-sidekick` supports syncing to/from remote hosts via SSH. Either the source or the destination (not both!) can be
a remote path in the form `user@host:/path`.

**Remote-execution mode** (default): If `rsync-sidekick` is installed on the remote host, it will be used as an agent
for fast remote scanning and action execution. This is the recommended setup.

**SFTP fallback**: If `rsync-sidekick` is not available on the remote host, it falls back to SFTP mode automatically.
You can also force SFTP mode with `--sftp`.

```bash
# Use a specific SSH key:
rsync-sidekick -i ~/.ssh/my_key /Users/manu/Photos/ user@server:/backup/Photos/

# Specify the remote rsync-sidekick path:
rsync-sidekick --sidekick-path /usr/local/bin/rsync-sidekick /local/path/ user@server:/remote/path/

# Run the remote agent with sudo (useful when syncing to root-owned directories):
rsync-sidekick --sidekick-path "sudo rsync-sidekick" /local/path/ user@server:/remote/path/
```

### Copying duplicate-content files (`--copy-duplicates`)

By default, `rsync-sidekick` only **moves** files at the destination. If the same content exists at multiple paths at
source but only one of those paths exists at the destination, the extra copies are left for `rsync` to transfer.

With `--copy-duplicates` (or `-c`), `rsync-sidekick` will **copy** the file locally at the destination instead, saving
network transfer time:

```bash
# Source has photo.jpg at paths A, B, C (same content). Destination only has it at A.
# Without -c: B and C must be transferred by rsync.
# With -c: rsync-sidekick copies A→B and A→C locally at the destination.
rsync-sidekick -c /Users/manu/Photos/ /Volumes/Portable/Photos/
```

### Using archive directories (`--archive-path`)

The `--archive-path` (or `-a`) flag lets you specify additional directories **on the destination side** that are scanned
for content matches. Files are only **copied** from archives, never moved. This is useful when you have an old
backup or archive that might contain files matching orphans at the source.

Archive paths imply `--copy-duplicates` behavior automatically.

```bash
# Scan an old backup directory for matching content:
rsync-sidekick -a /Volumes/OldBackup/Photos/ /Users/manu/Photos/ /Volumes/Portable/Photos/

# Multiple archive paths:
rsync-sidekick -a /archive1/ -a /archive2/ /source/ /destination/

# Works with remote destinations too (archives must be on the remote host):
rsync-sidekick -a /remote/archive/ /local/source/ user@server:/remote/dest/

# Wildcards: every snapshot below .snapshots, oldest first (quoted, so the local shell leaves them alone):
rsync-sidekick -a '/mnt/raid/.snapshots/*/*' /local/source/ /mnt/raid/data/
```

Wildcards (`*`, `?`, `[...]`) are resolved on the destination host — through the remote agent when the destination is
remote, which needs an agent of v2.9.0 or later (an older one makes the run stop with an error). Matches keep lexical
order, only directories count, and a pattern matching nothing stops the run like an unreadable archive path does.

### Reflink copies (`--reflink`)

On CoW (copy-on-write) filesystems like **btrfs** or **XFS**, the `--reflink` flag makes copy actions use
`cp --reflink=auto`, which is instant and uses no additional disk space. On filesystems that don't support reflinks,
it falls back to a regular copy automatically. This flag only has an effect when copies are being performed
(via `--copy-duplicates` or `--archive-path`).

```bash
# Instant zero-cost copies on btrfs:
rsync-sidekick -c --reflink /Users/manu/Photos/ /mnt/btrfs-backup/Photos/
```

### Transferring each content only once (`--copy-list`, `--plan-out`, `--apply-plan`)

When the source holds the same content at several paths that are all missing at the destination, plain
`rsync` transfers every copy. These three flags split the job so each content crosses the network once.

Example: a backup lost three folders. At source, `Movies/a.mkv`, `Archive/2024/a.mkv` and `Old/a-copy.mkv` are the
same 8 GiB file, and none of them is at the destination any more. `rsync` alone sends 24 GiB; with the steps below,
8 GiB go over the network and the other two paths become reflinks of the first one.

```text
copy.txt    Archive/2024/a.mkv                      ← rsync transfers this one
plan.jsonl  {"dg":"s…","sz":8589934592,"o":{"p":"Archive/2024/a.mkv","mt":…},
             "t":[{"p":"Movies/a.mkv","mt":…},{"p":"Old/a-copy.mkv","mt":…}]}
```

```bash
# 1. as usual, plus: write what rsync still has to transfer, and the duplicate groups
rsync-sidekick -c --reflink --copy-list=copy.txt --plan-out=plan.jsonl user@server:/data/ /backup/data/
# 2. transfer one file per distinct content
rsync -aHAX --files-from=copy.txt user@server:/data/ /backup/data/
# 3. reflink the duplicates from the transferred originals — no scan, no hashing
rsync-sidekick --apply-plan=plan.jsonl /backup/data/
```

- `copy.txt` lists paths relative to the source root: every file at source that nothing at the destination (or in an
  `--archive-path`) can serve, one per distinct content.
- `plan.jsonl` holds one duplicate group per line, with short keys to keep it small:
  `{"dg":"<digest>","sz":<size>,"o":{"p":"<original>","mt":<mtime>},"t":[{"p":"<target>","mt":<mtime>}]}`.
- `--apply-plan` skips a whole group when its original is missing or its size or mtime differs from the plan (the
  source changed after step 1). Existing targets are never overwritten, so the step can be repeated. Targets get their
  own mtime from the plan; mode, owner and group come from the original. `-n` shows what would happen.
- Only orphans sharing their size (and extension, unless `--ignore-extension`) with another orphan are hashed.
- Small files: hashing one costs a disk seek or two, transferring it costs less below a few hundred KiB. Use
  `--hash-min-size 512k`: smaller files are never hashed but still go into `copy.txt`, so one rsync pass covers
  everything. (Files below `--min-size` are not scanned at all and would need a second rsync pass.)
- Needs remote-execution mode with the source on the remote side.

### Scanning only some folders (`--include-dir`, `--include-from`)

To work on a few folders of a large tree while keeping paths relative to its root (and finding duplicates across those
folders), name them with `--include-dir`:

```bash
rsync-sidekick --include-dir FastDrive --include-dir 'Backup*' --include-dir 'Media/*/2024' \
    -c --reflink user@server:/data/ /backup/data/
```

- Paths are relative to the source and destination root; source and destination are both limited to them.
- Shell wildcards (`*`, `?`, `[...]`) work per path component. The walk starts at the last component without a
  wildcard, so `Backup*` walks the whole root and filters, while `Media/*/2024` only walks `Media`.
- An include directory missing at a local destination simply contributes nothing; at the source it is an error.
- `--archive-path` is not limited: `-a /backup/data` keeps the whole destination available as a copy source.
- `--include-from <file>` reads one entry per line; empty lines and lines starting with `#` are ignored.
- Unlike `--exclusions`, which matches names anywhere in the tree, include directories match paths from the root.

### Skipping small files (`--min-size`)

Every file `rsync-sidekick` considers costs a few disk seeks to hash, whether it is 2 KiB or 2 GiB —
so on trees where small files dominate the file count, most of the runtime buys almost no transfer
savings. `--min-size` leaves those files out of **every** directory scan (source, destination and
archive paths), so they never become orphans, never get hashed and never appear in an action.
`rsync` transfers them normally afterwards.

Sizes are binary multiples, and `k`, `m`, `g` and `t` are all understood — as is the format
`rsync-sidekick` prints itself, so a size from the output can be pasted straight back in:

```bash
# Only bother with files of 1 MiB and up:
rsync-sidekick --min-size 1M /mnt/media/ /mnt/backup/media/
```

Directories are never filtered, so `--sync-dir-timestamps` keeps working. In remote-exec mode the
threshold is sent to the agent, and the client applies it to the answer as well — so an older agent
that ignores the field still yields the same result, just with more data on the wire.

### Staying on one filesystem (`--one-file-system`)

On btrfs or other setups with nested mount points / subvolumes, `--one-file-system` prevents
`rsync-sidekick` from crossing filesystem boundaries when scanning source and destination directories
(similar to `rsync -x`). Each subvolume on btrfs has a different device ID, so this effectively
keeps the scan within a single subvolume.

A separate `--archive-one-file-system` flag controls the same behavior for archive paths. By default,
archives **do** cross filesystem boundaries, since you typically point `-a` at a parent directory
containing multiple btrfs snapshot subvolumes.

Both flags work locally and with remote-exec mode. They have **no effect in SFTP mode**, since SFTP
does not expose filesystem/device boundaries.

```bash
# Scan only within the @ subvolume, don't descend into nested subvols:
rsync-sidekick --one-file-system -c --reflink /mnt/data/@ /mnt/backup/@

# Archives cross into snapshots by default (no extra flag needed):
rsync-sidekick --one-file-system -c --reflink -a /mnt/backup/.snapshots/@/ /mnt/data/@ /mnt/backup/@
```

### Ignoring file extensions (`--ignore-extension`)

By default a file only matches another one with the same extension, size and digest. `--ignore-extension`
drops the extension from that comparison, so a copy is found even when its name is entirely different —
for example in an archive that stores every file under its content hash, without an extension:

```bash
rsync-sidekick --ignore-extension --reflink -a /mnt/backup/by-hash/ user@server:/photos/ /mnt/backup/photos/
```

### Reusing digests across runs (`--digest-cache`)

Hashing is what makes a large run slow, and repeated runs over the same trees — or the same archive paths
in several runs — hash the same files again. `--digest-cache` keeps every digest in a cache file and reuses
it as long as the file is unchanged.

A cached digest is used only if device, inode, size, mtime **and ctime** still match. ctime matters because
tools like `rsync -a` rewrite a file and restore its old mtime; the content changed, ctime did too. Files
changed in the last two seconds are not cached, since a write in the same timestamp tick would otherwise go
unnoticed.

* One cache per host: in remote-exec mode the agent keeps its own cache on the remote host
  (in SFTP mode only the local side is cached).
* Default location: `~/.cache/rsync-sidekick/digests.tsv` (of the user the process runs as — with
  `--sidekick-path "sudo rsync-sidekick"` that is root's).
* `--digest-cache-path` / `--remote-digest-cache-path` choose another file; either implies `--digest-cache`.
* A dry run (`-n`) fills the cache too — digests are facts, not actions.
* A cache file that is not writable, or held by another running `rsync-sidekick`, is used read-only:
  cached digests are reused, new ones are not saved, and a warning says so.
* Only entries below the paths of the run (source, destination, archive paths) are loaded into memory;
  the rest of the file is read past and kept, so one cache can serve many different runs.
* Format: one tab-separated line per file, appended as digests are computed. A changed file gets a new line;
  the newest line of a path wins. When more than half of the lines are outdated (and at least 10,000), the
  file is rewritten without them on open. A cache from an incompatible version is discarded.
* Without `--digest-cache`, digests are still remembered for the duration of the run, so no file is hashed
  twice — for example when the destination lies inside an archive path.

### Overlapping paths

An archive path may contain the destination (`-a /mnt/backup/ … /mnt/backup/photos/`), lie inside it, or
contain another archive path. Each directory is still read only once: a destination inside an archive path is
skipped by the archive walk and taken from the destination's file list, and a path inside one already walked
is taken from that walk. Files of the destination found this way only serve as copy sources; moves are decided
by comparing source and destination alone, before any archive is looked at. (Applies to local archive paths
when source/destination and archives follow the same `--one-file-system` setting.)

```bash
# The second run reuses every digest of unchanged files:
rsync-sidekick --digest-cache -c --reflink -a /mnt/backup/archive/ user@server:/data/ /mnt/backup/data/
```

### Running this from a Docker container

Not everyone needs this. But if you do, below is a simple example:

```shell
# Run rsync-sidekick:
docker run --rm -v /Users/manu:/mnt/homedir manumk/rsync-sidekick rsync-sidekick /mnt/homedir/Photos/ /mnt/homedir/Photos_backup/

# Then run rsync: (note the trailing slashes -- without them, rsync's behavior is different)
docker run --rm -v /Users/manu:/mnt/homedir manumk/rsync-sidekick rsync /mnt/homedir/Photos/ /mnt/homedir/Photos_backup/
```

## FAQs

#### Why was this tool created? Doesn't `rsync` provide flags for doing what this tool does? 🤔

`rsync` provides some flags - But it's complicated!

`--fuzzy` requires match of file size (same as rsync-sidekick). However, it also requires:

1. modification time match (How do you speed up rsync when timestamps of pics and videos are updated by photo organizing
   tools, exiftool etc.?)
2. files to be "similarly named" (This doesn't handle a ton of use-cases like reorganizing files across folders, change
   of extensions etc.)

As for flags `--detect-renamed`, `--detect-moved` and `—detect-renamed-lax`, they're patches on standard `rsync`. We
don't know which regression cases these flags break. In fact, the patch author mentions _"Use this option only if you
accept the risk and disk I/O is a bottleneck."_ in their code comment.

#### How about I use `rsync` with hard links instead? I read a blog somewhere.

Approaches using hard links require you to maintain a 'source shadow'. They're also quite 'stateful', with hidden
directories that we aren't supposed to touch!

Anyway, choose what works best for you. 👍

#### How will I benefit from using this tool?

Using `rsync-sidekick` before `rsrync` makes your backup process significantly faster than using only `rsync`. Sometimes
this performance benefit can even be 100x😲, if the only changes at your _source directory_ are the types mentioned
earlier in this article.

#### How do I build this?

See [CONTRIBUTING.md](./CONTRIBUTING.md) for instructions.

#### I want to contribute to this tool. How do I?

Great! See [CONTRIBUTING.md](./CONTRIBUTING.md).
