# Contributing to rsync-sidekick project

I'm glad you're considering contributing code to this open source project! 🙂

Contributions to this project are released to the public under the project's [open source license](./LICENSE).

Just follow these principles and you're good.

## Principles of rsync-sidekick

1. No destructive operations!
    - No deletes of any kind
2. No changes at source
    - Source directory should be treated as read-only
3. Agnostic of operating systems
4. Agnostic of shells (`zsh`, `bash` etc.)
5. Do not touch `rsync` in any way
6. Do not reimplement anything that `rsync` already does.
    - Definitely don't transfer files!
7. Do not call `rsync` (That's for users to do!)
    - This is a sidekick, not a wrapper.
8. Be "abort safe"
9. Don't maintain any state (hard links, soft links, dot files etc.) in source directory or target directory or anywhere
   else

## Contribution guidelines

1. Keep PRs small
2. Add test cases for all bug fixes and improvements

## How to build?

`git clone` this repo (obviously).

This project uses [`just`](https://github.com/casey/just) as a command runner — Make sure you have it installed (
`brew install just`).

To see all available commands for build, run etc., simply run `just` on the repo root.

If you don't want to install `just`, simply `cat justfile` and run the commands yourself. 






