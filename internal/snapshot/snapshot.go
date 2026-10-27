// Package snapshot names, lists and prunes backup archives.
package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	TimeFormat = "2006-01-02_150405"
	Ext        = ".tar.zst"
	EncExt     = ".tar.zst.age"
)

type Snapshot struct {
	Name      string // e.g. 2026-10-26_175200
	Path      string
	Time      time.Time
	Size      int64
	Encrypted bool
}

var ErrNone = errors.New("no snapshots found")

// NewPath returns an unused archive path in dir for a snapshot taken at t.
func NewPath(dir string, t time.Time, encrypted bool) string {
	ext := Ext
	if encrypted {
		ext = EncExt
	}
	base := t.Format(TimeFormat)
	name := base
	for i := 1; ; i++ {
		if !exists(filepath.Join(dir, name+Ext)) && !exists(filepath.Join(dir, name+EncExt)) {
			return filepath.Join(dir, name+ext)
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// Parse recognizes a snapshot archive by filename.
func Parse(path string) (Snapshot, bool) {
	base := filepath.Base(path)
	var name string
	var enc bool
	switch {
	case strings.HasSuffix(base, EncExt):
		name, enc = strings.TrimSuffix(base, EncExt), true
	case strings.HasSuffix(base, Ext):
		name = strings.TrimSuffix(base, Ext)
	default:
		return Snapshot{}, false
	}
	if len(name) < len(TimeFormat) {
		return Snapshot{}, false
	}
	t, err := time.ParseInLocation(TimeFormat, name[:len(TimeFormat)], time.Local)
	if err != nil {
		return Snapshot{}, false
	}
	return Snapshot{Name: name, Path: path, Time: t, Encrypted: enc}, true
}

// List returns snapshots in dir, newest first.
func List(dir string) ([]Snapshot, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		s, ok := Parse(filepath.Join(dir, e.Name()))
		if !ok {
			continue
		}
		if info, err := e.Info(); err == nil {
			s.Size = info.Size()
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Time.Equal(out[j].Time) {
			return out[i].Name > out[j].Name
		}
		return out[i].Time.After(out[j].Time)
	})
	return out, nil
}

// Find resolves a snapshot by name, unique prefix, file path, or "" / "latest".
func Find(dir, query string) (Snapshot, error) {
	if query != "" && query != "latest" {
		if s, ok := Parse(query); ok && exists(query) {
			return s, nil
		}
	}
	snaps, err := List(dir)
	if err != nil {
		return Snapshot{}, err
	}
	if len(snaps) == 0 {
		return Snapshot{}, fmt.Errorf("%w in %s", ErrNone, dir)
	}
	if query == "" || query == "latest" {
		return snaps[0], nil
	}
	var matches []Snapshot
	for _, s := range snaps {
		if s.Name == query {
			return s, nil
		}
		if strings.HasPrefix(s.Name, query) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return Snapshot{}, fmt.Errorf("no snapshot matches %q", query)
	case 1:
		return matches[0], nil
	default:
		return Snapshot{}, fmt.Errorf("%q matches %d snapshots; be more specific", query, len(matches))
	}
}

// Prune deletes all but the newest keep snapshots. keep <= 0 keeps everything.
func Prune(dir string, keep int) ([]Snapshot, error) {
	if keep <= 0 {
		return nil, nil
	}
	snaps, err := List(dir)
	if err != nil || len(snaps) <= keep {
		return nil, err
	}
	removed := snaps[keep:]
	for _, s := range removed {
		if err := os.Remove(s.Path); err != nil {
			return nil, err
		}
	}
	return removed, nil
}
