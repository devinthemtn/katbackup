// Package collect gathers the configured files and copies them into a staging folder.
package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/paths"
)

type Options struct {
	Home     string
	Includes []string // user patterns, e.g. "~/.config/nvim"
	Excludes []string // user patterns, e.g. "**/node_modules"
	// Staging is the directory files are copied into. Empty means only
	// enumerate (dry run); entries are still hashed from their source.
	Staging string
}

type Result struct {
	Entries  []archive.Entry
	Warnings []string
	Bytes    int64
}

// Collect walks every include, skipping excludes, sockets and devices.
// The backup directory itself is always skipped.
func Collect(opts Options) (Result, error) {
	var res Result
	excludes := []string{paths.BackupDirName}
	for _, p := range opts.Excludes {
		excludes = append(excludes, paths.Normalize(p, opts.Home))
	}
	seen := map[string]bool{}

	add := func(abs, rel string, d fs.DirEntry) error {
		if seen[rel] {
			return nil
		}
		seen[rel] = true
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := archive.Entry{Path: rel, Mode: info.Mode().Perm(), ModTime: info.ModTime()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			e.Type = archive.TypeSymlink
			if e.LinkTarget, err = os.Readlink(abs); err != nil {
				return err
			}
		case info.IsDir():
			e.Type = archive.TypeDir
		case info.Mode().IsRegular():
			e.Type = archive.TypeFile
			e.Size = info.Size()
		default:
			res.Warnings = append(res.Warnings, "skipped special file "+rel)
			return nil
		}
		if err := stage(abs, opts.Staging, &e); err != nil {
			return err
		}
		res.Bytes += e.Size
		res.Entries = append(res.Entries, e)
		return nil
	}

	for _, inc := range opts.Includes {
		root := paths.Expand(inc, opts.Home)
		rootRel, err := paths.RelToHome(root, opts.Home)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %s: outside home", inc))
			continue
		}
		if paths.MatchAny(excludes, rootRel) {
			continue
		}
		if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
			res.Warnings = append(res.Warnings, "missing "+inc)
			continue
		}
		err = filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
			if err != nil {
				res.Warnings = append(res.Warnings, err.Error())
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			rel, err := paths.RelToHome(abs, opts.Home)
			if err != nil {
				return err
			}
			if paths.MatchAny(excludes, rel) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			return add(abs, rel, d)
		})
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

// stage hashes a file and, when staging is set, copies it there.
func stage(abs, staging string, e *archive.Entry) error {
	var dst string
	if staging != "" {
		dst = filepath.Join(staging, filepath.FromSlash(e.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
	}
	switch e.Type {
	case archive.TypeDir:
		if dst != "" {
			return os.MkdirAll(dst, 0o700)
		}
	case archive.TypeSymlink:
		if dst != "" {
			return os.Symlink(e.LinkTarget, dst)
		}
	case archive.TypeFile:
		src, err := os.Open(abs)
		if err != nil {
			return err
		}
		defer src.Close()
		h := sha256.New()
		w := io.Writer(h)
		var out *os.File
		if dst != "" {
			if out, err = os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err != nil {
				return err
			}
			defer out.Close()
			w = io.MultiWriter(h, out)
		}
		// Copy exactly the size recorded so the tar header stays truthful
		// even if the file is being written to while we back it up.
		n, err := io.Copy(w, io.LimitReader(src, e.Size))
		if err != nil {
			return err
		}
		e.Size = n
		e.SHA256 = hex.EncodeToString(h.Sum(nil))
		if out != nil {
			return out.Close()
		}
	}
	return nil
}
