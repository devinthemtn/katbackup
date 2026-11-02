package archive_test

import (
	"testing"
	"time"

	"github.com/devinthemtn/katbackup/internal/archive"
)

func file(path, sum string) archive.Entry {
	return archive.Entry{Path: path, Type: archive.TypeFile, Mode: 0o644, SHA256: sum}
}

func TestDiff(t *testing.T) {
	touched := file("same", "s")
	touched.ModTime = time.Unix(1000, 0)

	chmodded := file("chmod", "c")
	chmodded.Mode = 0o600

	relinked := archive.Entry{Path: "link", Type: archive.TypeSymlink, Mode: 0o777, LinkTarget: "b"}
	retyped := archive.Entry{Path: "retyped", Type: archive.TypeDir, Mode: 0o644}

	before := []archive.Entry{
		file("same", "s"),
		file("edit", "1"),
		file("gone", "g"),
		file("chmod", "c"),
		file("retyped", ""),
		{Path: "link", Type: archive.TypeSymlink, Mode: 0o777, LinkTarget: "a"},
	}
	after := []archive.Entry{
		touched,
		file("edit", "2"),
		file("new", "n"),
		chmodded,
		retyped,
		relinked,
	}

	want := []struct {
		path string
		kind archive.ChangeKind
	}{
		{"chmod", archive.ModeChanged},
		{"edit", archive.Modified},
		{"gone", archive.Removed},
		{"link", archive.Modified},
		{"new", archive.Added},
		{"retyped", archive.Modified},
	}
	got := archive.Diff(before, after)
	if len(got) != len(want) {
		t.Fatalf("got %d changes, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Path != w.path || got[i].Kind != w.kind {
			t.Errorf("change %d: got %s %s, want %s %s", i, got[i].Kind, got[i].Path, w.kind, w.path)
		}
	}
}

func TestDiffEntriesPointAtTheirOwnEntry(t *testing.T) {
	got := archive.Diff(nil, []archive.Entry{file("a", "1"), file("b", "2")})
	if len(got) != 2 || got[0].New.Path != "a" || got[1].New.Path != "b" {
		t.Fatalf("changes share an entry: %+v", got)
	}
	if got[0].Old != nil {
		t.Errorf("added entry has an old side: %+v", got[0].Old)
	}
}

func TestDiffIdentical(t *testing.T) {
	e := []archive.Entry{file("a", "1")}
	if got := archive.Diff(e, e); len(got) != 0 {
		t.Errorf("identical entries differ: %+v", got)
	}
}
