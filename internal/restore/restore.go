// Package restore plans and applies restoring snapshot entries onto disk.
package restore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/paths"
)

type Kind string

const (
	Create    Kind = "create"
	Overwrite Kind = "overwrite"
	Unchanged Kind = "unchanged"
)

type Action struct {
	Entry  archive.Entry
	Target string // absolute destination path
	Kind   Kind
	// TypeChanged means the existing path is a different kind of thing
	// (e.g. a directory where the snapshot has a file). Those are always
	// moved aside, even with Force.
	TypeChanged bool
}

type Options struct {
	// Force overwrites changed files instead of moving them to <file>.katbak-<ts>.
	Force bool
	// Progress, if set, is called after each written entry.
	Progress func(done, total int)
}

// Filter builds an entry predicate from user patterns. No patterns selects everything.
func Filter(patterns []string, home string) func(archive.Entry) bool {
	if len(patterns) == 0 {
		return func(archive.Entry) bool { return true }
	}
	norm := make([]string, len(patterns))
	for i, p := range patterns {
		norm[i] = paths.Normalize(p, home)
	}
	return func(e archive.Entry) bool { return paths.MatchAny(norm, e.Path) }
}

// Plan compares the selected entries with what is currently under targetRoot.
func Plan(idx archive.Index, keep func(archive.Entry) bool, targetRoot string) []Action {
	var out []Action
	for _, e := range idx.Entries {
		if keep != nil && !keep(e) {
			continue
		}
		target := filepath.Join(targetRoot, filepath.FromSlash(e.Path))
		kind, typeChanged := compare(e, target)
		out = append(out, Action{Entry: e, Target: target, Kind: kind, TypeChanged: typeChanged})
	}
	return out
}

func compare(e archive.Entry, target string) (Kind, bool) {
	info, err := os.Lstat(target)
	if err != nil {
		return Create, false
	}
	mode := info.Mode()
	switch e.Type {
	case archive.TypeDir:
		if mode.IsDir() {
			return Unchanged, false
		}
	case archive.TypeSymlink:
		if mode&fs.ModeSymlink != 0 {
			if link, _ := os.Readlink(target); link == e.LinkTarget {
				return Unchanged, false
			}
			return Overwrite, false
		}
	case archive.TypeFile:
		if mode.IsRegular() {
			if info.Size() == e.Size && hashFile(target) == e.SHA256 {
				return Unchanged, false
			}
			return Overwrite, false
		}
	}
	return Overwrite, true
}

func hashFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Pending returns the actions that will actually write something.
func Pending(actions []Action) []Action {
	var out []Action
	for _, a := range actions {
		if a.Kind != Unchanged {
			out = append(out, a)
		}
	}
	return out
}

// Apply streams the archive and writes every Create/Overwrite action.
// It returns the paths existing files were moved to.
func Apply(archivePath, passphrase string, actions []Action, opts Options) ([]string, error) {
	todo := map[string]Action{}
	for _, a := range Pending(actions) {
		todo[a.Entry.Path] = a
	}
	if len(todo) == 0 {
		return nil, nil
	}
	r, err := archive.Open(archivePath, passphrase)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	suffix := ".katbak-" + time.Now().Format("20060102-150405")
	var moved []string
	var dirs []Action
	total, done := len(todo), 0
	for len(todo) > 0 {
		e, content, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return moved, err
		}
		a, ok := todo[e.Path]
		if !ok {
			continue
		}
		delete(todo, e.Path)
		if a.Kind == Overwrite && (a.TypeChanged || !opts.Force) {
			bak := a.Target + suffix
			if err := os.Rename(a.Target, bak); err != nil {
				return moved, fmt.Errorf("move aside %s: %w", a.Target, err)
			}
			moved = append(moved, bak)
		}
		if err := write(a, content); err != nil {
			return moved, fmt.Errorf("%s: %w", e.Path, err)
		}
		if e.Type == archive.TypeDir {
			dirs = append(dirs, a)
		}
		done++
		if opts.Progress != nil {
			opts.Progress(done, total)
		}
	}
	// Directory mtimes change as children are written, so set them last.
	for _, d := range dirs {
		_ = os.Chtimes(d.Target, d.Entry.ModTime, d.Entry.ModTime)
	}
	if len(todo) > 0 {
		return moved, fmt.Errorf("%d entries missing from archive", len(todo))
	}
	return moved, nil
}

func write(a Action, content io.Reader) error {
	e := a.Entry
	if err := os.MkdirAll(filepath.Dir(a.Target), 0o755); err != nil {
		return err
	}
	switch e.Type {
	case archive.TypeDir:
		if err := os.MkdirAll(a.Target, e.Mode.Perm()); err != nil {
			return err
		}
		return os.Chmod(a.Target, e.Mode.Perm())
	case archive.TypeSymlink:
		if err := os.Remove(a.Target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Symlink(e.LinkTarget, a.Target)
	case archive.TypeFile:
		tmp, err := os.CreateTemp(filepath.Dir(a.Target), ".katbackup-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err := io.Copy(tmp, content); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Chmod(e.Mode.Perm()); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Chtimes(tmp.Name(), e.ModTime, e.ModTime); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), a.Target)
	}
	return fmt.Errorf("unknown entry type %q", e.Type)
}
