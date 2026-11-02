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
	"path"
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
	// Directories that will be replaced by a new, empty one. Whatever is
	// beneath them on disk now won't be there after the restore, even if it
	// currently looks unchanged (through a symlink, for example).
	replaced := map[string]bool{}
	for _, e := range idx.Entries {
		if keep != nil && !keep(e) {
			continue
		}
		target := filepath.Join(targetRoot, filepath.FromSlash(e.Path))
		kind, typeChanged := compare(e, target)
		if underAny(replaced, e.Path) {
			kind, typeChanged = Create, false
		} else if typeChanged && e.Type == archive.TypeDir {
			replaced[e.Path] = true
		}
		out = append(out, Action{Entry: e, Target: target, Kind: kind, TypeChanged: typeChanged})
	}
	return out
}

func underAny(dirs map[string]bool, rel string) bool {
	if len(dirs) == 0 {
		return false
	}
	for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
		if dirs[dir] {
			return true
		}
	}
	return false
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

func hashFile(file string) string {
	f, err := os.Open(file)
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
		moveAside := func() error {
			if a.Kind != Overwrite || (opts.Force && !a.TypeChanged) {
				return nil
			}
			bak := a.Target + suffix
			if err := os.Rename(a.Target, bak); err != nil {
				return fmt.Errorf("move aside %s: %w", a.Target, err)
			}
			moved = append(moved, bak)
			return nil
		}
		if err := write(a, content, moveAside); err != nil {
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
	// Writing a child changes its directory's mtime and needs write permission,
	// so modes and mtimes are set last, deepest first.
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		if err := os.Chmod(d.Target, d.Entry.Mode.Perm()); err != nil {
			return moved, fmt.Errorf("%s: %w", d.Entry.Path, err)
		}
		_ = os.Chtimes(d.Target, d.Entry.ModTime, d.Entry.ModTime)
	}
	if len(todo) > 0 {
		return moved, fmt.Errorf("%d entries missing from archive", len(todo))
	}
	return moved, nil
}

// ExtractFile writes one file entry's content to dst as a new read-only file,
// verifying its checksum. dst doesn't exist afterwards if anything went wrong.
func ExtractFile(archivePath, passphrase string, e archive.Entry, dst string) error {
	if e.Type != archive.TypeFile {
		return fmt.Errorf("%s is not a regular file", e.Path)
	}
	r, err := archive.Open(archivePath, passphrase)
	if err != nil {
		return err
	}
	defer r.Close()
	for {
		got, content, err := r.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: missing from archive", e.Path)
		}
		if err != nil {
			return err
		}
		if got.Path == e.Path {
			return extractTo(dst, content, got)
		}
	}
}

func extractTo(dst string, content io.Reader, e archive.Entry) (err error) {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst)
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), content); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != e.SHA256 {
		return fmt.Errorf("checksum mismatch: snapshot is corrupt (want %s, got %s)", e.SHA256, got)
	}
	return nil
}

// write puts one entry on disk. moveAside runs only once the new content is
// known to be good, so a corrupt archive leaves the existing file untouched.
func write(a Action, content io.Reader, moveAside func() error) error {
	e := a.Entry
	// Parents the snapshot has no entry for get no access for other users:
	// this file may be a secret, and nothing says the folder should be open.
	if err := os.MkdirAll(filepath.Dir(a.Target), 0o700); err != nil {
		return err
	}
	switch e.Type {
	case archive.TypeDir:
		if err := moveAside(); err != nil {
			return err
		}
		// Stay writable until Apply sets the recorded mode, after the children.
		if err := os.MkdirAll(a.Target, 0o700); err != nil {
			return err
		}
		return os.Chmod(a.Target, e.Mode.Perm()|0o700)
	case archive.TypeSymlink:
		if err := moveAside(); err != nil {
			return err
		}
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
		h := sha256.New()
		if _, err := io.Copy(io.MultiWriter(tmp, h), content); err != nil {
			tmp.Close()
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != e.SHA256 {
			tmp.Close()
			return fmt.Errorf("checksum mismatch: snapshot is corrupt (want %s, got %s)", e.SHA256, got)
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
		if err := moveAside(); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), a.Target)
	}
	return fmt.Errorf("unknown entry type %q", e.Type)
}
