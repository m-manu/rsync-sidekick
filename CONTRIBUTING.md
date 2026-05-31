# Contributing to rsync-sidekick project

I'm glad you're considering contributing code to this open source project! 🙂

Contributions to this project are released to the public under the project's [open source license](./LICENSE).

Just follow these principles and you're good.

## Principles of rsync-sidekick

1. No destructive operations!
    - No deletes of any kind
   - Just stick to 'move' and 'copy' operations
2. No changes at source
    - Source directory should be treated as read-only
3. Agnostic of operating systems
   - Except for `--shellscript` option
   - Agnostic of shells (`zsh`, `bash` etc.)
   - However, in non Unix-like environments (e.g. Windows), some features might be unavailable.
4. Do not touch `rsync` in any way
5. Do not reimplement anything that `rsync` already does.
    - Definitely don't transfer files!
6. Do not call `rsync` (That's for users to do!)
    - This is a sidekick, not a wrapper.
7. Be "abort safe"
8. Don't maintain any persistent state (hard links, soft links, dot files etc.) in source directory or target directory
   or anywhere else to achieve the core logic.
   - Temporary states and files may be created
   - Debug/verbose options may create files (not core logic)

## Contribution guidelines

1. Keep PRs small
2. Add test cases for all bug fixes and improvements

## How to build?

`git clone` this repo (obviously).

This project uses [`just`](https://github.com/casey/just) as a command runner — Make sure you have it installed (
`brew install just`).

To see all available commands for build, run etc., simply run `just` on the repo root.

If you don't want to install `just`, simply `cat justfile` and run the commands yourself. 






