# katbackup

Back up your Linux config files into `~/.katConfigBackup` as compressed, timestamped snapshots, and restore them all or pick individual files in a TUI.

```sh
go build -o katbackup ./cmd/katbackup   # or: go install ./cmd/katbackup

katbackup init            # scan ~ and write ~/.config/katbackup/config.toml (add --tui to pick)
katbackup backup          # → ~/.katConfigBackup/2026-10-26_175200.tar.zst
katbackup backup -e       # encrypted (age passphrase) and includes `secrets`
katbackup list
katbackup show [snap] -t
katbackup restore                          # everything from latest
katbackup restore ~/.zshrc .config/nvim -n # dry-run selected paths
katbackup restore -s 2026-10-26 --to /tmp/out
katbackup tui             # browse snapshots, tick files, restore
katbackup prune -k 5
```

## How it works
- `backup` copies the files into `~/.katConfigBackup/staging`, writes them to a `tar` compressed with zstd (and encrypted with age if requested), then deletes the staging copy (`--keep-dir` keeps it) and prunes down to `keep` snapshots.
- Each archive starts with `katbackup-index.json`, which lists every path relative to `$HOME` with its mode, mtime, sha256 and symlink target. That lets `show`, `tui` and `restore` see changes without unpacking the archive.
- On restore, a changed file is moved to `<file>.katbak-<timestamp>` before it's overwritten. `--force` skips that, and in the TUI `b` toggles it.
- Anything listed under `secrets`, or matching the built-in credential patterns (`.ssh`, `.gnupg`, `gh/hosts.yml`, …), is only backed up into encrypted snapshots.
- Set `KATBACKUP_PASSPHRASE` to skip the passphrase prompt in scripts.

## TUI keys
`space` select · `a` all · `c` only files that differ from disk · `←/→` fold · `/` filter · `enter` review & restore · `esc` back · `q` quit
