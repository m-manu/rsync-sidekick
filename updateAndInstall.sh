#!/bin/bash
set -e

[ "$EUID" -eq 0 ] && echo "ERROR: Do not run as root (go build would create root-owned files)" >&2 && exit 1

export GOROOT="${GOROOT:-/usr/local/go}"
export PATH="$GOROOT/bin:$PATH"

git pull
go build
sudo cp rsync-sidekick /usr/local/bin/rsync-sidekick
rsync-sidekick --version
