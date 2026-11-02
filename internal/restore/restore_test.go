package restore_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/restore"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func fileEntry(path, content string) archive.Entry {
	return archive.Entry{
		Path: path, Type: archive.TypeFile, Mode: 0o644, Size: int64(len(content)),
		ModTime: time.Now(), SHA256: sum(content),
	}
}

// build writes an archive from files on disk (path → content) and an index,
// and returns the archive path along with the index as read back from it.
func build(t *testing.T, files map[string]string, entries []archive.Entry) (string, archive.Index) {
	t.Helper()
	src := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(t.TempDir(), "2026-01-01_000000.tar.zst")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Write(f, src, archive.Index{Entries: entries}, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	idx, err := archive.ReadIndex(p, "")
	if err != nil {
		t.Fatal(err)
	}
	return p, idx
}

func put(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Content that doesn't match the recorded hash must be refused, and the file
// already on disk must stay exactly where it is.
func TestCorruptContentLeavesExistingFileInPlace(t *testing.T) {
	e := fileEntry("f", "good")
	e.SHA256 = sum("something else")
	arc, idx := build(t, map[string]string{"f": "good"}, []archive.Entry{e})

	out := t.TempDir()
	target := filepath.Join(out, "f")
	put(t, target, "mine")

	moved, err := restore.Apply(arc, "", restore.Plan(idx, nil, out), restore.Options{})
	if err == nil {
		t.Fatal("content with a wrong hash was restored")
	}
	if len(moved) != 0 {
		t.Errorf("file was moved aside before the new content was checked: %v", moved)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("existing file is gone from its path: %v", err)
	}
	if string(got) != "mine" {
		t.Errorf("existing file changed to %q", got)
	}
}

// When a snapshot directory is now a symlink on disk, its contents look
// unchanged through the link. They still have to be written, because the
// symlink is moved aside and replaced with a real, empty directory.
func TestChildrenOfReplacedDirAreWritten(t *testing.T) {
	dir := archive.Entry{Path: "d", Type: archive.TypeDir, Mode: 0o755, ModTime: time.Now()}
	child := fileEntry("d/f", "same")
	arc, idx := build(t, map[string]string{"d/f": "same"}, []archive.Entry{dir, child})

	out := t.TempDir()
	put(t, filepath.Join(out, "elsewhere/f"), "same")
	if err := os.Symlink("elsewhere", filepath.Join(out, "d")); err != nil {
		t.Fatal(err)
	}

	if _, err := restore.Apply(arc, "", restore.Plan(idx, nil, out), restore.Options{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(out, "d"))
	if err != nil || !info.IsDir() {
		t.Fatalf("d should be a real directory: %v %v", info, err)
	}
	got, err := os.ReadFile(filepath.Join(out, "d/f"))
	if err != nil {
		t.Fatalf("child was not restored: %v", err)
	}
	if string(got) != "same" {
		t.Errorf("child content %q", got)
	}
}

// Restoring one file from a private folder onto a fresh machine must not
// create that folder readable by other users.
func TestMissingParentsArePrivate(t *testing.T) {
	arc, idx := build(t, map[string]string{".ssh/id": "key"}, []archive.Entry{fileEntry(".ssh/id", "key")})
	out := t.TempDir()
	if _, err := restore.Apply(arc, "", restore.Plan(idx, nil, out), restore.Options{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(out, ".ssh"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("parent mode %o, want 700", perm)
	}
}

// A read-only directory has to end up read-only, but only after its contents
// have been written into it.
func TestReadOnlyDirectory(t *testing.T) {
	dir := archive.Entry{Path: "ro", Type: archive.TypeDir, Mode: 0o500, ModTime: time.Now()}
	child := fileEntry("ro/f", "x")
	arc, idx := build(t, map[string]string{"ro/f": "x"}, []archive.Entry{dir, child})

	out := t.TempDir()
	t.Cleanup(func() { os.Chmod(filepath.Join(out, "ro"), 0o700) })

	if _, err := restore.Apply(arc, "", restore.Plan(idx, nil, out), restore.Options{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(out, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o500 {
		t.Errorf("mode %o, want 500", perm)
	}
	if _, err := os.Stat(filepath.Join(out, "ro/f")); err != nil {
		t.Errorf("child missing: %v", err)
	}
	if p := restore.Pending(restore.Plan(idx, nil, out)); len(p) != 0 {
		t.Errorf("expected nothing pending, got %d", len(p))
	}
}

func TestForceOverwritesWithoutBackup(t *testing.T) {
	arc, idx := build(t, map[string]string{"f": "new"}, []archive.Entry{fileEntry("f", "new")})
	out := t.TempDir()
	put(t, filepath.Join(out, "f"), "old")

	moved, err := restore.Apply(arc, "", restore.Plan(idx, nil, out), restore.Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 0 {
		t.Errorf("force kept a copy: %v", moved)
	}
	if got, _ := os.ReadFile(filepath.Join(out, "f")); string(got) != "new" {
		t.Errorf("content %q", got)
	}
}

// A type change is always moved aside, even with Force.
func TestForceStillMovesTypeChanges(t *testing.T) {
	arc, idx := build(t, map[string]string{"f": "new"}, []archive.Entry{fileEntry("f", "new")})
	out := t.TempDir()
	put(t, filepath.Join(out, "f/inner"), "keep me")

	moved, err := restore.Apply(arc, "", restore.Plan(idx, nil, out), restore.Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 {
		t.Fatalf("moved %v", moved)
	}
	if got, _ := os.ReadFile(filepath.Join(moved[0], "inner")); string(got) != "keep me" {
		t.Errorf("directory contents not kept: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(out, "f")); string(got) != "new" {
		t.Errorf("content %q", got)
	}
}

func TestExtractFile(t *testing.T) {
	const content = "snapshot version\n"
	e := fileEntry(".zshrc", content)
	files := map[string]string{".zshrc": content, ".other": "x"}
	entries := []archive.Entry{fileEntry(".other", "x"), e}

	src := t.TempDir()
	for rel, c := range files {
		put(t, filepath.Join(src, rel), c)
	}
	encrypted := filepath.Join(t.TempDir(), "2026-01-01_000000.tar.zst.age")
	f, err := os.Create(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Write(f, src, archive.Index{Entries: entries}, "hunter2"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	plain, _ := build(t, files, entries)

	for _, tc := range []struct{ name, path, pass string }{
		{"plain", plain, ""},
		{"encrypted", encrypted, "hunter2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), ".zshrc")
			if err := restore.ExtractFile(tc.path, tc.pass, e, dst); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(dst)
			if err != nil || string(got) != content {
				t.Errorf("content = %q, %v", got, err)
			}
			info, err := os.Stat(dst)
			if err != nil || info.Mode().Perm() != 0o400 {
				t.Errorf("mode = %v, %v", info.Mode(), err)
			}
		})
	}
}

func TestExtractFileRefusesCorruptContent(t *testing.T) {
	e := fileEntry(".zshrc", "what the index says")
	e.Size = int64(len("what is stored!!!!!"))
	p, _ := build(t, map[string]string{".zshrc": "what is stored!!!!!"}, []archive.Entry{e})
	dst := filepath.Join(t.TempDir(), ".zshrc")
	if err := restore.ExtractFile(p, "", e, dst); err == nil {
		t.Fatal("expected a checksum error")
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Errorf("corrupt copy left behind: %v", err)
	}
}

func TestExtractFileMissingEntry(t *testing.T) {
	p, _ := build(t, map[string]string{".a": "a"}, []archive.Entry{fileEntry(".a", "a")})
	dst := filepath.Join(t.TempDir(), "out")
	if err := restore.ExtractFile(p, "", fileEntry(".nope", "b"), dst); err == nil {
		t.Fatal("expected an error for a missing entry")
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Errorf("file left behind: %v", err)
	}
}
