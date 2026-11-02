// Package backup runs a full snapshot: stage → compress (→ encrypt) → prune.
package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/collect"
	"github.com/devinthemtn/katbackup/internal/config"
	"github.com/devinthemtn/katbackup/internal/paths"
	"github.com/devinthemtn/katbackup/internal/scan"
	"github.com/devinthemtn/katbackup/internal/snapshot"
)

type Options struct {
	Home   string
	Config config.Config
	// Dir is where the snapshot goes. Empty means the default location. Any
	// other folder has to exist already: creating it could hide an unmounted
	// drive by quietly writing to the local disk instead.
	Dir        string
	Encrypt    bool   // encrypt and include secrets
	Passphrase string // required when Encrypt is set, unless DryRun
	DryRun     bool
	KeepDir    bool // keep the uncompressed staging copy
	// SkipUnchanged writes nothing if the newest snapshot of the same kind
	// already holds exactly this.
	SkipUnchanged bool
	Now           time.Time
}

type Result struct {
	Archive  string
	Staging  string
	Entries  []archive.Entry
	Bytes    int64
	Warnings []string
	Notes    []string
	Pruned   []snapshot.Snapshot
	// UnchangedSince is the snapshot that made this run unnecessary, if any.
	UnchangedSince *snapshot.Snapshot
}

// ErrBusy means another backup is running.
var ErrBusy = errors.New("another backup is already running")

// lock stops two backups overlapping, such as a scheduled one starting during a
// manual one. They share a staging folder, so the second would delete files the
// first is still reading. The kernel drops the lock when the process ends, even
// if it's killed, so a crash can't leave backups blocked.
func lock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock backups: %w", err)
	}
	return func() { f.Close() }, nil
}

func Run(opts Options) (res Result, err error) {
	cfg := opts.Config
	encrypt := opts.Encrypt || opts.Passphrase != ""
	if encrypt && opts.Passphrase == "" && !opts.DryRun {
		return res, errors.New("encrypted backup needs a passphrase")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	// Staging stays in the default location wherever snapshots go, because it
	// holds plaintext and the snapshot folder may be synced or on a network.
	local := paths.BackupDir(opts.Home)
	dir := local
	var skipDir string
	if opts.Dir != "" && filepath.Clean(opts.Dir) != local {
		dir = filepath.Clean(opts.Dir)
		info, err := os.Stat(dir)
		if err != nil {
			return res, fmt.Errorf("backup location %s isn't available (create it, or mount the drive it's on): %w", dir, err)
		}
		if !info.IsDir() {
			return res, fmt.Errorf("backup location %s is not a folder", dir)
		}
		if rel, err := paths.RelToHome(dir, opts.Home); err == nil {
			skipDir = rel
		}
	}

	includes := append([]string(nil), cfg.Include...)
	excludes := append([]string(nil), cfg.Exclude...)
	if encrypt {
		includes = append(includes, cfg.Secrets...)
	} else {
		excludes = append(excludes, cfg.Secrets...)
		excludes = append(excludes, scan.SecretPatterns...)
	}

	if !opts.DryRun {
		// Taken before touching staging, which the other run is using. Not
		// `unlock, err :=`: the cleanup below has to see the function's err.
		unlock, lockErr := lock(local)
		if lockErr != nil {
			return res, lockErr
		}
		defer unlock()
		res.Staging = filepath.Join(local, paths.StagingName)
		// Staging holds plaintext copies, secrets included when encrypting.
		defer func() {
			if err != nil && !opts.KeepDir {
				os.RemoveAll(filepath.Join(local, paths.StagingName))
				res.Staging = ""
			}
		}()
		if err := os.RemoveAll(res.Staging); err != nil {
			return res, err
		}
		if err := os.MkdirAll(res.Staging, 0o700); err != nil {
			return res, err
		}
		// A custom location keeps its own permissions: it may be shared on
		// purpose, and chmod can fail on a network mount.
		if err := os.Chmod(local, 0o700); err != nil {
			return res, err
		}
	}

	col, err := collect.Collect(collect.Options{
		Home: opts.Home, Includes: includes, Excludes: excludes, SkipDir: skipDir, Staging: res.Staging,
	})
	res.Entries, res.Bytes, res.Warnings, res.Notes = col.Entries, col.Bytes, col.Warnings, col.Notes
	if err != nil {
		return res, err
	}
	if !encrypt && (len(cfg.Secrets) > 0) {
		res.Notes = append(res.Notes,
			fmt.Sprintf("%d secret path(s) skipped; use --encrypt to include them", len(cfg.Secrets)))
	}
	if encrypt && opts.KeepDir && !opts.DryRun {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("--keep-dir leaves an unencrypted copy of everything, secrets included, in %s", res.Staging))
	}
	if opts.SkipUnchanged && len(res.Entries) > 0 {
		res.UnchangedSince = unchangedSince(dir, res.Entries, encrypt, opts.Passphrase, &res.Warnings)
	}
	if opts.DryRun {
		return res, nil
	}
	if len(res.Entries) == 0 {
		return res, fmt.Errorf("nothing to back up; check include paths in the config")
	}
	if res.UnchangedSince != nil {
		if !opts.KeepDir {
			if err := os.RemoveAll(res.Staging); err != nil {
				return res, err
			}
			res.Staging = ""
		}
		return res, nil
	}

	host, _ := os.Hostname()
	idx := archive.Index{Hostname: host, Created: opts.Now, Entries: res.Entries}
	res.Archive = snapshot.NewPath(dir, opts.Now, encrypt)
	if err := writeArchive(res.Archive, res.Staging, idx, opts.Passphrase); err != nil {
		return res, err
	}
	if !opts.KeepDir {
		if err := os.RemoveAll(res.Staging); err != nil {
			return res, err
		}
		res.Staging = ""
	}
	res.Pruned, err = snapshot.Prune(dir, int(cfg.Keep))
	return res, err
}

// unchangedSince returns the newest snapshot of the same kind if it holds exactly
// these entries. It only compares like with like, because plain snapshots leave
// secrets out and would never match an encrypted one. Not being able to compare
// must never stop a backup, so any failure just means a new snapshot gets written.
func unchangedSince(dir string, entries []archive.Entry, encrypted bool, pass string, warnings *[]string) *snapshot.Snapshot {
	snaps, err := snapshot.List(dir)
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("couldn't check for changes: %v", err))
		return nil
	}
	for _, s := range snaps {
		if s.Encrypted != encrypted {
			continue
		}
		// A dry run doesn't ask for the passphrase, so it can't look inside.
		if encrypted && pass == "" {
			return nil
		}
		idx, err := archive.ReadIndex(s.Path, pass)
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("couldn't compare with %s, so writing a new snapshot: %v", s.Name, err))
			return nil
		}
		if len(archive.Diff(idx.Entries, entries)) > 0 {
			return nil
		}
		return &s
	}
	return nil
}

func writeArchive(path, staging string, idx archive.Index, pass string) error {
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := archive.Write(f, staging, idx, pass); err != nil {
		f.Close()
		return err
	}
	// Older snapshots get pruned right after this, so the new one has to
	// survive a crash before they're gone: data first, then the new name.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
