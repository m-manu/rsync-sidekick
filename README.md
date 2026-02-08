# rsync-sidekick

[![build-and-test](https://github.com/m-manu/rsync-sidekick/actions/workflows/build-and-test.yml/badge.svg)](https://github.com/m-manu/rsync-sidekick/actions/workflows/build-and-test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/m-manu/rsync-sidekick)](https://goreportcard.com/report/github.com/m-manu/rsync-sidekick)
[![Go Reference](https://pkg.go.dev/badge/github.com/m-manu/rsync-sidekick.svg)](https://pkg.go.dev/github.com/m-manu/rsync-sidekick)
[![License](https://img.shields.io/badge/License-Apache%202-blue.svg)](./LICENSE)

## Introduction

`rsync` is a fantastic tool. Yet, by itself, it's a pain to use for repeated backing up of media files (videos, music,
photos, etc.) _that are reorganized frequently_.

`rsync-sidekick` is a safe and simple tool that is designed to run **before** `rsync` is run.

## What does this do?

`rsync-sidekick` propagates following changes (or any combination) from _source directory_ to _destination directory_:

1. Change in file modification timestamp
2. Rename of file/directory
3. Moving a file from one directory to another
4. Directory timestamp synchronization (with `-d` flag)

It works with **local directories**, **remote hosts via SSH** (using a remote agent or SFTP fallback), and inside **Docker containers**.

Note:

* This tool **does not delete** any files or folders (under any circumstances) -- that's why it's safe to use 😌
    * Your files are just _moved around_
    * Now, if you're uncomfortable with this tool even moving your files around, consider using the `--dry-run` option
* This tool **does not** actually **transfer** files -- that's for `rsync` to do 🙂
* Since you'd run `rsync` after this tool is run, any changes that this tool couldn't propagate would just be propagated
  by `rsync`
    * So the most that you might lose is some time with `rsync` doing more work than it could have -- Which is likely
      still much less than not using this tool at all 😄

## How to install?

1. Install Go version at least **1.24**
    * On Ubuntu: `snap install go`
    * On Mac: `brew install go`
    * For anything else: [Go downloads page](https://go.dev/dl/)
2. Run command:
   ```bash
   go install github.com/m-manu/rsync-sidekick@latest
   ```
3. Add following line in your `.bashrc`/`.zshrc` file:
   ```bash
   export PATH="$PATH:$HOME/go/bin"
   ```

## How to use?

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
  -n, --dry-run                       show what would be done, but don't actually perform any actions
  -x, --exclusions string             path to file containing newline separated list of file/directory names to be excluded
                                      (even if this is not set, files/directories such these will still be ignored: $RECYCLE.BIN, desktop.ini, Thumbs.db etc.)
  -h, --help                          display help
      --list                          list files along their metadata for given directory
  -f, --progress-frequency duration   frequency of progress reporting e.g. '5s', '1m' (default 2s)
      --sftp                          force SFTP mode (don't try remote-execution)
  -s, --shellscript                   instead of applying changes directly, generate a shell script
                                      (this flag is useful if you want to run the shell script as a different user)
  -p, --shellscript-at-path string    similar to --shellscript option but you can specify output script path
                                      (this flag cannot be specified if --shellscript option is specified)
      --sidekick-path string          remote rsync-sidekick command (e.g. "sudo rsync-sidekick") (default "rsync-sidekick")
  -i, --ssh-key string                path to SSH private key for remote connections
  -d, --sync-dir-timestamps           also propagate directory timestamps from source to destination
  -v, --verbose                       generates extra information, even a file dump (caution: makes it slow!)
      --version                       show application version (v1.10.5) and exit

More details here: https://github.com/m-manu/rsync-sidekick
```

## Remote usage (SSH)

`rsync-sidekick` supports syncing to/from remote hosts via SSH. Either the source or the destination (not both) can be
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

## Running this from a Docker container

Below is a simple example:

```shell
# Run rsync-sidekick:
docker run --rm -v /Users/manu:/mnt/homedir manumk/rsync-sidekick rsync-sidekick /mnt/homedir/Photos/ /mnt/homedir/Photos_backup/

# Then run rsync: (note the trailing slashes -- without them, rsync's behavior is different)
docker run --rm -v /Users/manu:/mnt/homedir manumk/rsync-sidekick rsync /mnt/homedir/Photos/ /mnt/homedir/Photos_backup/
```

## FAQs

### Why was this tool created?

`rsync` options such as `--detect-renamed`, `--detect-renamed-lax`, `--detect-moved` and `--fuzzy` don't work reliably
and sometimes are dangerous! `rsync-sidekick` is reliable alternative to all these options and much more.

### How will I benefit from using this tool?

Using `rsync-sidekick` before `rsrync` makes your backup process significantly faster than using only `rsync`. Sometimes
this performance benefit can even be 100x😲, if the only changes at your _source directory_ are the 3 types mentioned
earlier in this article.
