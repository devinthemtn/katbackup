package collect_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/collect"
)

func put(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func has(entries []archive.Entry, path string) bool {
	for _, e := range entries {
		if e.Path == path {
			return true
		}
	}
	return false
}

func warned(warnings []string, part string) bool {
	for _, w := range warnings {
		if strings.Contains(w, part) {
			return true
		}
	}
	return false
}

// One file the user can't read must not stop everything else being backed up.
func TestUnreadableFileIsSkippedWithWarning(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read everything")
	}
	home := t.TempDir()
	put(t, filepath.Join(home, ".config/app/good"), "ok", 0o644)
	put(t, filepath.Join(home, ".config/app/locked"), "no", 0o000)

	res, err := collect.Collect(collect.Options{
		Home: home, Includes: []string{"~/.config/app"}, Staging: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("backup aborted: %v", err)
	}
	if !has(res.Entries, ".config/app/good") {
		t.Error("readable file was not collected")
	}
	if has(res.Entries, ".config/app/locked") {
		t.Error("unreadable file has an entry in the index")
	}
	if !warned(res.Warnings, ".config/app/locked") {
		t.Errorf("no warning names the skipped file: %v", res.Warnings)
	}
}

// Failing to write the staging copy (a full disk, say) is not the same as a
// bad source file. Skipping would produce a backup missing files.
func TestStagingFailureAborts(t *testing.T) {
	home := t.TempDir()
	put(t, filepath.Join(home, ".zshrc"), "x", 0o644)
	notADir := filepath.Join(t.TempDir(), "file")
	put(t, notADir, "", 0o644)

	_, err := collect.Collect(collect.Options{
		Home: home, Includes: []string{"~/.zshrc"}, Staging: notADir,
	})
	if err == nil {
		t.Fatal("staging failure was ignored")
	}
}

func TestSymlinkedIncludeWarns(t *testing.T) {
	home := t.TempDir()
	put(t, filepath.Join(home, "dotfiles/nvim/init.lua"), "x", 0o644)
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../dotfiles/nvim", filepath.Join(home, ".config/nvim")); err != nil {
		t.Fatal(err)
	}
	res, err := collect.Collect(collect.Options{Home: home, Includes: []string{"~/.config/nvim"}})
	if err != nil {
		t.Fatal(err)
	}
	if !warned(res.Notes, "symlink") {
		t.Errorf("no symlink note: %v", res.Notes)
	}
}

// An include reached through another included symlink would give the index a
// child under a symlink, which restore rejects as unsafe. This really happens:
// `init` adds ~/.config/gh/hosts.yml to secrets, and ~/.config/gh may be a
// stow symlink. Such a child has to be dropped at backup time, with a warning,
// rather than produce a snapshot that can't be restored.
func TestChildOfIncludedSymlinkIsDropped(t *testing.T) {
	home := t.TempDir()
	put(t, filepath.Join(home, "dotfiles/gh/hosts.yml"), "token", 0o600)
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../dotfiles/gh", filepath.Join(home, ".config/gh")); err != nil {
		t.Fatal(err)
	}
	res, err := collect.Collect(collect.Options{
		Home: home, Includes: []string{"~/.config/gh", "~/.config/gh/hosts.yml"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !has(res.Entries, ".config/gh") {
		t.Error("the symlink itself should be kept")
	}
	if has(res.Entries, ".config/gh/hosts.yml") {
		t.Error("child of a symlink entry would make the snapshot unrestorable")
	}
	if !warned(res.Warnings, ".config/gh/hosts.yml") {
		t.Errorf("no warning names the dropped file: %v", res.Warnings)
	}
	if res.Bytes != 0 {
		t.Errorf("dropped file still counted: %d bytes", res.Bytes)
	}
}

// Staging a file through an already-staged absolute symlink resolves to the real
// file, so opening the "copy" for writing would truncate the user's original.
func TestStagingNeverWritesThroughSymlinks(t *testing.T) {
	for name, includes := range map[string][]string{
		"symlink listed first": {"~/.config/gh", "~/.config/gh/hosts.yml"},
		"child listed first":   {"~/.config/gh/hosts.yml", "~/.config/gh"},
	} {
		for _, kind := range []string{"absolute", "relative"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				home := t.TempDir()
				original := filepath.Join(home, "dotfiles/gh/hosts.yml")
				put(t, original, "token", 0o600)
				if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
					t.Fatal(err)
				}
				target := "../dotfiles/gh"
				if kind == "absolute" {
					target = filepath.Join(home, "dotfiles/gh")
				}
				if err := os.Symlink(target, filepath.Join(home, ".config/gh")); err != nil {
					t.Fatal(err)
				}

				res, err := collect.Collect(collect.Options{
					Home: home, Includes: includes, Staging: filepath.Join(home, "staging"),
				})
				if err != nil {
					t.Errorf("backup failed: %v", err)
				}
				got, rerr := os.ReadFile(original)
				if rerr != nil || string(got) != "token" {
					t.Fatalf("ORIGINAL FILE DAMAGED: content %q, err %v", got, rerr)
				}
				if has(res.Entries, ".config/gh/hosts.yml") {
					t.Error("child of a symlink is in the index")
				}
			})
		}
	}
}

// Parents must precede their children however the includes overlap, because
// restoring a parent can replace whatever is already at that path.
func TestParentsComeBeforeChildren(t *testing.T) {
	home := t.TempDir()
	put(t, filepath.Join(home, ".a/b/c"), "x", 0o644)
	res, err := collect.Collect(collect.Options{Home: home, Includes: []string{"~/.a/b", "~/.a"}})
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, e := range res.Entries {
		pos[e.Path] = i
	}
	if !(pos[".a"] < pos[".a/b"] && pos[".a/b"] < pos[".a/b/c"]) {
		t.Errorf("order: %v", pos)
	}
}
