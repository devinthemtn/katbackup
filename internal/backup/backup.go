// Package backup runs a full snapshot: stage → compress (→ encrypt) → prune.
package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/collect"
	"github.com/devinthemtn/katbackup/internal/config"
	"github.com/devinthemtn/katbackup/internal/paths"
	"github.com/devinthemtn/katbackup/internal/scan"
	"github.com/devinthemtn/katbackup/internal/snapshot"
)

type Options struct {
	Home       string
	Config     config.Config
	Passphrase string // non-empty encrypts and includes secrets
	DryRun     bool
	KeepDir    bool // keep the uncompressed staging copy
	Now        time.Time
}

type Result struct {
	Archive  string
	Staging  string
	Entries  []archive.Entry
	Bytes    int64
	Warnings []string
	Pruned   []snapshot.Snapshot
}

func Run(opts Options) (Result, error) {
	cfg := opts.Config
	encrypt := opts.Passphrase != ""
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	dir := paths.BackupDir(opts.Home)

	includes := append([]string(nil), cfg.Include...)
	excludes := append([]string(nil), cfg.Exclude...)
	if encrypt {
		includes = append(includes, cfg.Secrets...)
	} else {
		excludes = append(excludes, cfg.Secrets...)
		excludes = append(excludes, scan.SecretPatterns...)
	}

	var res Result
	if !opts.DryRun {
		res.Staging = filepath.Join(dir, paths.StagingName)
		if err := os.RemoveAll(res.Staging); err != nil {
			return res, err
		}
		if err := os.MkdirAll(res.Staging, 0o700); err != nil {
			return res, err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return res, err
		}
	}

	col, err := collect.Collect(collect.Options{
		Home: opts.Home, Includes: includes, Excludes: excludes, Staging: res.Staging,
	})
	res.Entries, res.Bytes, res.Warnings = col.Entries, col.Bytes, col.Warnings
	if err != nil {
		return res, err
	}
	if !encrypt && (len(cfg.Secrets) > 0) {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("%d secret path(s) skipped; use --encrypt to include them", len(cfg.Secrets)))
	}
	if opts.DryRun {
		return res, nil
	}
	if len(res.Entries) == 0 {
		os.RemoveAll(res.Staging)
		return res, fmt.Errorf("nothing to back up; check include paths in the config")
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
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
