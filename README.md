# rsync-sidekick

[![Build Status](https://api.travis-ci.org/m-manu/rsync-sidekick.svg?branch=main&status=passed)](https://travis-ci.org/github/m-manu/rsync-sidekick)
[![Go Report Card](https://goreportcard.com/badge/github.com/m-manu/rsync-sidekick)](https://goreportcard.com/report/github.com/m-manu/rsync-sidekick)
[![Go Reference](https://pkg.go.dev/badge/github.com/m-manu/rsync-sidekick.svg)](https://pkg.go.dev/github.com/m-manu/rsync-sidekick)
[![License](https://img.shields.io/badge/License-Apache%202-blue.svg)](./LICENSE)

## Introduction

`rsync` is a fantastic tool. Yet, by itself, it's a pain to use for repeated backing up of media files (videos, music,
photos, etc.) _that are reorganized frequently_.

`rsync-sidekick` is a light-weight tool that is designed to run **before** `rsync` is run. This propagates following
changes from _source directory_ to _destination directory_ (or any combination of below):

1. Change in file modification timestamp
2. Rename of file/directory
3. Moving a file from one directory to another

Note that, this tool

* does *not* do any actual file transfer
* does *not* delete anything

## How to install?

1. Install Go **1.16**
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

Use `rsync-sidekick -help` to see additional command line options.

### Step 2

Run `rsync` as you would do normally:

```bash
# (note the trailing slashes -- without them, rsync's behavior is different)
rsync -av /Users/manu/Photos/ /Volumes/Portable/Photos/ 
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

Using `rsync-sidekick` before `rsrync` makes your backup process significantly faster than using only `rsync` (sometimes
even 100x faster if the only changes at _source directory_ are the 3 types mentioned earlier in this article)