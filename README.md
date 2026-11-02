# katbackup

Back up your Linux config files into `~/.katConfigBackup` as compressed, timestamped snapshots, and restore them all or pick individual files in a TUI.

## Install

katbackup runs on Linux, on amd64 and arm64.

### Download a release

Download the tarball for your machine and `checksums.txt` from the [releases page](https://github.com/devinthemtn/katbackup/releases), then check and install it:

```sh
sha256sum --check --ignore-missing checksums.txt
tar -xzf katbackup_*_linux_amd64.tar.gz katbackup   # or linux_arm64
install -D katbackup ~/.local/bin/katbackup
```

Make sure `~/.local/bin` is on your `PATH`.

### Build with Go

You need Go 1.21 or newer. The project needs a newer Go than that, but Go 1.21 and later download the right version automatically the first time you build. To install or upgrade Go itself, see [go.dev/doc/install](https://go.dev/doc/install).

```sh
go install github.com/devinthemtn/katbackup/cmd/katbackup@latest
```

`go install` puts the program in `~/go/bin`. Make sure that folder is on your `PATH` (for example in `~/.zshrc`):

```sh
export PATH="$HOME/go/bin:$PATH"
```

To build from a clone instead, run `go build -o katbackup ./cmd/katbackup`, then run it as `./katbackup`.

Check it works with `katbackup --version` (or `katbackup --help`, which shows the version at the top). Builds that aren't from a release tag report `dev`.

## First run

```sh
katbackup init --tui    # pick which config files and folders to back up
katbackup backup -n     # dry run: list what would go into the backup
katbackup backup        # create the first snapshot
katbackup list          # see it
katbackup restore -n    # dry run: show what a restore would change (nothing yet)
```

`init` without `--tui` uses the suggested paths directly. Either way, you can edit the config file afterwards (see below).

## Commands

```sh
katbackup init [--tui] [--force]   # scan ~ and write the config file
katbackup scan                     # list candidate paths with size and flags
katbackup backup                   # → ~/.katConfigBackup/2026-10-26_175200.tar.zst
katbackup backup -e                # encrypted with a passphrase; also includes `secrets`
katbackup backup -n                # dry run
katbackup backup --keep-dir        # keep the uncompressed staging copy
katbackup list [--json]
katbackup show [snapshot] [-t] [--json]
katbackup restore                              # everything from the latest snapshot
katbackup restore ~/.zshrc .config/nvim -n     # dry run for selected paths
katbackup restore '.config/**/*.toml' -y       # globs work; -y skips the confirm prompt
katbackup restore -s 2026-10-26 --to /tmp/out  # pick a snapshot, restore somewhere else
katbackup restore --force                      # overwrite without keeping .katbak copies
katbackup tui                                  # browse snapshots, compare and tick files, restore (? for keys)
katbackup prune [-k 5 | -k all]               # delete old snapshots (default: keep from config)
```

### Choosing a snapshot

`show` and `restore -s` accept:
- **A full name,** like `2026-10-26_175200`.
- **The start of a name,** like `2026-10-26`, as long as only one snapshot matches.
- **`latest`,** which is also what you get when you leave it out.

### Paths for restore

Paths are relative to your home folder. `~/.zshrc`, `.zshrc` and `/home/you/.zshrc` all mean the same file. A folder matches everything inside it, and `**` globs work.

## Config file

The config file is `~/.config/katbackup/config.toml`, or `$XDG_CONFIG_HOME/katbackup/config.toml` if that variable is set. `init` writes it for you.

```toml
keep = 10          # snapshots to keep; older ones are deleted after each backup ("all" = never delete)
encrypt = false    # true = always encrypt (prompts for a passphrase on every backup)
include = ["~/.zshrc", "~/.gitconfig", "~/.config/nvim", "~/.config/zellij"]
exclude = ["**/node_modules", "**/.git", "**/*.log", "**/cache"]
secrets = ["~/.ssh", "~/.config/gh/hosts.yml"]
diff_tool = 'nvim -d "$LOCAL" "$REMOTE"'   # optional; the TUI's compare command
```

| Key | Meaning |
|---|---|
| `keep` | How many snapshots to keep: a number, or `"all"` to never delete any. Defaults to `10`. (`0` also means all.) Plain and encrypted snapshots count together, and the oldest are deleted first. |
| `encrypt` | Encrypt every backup, as if you always passed `-e`. |
| `include` | Files and folders to back up. Folders are copied with everything inside them. |
| `exclude` | Globs to skip inside included folders. |
| `secrets` | Paths that are only backed up into encrypted snapshots. |
| `diff_tool` | Shell command the TUI's `d` key compares files with. `$LOCAL` is the file on disk and `$REMOTE` is the snapshot's copy; if the command uses neither, both are added to the end, so `"nvim -d"` works. Defaults to `git difftool` if git has a `diff.tool` or `merge.tool` set, and otherwise `git diff --no-index`. |

The TUI comes back as soon as the command exits, so it has to stay running while you look at the diff. GUI tools may need a flag for that (`code --wait --diff`), and tools that only print need a pager (`diff -u "$LOCAL" "$REMOTE" | less`).

## How it works

- `backup` copies the files into `~/.katConfigBackup/staging`, writes them to a `tar` compressed with zstd (and encrypted with age if requested), then deletes the staging copy (`--keep-dir` keeps it) and prunes down to `keep` snapshots.
- Each archive starts with `katbackup-index.json`, which lists every path relative to `$HOME` with its mode, mtime, sha256 and symlink target. That lets `show`, `tui` and `restore` see changes without unpacking the archive. Because paths are stored relative to `$HOME`, a snapshot restores fine on another machine or under another username.
- On restore, each file's checksum is verified against the index before anything on disk changes. A changed file is then moved to `<file>.katbak-<timestamp>` before it's overwritten. `--force` skips that, and in the TUI `b` toggles it.
- Files that can't be read, such as ones you lack permission for or that vanish during the backup, are skipped with a warning, and the backup continues. Check the warnings if you need every file.
- Symlinks are stored as links, and what they point to is not backed up. katbackup warns if an entry in `include` is a symlink. If your dotfiles are symlinked into place, include the folder they really live in.
- `restore` refuses a snapshot containing paths that would land outside the target folder, so a snapshot from elsewhere can't write to unexpected places.
- `--keep-dir` keeps an unencrypted copy of everything in the staging folder, secrets included, even for an encrypted backup.
- Anything listed under `secrets`, or matching the built-in credential patterns (`.ssh`, `.gnupg`, `gh/hosts.yml`, …), is only backed up into encrypted snapshots.
- Comparing a file in the TUI extracts the snapshot's copy to a private temp folder (under `$XDG_RUNTIME_DIR` if set), deleted when the tool exits. For an encrypted snapshot that copy is unencrypted, and katbackup can't clean up anything the tool saves itself, such as vim swap files or an editor's history.
- Set `KATBACKUP_PASSPHRASE` to skip the passphrase prompt in scripts.

## TUI keys

`space` select · `a` all · `c` only files that differ from disk · `d` compare the file with the snapshot's copy · `←/→` fold · `/` filter · `enter` review & restore · `esc` back · `?` help · `q` quit

## Known limitations

- A file whose only change is its permissions shows as unchanged, so a restore won't put the old permissions back.
- Secret detection is a fixed list of patterns (see [How it works](#how-it-works)). Add any other credential files to `secrets` yourself, or they'll go into plain snapshots.

## License

[MIT](LICENSE)
