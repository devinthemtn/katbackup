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
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/paths"
)

type Options struct {
	Home     string
	Includes []string // user patterns, e.g. "~/.config/nvim"
	Excludes []string // user patterns, e.g. "**/node_modules"
	// SkipDir is a home-relative folder to leave out, matched literally rather
	// than as a glob. It's for a backup location inside the home folder.
	SkipDir string
	// Staging is the directory files are copied into. Empty means only
	// enumerate (dry run); entries are still hashed from their source.
	Staging string
}

type Result struct {
	Entries []archive.Entry
	// Warnings are things the user may have wanted backed up that weren't.
	Warnings []string
	// Notes are expected on every run, such as a socket in an included folder.
	Notes []string
	Bytes int64
}

// Collect walks every include, skipping excludes, sockets and devices.
// The backup directory itself is always skipped.
func Collect(opts Options) (Result, error) {
	var res Result
	// Restore's leftovers are always skipped. They're copies of real files under
	// names the secret patterns don't match, so hosts.yml.katbak-… would otherwise
	// put a token into a plain snapshot. This can't be a default in the config,
	// because existing configs wouldn't get it.
	excludes := []string{
		paths.BackupDirName,
		"**/*" + paths.RestoreBackupMarker + "*",
		"**/" + paths.RestoreTempPrefix + "*",
	}
	for _, p := range opts.Excludes {
		excludes = append(excludes, paths.Normalize(p, opts.Home))
	}
	excluded := func(rel string) bool {
		if opts.SkipDir != "" && (rel == opts.SkipDir || strings.HasPrefix(rel, opts.SkipDir+"/")) {
			return true
		}
		return paths.MatchAny(excludes, rel)
	}
	seen := map[string]bool{}
	symlinks := map[string]bool{}

	add := func(abs, rel string, d fs.DirEntry) error {
		if seen[rel] {
			return nil
		}
		seen[rel] = true
		skip := func(err error) error {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %s: %v", rel, err))
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return skip(err)
		}
		e := archive.Entry{Path: rel, Mode: info.Mode().Perm(), ModTime: info.ModTime()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			e.Type = archive.TypeSymlink
			if e.LinkTarget, err = os.Readlink(abs); err != nil {
				return skip(err)
			}
		case info.IsDir():
			e.Type = archive.TypeDir
		case info.Mode().IsRegular():
			e.Type = archive.TypeFile
			e.Size = info.Size()
		default:
			res.Notes = append(res.Notes, "skipped special file "+rel)
			return nil
		}
		if err := stage(abs, opts.Staging, &e); err != nil {
			var src *sourceError
			if errors.As(err, &src) {
				return skip(src.err)
			}
			return err
		}
		if e.Type == archive.TypeSymlink {
			symlinks[rel] = true
		}
		res.Bytes += e.Size
		res.Entries = append(res.Entries, e)
		return nil
	}

	type include struct{ spec, abs, rel string }
	var includes []include
	for _, inc := range opts.Includes {
		root := paths.Expand(inc, opts.Home)
		rootRel, err := paths.RelToHome(root, opts.Home)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %s: outside home", inc))
			continue
		}
		includes = append(includes, include{inc, root, rootRel})
	}
	// Walk parents before children, whatever order the config lists them in.
	// That way a symlink is always recorded before anything beneath it comes up,
	// and restore can rely on seeing a directory before its contents.
	sort.SliceStable(includes, func(i, j int) bool { return includes[i].rel < includes[j].rel })

	for _, inc := range includes {
		root, rootRel := inc.abs, inc.rel
		if excluded(rootRel) {
			continue
		}
		rootInfo, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			res.Warnings = append(res.Warnings, "missing "+inc.spec)
			continue
		}
		if err == nil && rootInfo.Mode()&fs.ModeSymlink != 0 {
			res.Notes = append(res.Notes,
				fmt.Sprintf("%s is a symlink: only the link is backed up, not what it points to", inc.spec))
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
			if excluded(rel) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			// Never stage through a symlink: the staged link can resolve to the
			// real file, and writing the "copy" would truncate the original.
			// Restore also rejects an index with entries beneath a symlink.
			if link := symlinkParent(symlinks, rel); link != "" {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("skipped %s: %s is a symlink, so include the folder it points to instead", rel, link))
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
	// Overlapping includes can list a child before its parent. Restore relies
	// on parents coming first, and sorting by path guarantees that.
	return res, nil
}

// symlinkParent returns the recorded symlink that rel lies beneath, if any.
func symlinkParent(symlinks map[string]bool, rel string) string {
	for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
		if symlinks[dir] {
			return dir
		}
	}
	return ""
}

// sourceError marks a failure reading the file being backed up, as opposed to
// writing the staging copy. The first skips one file; the second aborts.
type sourceError struct{ err error }

func (e *sourceError) Error() string { return e.err.Error() }
func (e *sourceError) Unwrap() error { return e.err }

type sourceReader struct{ r io.Reader }

func (s sourceReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && err != io.EOF {
		err = &sourceError{err}
	}
	return n, err
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
			return &sourceError{err}
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
		n, err := io.Copy(w, io.LimitReader(sourceReader{src}, e.Size))
		if err != nil {
			if dst != "" {
				os.Remove(dst)
			}
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
