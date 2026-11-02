package archive

import "sort"

// ChangeKind says how a path differs between two sets of entries.
type ChangeKind string

const (
	Added    ChangeKind = "added"
	Removed  ChangeKind = "removed"
	Modified ChangeKind = "modified"
	// ModeChanged means only the permission bits differ.
	ModeChanged ChangeKind = "mode"
)

// Change is one difference. Old is nil for Added and New is nil for Removed.
type Change struct {
	Path string     `json:"path"`
	Kind ChangeKind `json:"kind"`
	Old  *Entry     `json:"old,omitempty"`
	New  *Entry     `json:"new,omitempty"`
}

// Diff compares two sets of entries and returns the differences sorted by path.
// Modification times are ignored: a touched file, or a folder whose contents
// came and went, holds the same thing it did before.
func Diff(oldEntries, newEntries []Entry) []Change {
	before := make(map[string]Entry, len(oldEntries))
	for _, e := range oldEntries {
		before[e.Path] = e
	}
	var out []Change
	seen := make(map[string]bool, len(newEntries))
	for _, e := range newEntries {
		seen[e.Path] = true
		prev, ok := before[e.Path]
		if !ok {
			out = append(out, Change{Path: e.Path, Kind: Added, New: &e})
			continue
		}
		if kind, changed := compareEntries(prev, e); changed {
			out = append(out, Change{Path: e.Path, Kind: kind, Old: &prev, New: &e})
		}
	}
	for _, e := range oldEntries {
		if !seen[e.Path] {
			out = append(out, Change{Path: e.Path, Kind: Removed, Old: &e})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func compareEntries(a, b Entry) (ChangeKind, bool) {
	if a.Type != b.Type || a.SHA256 != b.SHA256 || a.LinkTarget != b.LinkTarget {
		return Modified, true
	}
	if a.Mode.Perm() != b.Mode.Perm() {
		return ModeChanged, true
	}
	return "", false
}
