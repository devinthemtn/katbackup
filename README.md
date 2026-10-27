# katbackup

Back up your Linux config files into `~/.katConfigBackup` as compressed, timestamped snapshots, and restore them all or pick individual files in a TUI.

## Install

**You need** Go 1.21 or newer and git. The project needs Go 1.26, but Go 1.21 and later download the right version automatically the first time you build. See [upgrade-go.md](upgrade-go.md) if you want to upgrade your system Go.

```sh
git clone https://github.com/devinthemtn/katbackup.git   # or wherever the repo lives
cd katbackup
go install ./cmd/katbackup
```

`go install` puts the program in `~/go/bin`. Make sure that folder is on your `PATH` (for example in `~/.zshrc`):

```sh
export PATH="$HOME/go/bin:$PATH"
```

Then check it works with `katbackup --help`.

If you'd rather not install it, `go build -o katbackup ./cmd/katbackup` builds it in the project folder, and you run it as `./katbackup`.

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
katbackup tui                                  # browse snapshots, tick files, restore
katbackup prune [-k 5]                         # delete old snapshots (default: keep from config)
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
keep = 10          # snapshots to keep; older ones are deleted after each backup (0 = keep all)
encrypt = false    # true = always encrypt (prompts for a passphrase on every backup)
include = ["~/.zshrc", "~/.gitconfig", "~/.config/nvim", "~/.config/zellij"]
exclude = ["**/node_modules", "**/.git", "**/*.log", "**/cache"]
secrets = ["~/.ssh", "~/.config/gh/hosts.yml"]
```

| Key | Meaning |
|---|---|
| `keep` | How many snapshots to keep. `0` keeps them all. |
| `encrypt` | Encrypt every backup, as if you always passed `-e`. |
| `include` | Files and folders to back up. Folders are copied with everything inside them. |
| `exclude` | Globs to skip inside included folders. |
| `secrets` | Paths that are only backed up into encrypted snapshots. |

## How it works

- `backup` copies the files into `~/.katConfigBackup/staging`, writes them to a `tar` compressed with zstd (and encrypted with age if requested), then deletes the staging copy (`--keep-dir` keeps it) and prunes down to `keep` snapshots.
- Each archive starts with `katbackup-index.json`, which lists every path relative to `$HOME` with its mode, mtime, sha256 and symlink target. That lets `show`, `tui` and `restore` see changes without unpacking the archive. Because paths are stored relative to `$HOME`, a snapshot restores fine on another machine or under another username.
- On restore, a changed file is moved to `<file>.katbak-<timestamp>` before it's overwritten. `--force` skips that, and in the TUI `b` toggles it.
- Anything listed under `secrets`, or matching the built-in credential patterns (`.ssh`, `.gnupg`, `gh/hosts.yml`, …), is only backed up into encrypted snapshots.
- Set `KATBACKUP_PASSPHRASE` to skip the passphrase prompt in scripts.

## TUI keys

`space` select · `a` all · `c` only files that differ from disk · `←/→` fold · `/` filter · `enter` review & restore · `esc` back · `q` quit
