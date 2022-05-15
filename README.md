# rsync-sidekick

[![build-and-test](https://github.com/m-manu/rsync-sidekick/actions/workflows/build-and-test.yml/badge.svg)](https://github.com/m-manu/rsync-sidekick/actions/workflows/build-and-test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/m-manu/rsync-sidekick)](https://goreportcard.com/report/github.com/m-manu/rsync-sidekick)
[![Go Reference](https://pkg.go.dev/badge/github.com/m-manu/rsync-sidekick.svg)](https://pkg.go.dev/github.com/m-manu/rsync-sidekick)
[![License](https://img.shields.io/badge/License-Apache%202-blue.svg)](./LICENSE)

## Introduction

`rsync` is a fantastic tool. Yet, by itself, it's a pain to use for repeated backing up of media files (videos, music,
photos, etc.) _that are reorganized frequently_.

`rsync-sidekick` is a safe and simple tool that is designed to run **before** `rsync` is run.

This propagates following changes from _source directory_ to _destination directory_ (or any combination of below):

1. Change in file modification timestamp
2. Rename of file/directory
3. Moving a file from one directory to another

Note:

* This tool **does not delete** any files or folders (under any circumstances) -- that's why safe-to-use 😌
    * Your files are just _moved around_
    * Now, if you're uncomfortable with this tool even moving your files around, there is a `-shellscript` option, that
      just generates a script for you to read and run (think of it like a `--dry-run` option)
* This tool **does not** actually **transfer** files -- that's for `rsync` to do 🙂
* Since you'd run `rsync` after this tool is run, any changes that this tool couldn't propagate would just be propagated
  by `rsync`
    * So the most that you might lose is some time with `rsync` doing more work than it could have -- Which is likely
      still much less than not using this tool at all 😄

## How to install?

1. Install Go version at least **1.17**
    * On Ubuntu: `snap install go`
    * On Mac: `brew install go`
    * For anything else: [Go downloads page](https://golang.org/dl/)
2. Run command:
   ```bash
   go get github.com/m-manu/rsync-sidekick
   ```

## How to use?

### Step 1

Run this tool:

```bash
rsync-sidekick /Users/manu/Photos/ /Volumes/Portable/Photos/
```

### Step 2

Run `rsync` as you would normally do:

```bash
# (note the trailing slashes -- without them, rsync's behavior is different)
rsync -av /Users/manu/Photos/ /Volumes/Portable/Photos/ 
```

## Command line options

Running `rsync-sidekick -help` displays following information:

```
usage:
	rsync-sidekick <flags> [source-dir] [destination-dir]
where:
	source-dir        Source directory
	destination-dir   Destination directory
flags: (all optional)
  -exclusions string
    	path to a text file that contains ignorable file/directory names separated by new lines (even without this flag, this tool ignores commonly ignorable names such as 'System Volume Information', 'Thumbs.db' etc.)
  -extrainfo
    	generate extra information (caution: makes it slow!)
  -shellscript
    	instead of applying changes directly, generate a shell script (this flag is useful if you want run the shell script as a different user)
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
