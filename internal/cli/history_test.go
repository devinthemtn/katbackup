package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/backup"
	"github.com/devinthemtn/katbackup/internal/config"
	"github.com/devinthemtn/katbackup/internal/restore"
	"github.com/devinthemtn/katbackup/internal/snapshot"
)

func entry(p, sum string) archive.Entry {
	return archive.Entry{Path: p, Type: archive.TypeFile, Mode: 0o644, SHA256: sum}
}

func loaded(name string, encrypted bool, entries ...archive.Entry) loadedSnapshot {
	return loadedSnapshot{snap: snapshot.Snapshot{Name: name, Encrypted: encrypted}, entries: entries}
}

func rowsText(rows []logRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, strings.TrimSpace(fmt.Sprintf("%s %s %s", r.Snapshot, r.Kind, r.Path)))
	}
	return out
}

func TestFileHistory(t *testing.T) {
	all := func(archive.Entry) bool { return true }
	for _, tc := range []struct {
		name  string
		snaps []loadedSnapshot // newest first
		keep  func(archive.Entry) bool
		want  []string
	}{
		{
			name: "added, modified, removed, newest first",
			snaps: []loadedSnapshot{
				loaded("4", false),
				loaded("3", false, entry("f", "2")),
				loaded("2", false, entry("f", "1")),
				loaded("1", false, entry("f", "1")),
			},
			keep: all,
			want: []string{"4 removed f", "3 modified f", "1 added f"},
		},
		{
			name: "a secret isn't removed by a plain snapshot leaving it out",
			snaps: []loadedSnapshot{
				loaded("4", false, entry("z", "1")),
				loaded("3", true, entry("s", "1"), entry("z", "1")),
				loaded("2", false, entry("z", "1")),
				loaded("1", true, entry("s", "1"), entry("z", "1")),
			},
			keep: all,
			want: []string{"1 added s", "1 added z"},
		},
		{
			name: "an encrypted snapshot lacking a secret means it really went",
			snaps: []loadedSnapshot{
				loaded("3", true),
				loaded("2", false),
				loaded("1", true, entry("s", "1")),
			},
			keep: all,
			want: []string{"3 removed s", "1 added s"},
		},
		{
			name: "a change in a plain snapshot is still seen after an encrypted one",
			snaps: []loadedSnapshot{
				loaded("2", false, entry("f", "2")),
				loaded("1", true, entry("f", "1")),
			},
			keep: all,
			want: []string{"2 modified f", "1 added f"},
		},
		{
			name: "folders aren't listed, and globs narrow it",
			snaps: []loadedSnapshot{
				loaded("1", false,
					archive.Entry{Path: ".config", Type: archive.TypeDir},
					entry(".config/a.toml", "1"),
					entry(".config/b.lua", "1")),
			},
			keep: restore.Filter([]string{".config/*.toml"}, "/home/me"),
			want: []string{"1 added .config/a.toml"},
		},
		{
			name: "a locked snapshot is listed and doesn't stop the walk",
			snaps: []loadedSnapshot{
				loaded("3", false, entry("f", "2")),
				{snap: snapshot.Snapshot{Name: "2", Encrypted: true}, unreadable: kindLocked},
				loaded("1", false, entry("f", "1")),
			},
			keep: all,
			want: []string{"3 modified f", "2 locked", "1 added f"},
		},
		{
			name:  "nothing matches",
			snaps: []loadedSnapshot{loaded("1", false, entry("f", "1"))},
			keep:  func(archive.Entry) bool { return false },
			want:  []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := rowsText(fileHistory(tc.snaps, tc.keep))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestVisibleChanges(t *testing.T) {
	dir := func(p string) archive.Entry { return archive.Entry{Path: p, Type: archive.TypeDir, Mode: 0o755} }
	f := entry("full/sub/file", "1")
	full, sub, empty := dir("full"), dir("full/sub"), dir("empty")
	chmodBefore, chmodAfter := dir("chmod"), dir("chmod")
	chmodAfter.Mode = 0o700
	got := visibleChanges(archive.Diff(
		[]archive.Entry{chmodBefore},
		[]archive.Entry{full, sub, f, empty, chmodAfter},
	))
	var paths []string
	for _, c := range got {
		paths = append(paths, c.Path)
	}
	want := []string{"chmod", "empty", "full/sub/file"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("got %v, want %v", paths, want)
	}
	if none := visibleChanges(nil); none == nil {
		t.Error("nil result would encode as JSON null")
	}
}

func TestHumanAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now",
		time.Minute:      "1 minute ago",
		3 * time.Hour:    "3 hours ago",
		50 * time.Hour:   "2 days ago",
	} {
		if got := humanAge(d); got != want {
			t.Errorf("%v: got %q, want %q", d, got, want)
		}
	}
}

// encryptedSnapshot writes a real encrypted snapshot with the given passphrase.
func encryptedSnapshot(t *testing.T, home, pass string, at time.Time) snapshot.Snapshot {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Include = []string{"~/.zshrc"}
	res, err := backup.Run(backup.Options{Home: home, Config: cfg, Passphrase: pass, Now: at})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := snapshot.Parse(res.Archive)
	if !ok {
		t.Fatalf("can't parse %s", res.Archive)
	}
	return s
}

func TestUnlockerOnce(t *testing.T) {
	home := t.TempDir()
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)
	right := encryptedSnapshot(t, home, "right", base)
	other := encryptedSnapshot(t, home, "other", base.Add(time.Minute))

	t.Setenv(passEnv, "right")
	u := unlocker{once: true}
	if _, err := u.index(right); err != nil {
		t.Fatalf("right passphrase: %v", err)
	}
	if _, err := u.index(other); !errors.Is(err, archive.ErrBadPassphrase) {
		t.Errorf("other passphrase: got %v, want a locked snapshot", err)
	}

	skip := unlocker{once: true, skip: true}
	if _, err := skip.index(right); !errors.Is(err, errSkipEncrypted) {
		t.Errorf("--no-encrypted: got %v", err)
	}
}

// Runs the real commands end to end in a throwaway home folder.
func runCLI(t *testing.T, args ...string) error {
	t.Helper()
	dirFlag = ""
	t.Cleanup(func() { dirFlag = "" })
	root := newRoot()
	root.SetArgs(args)
	return root.Execute()
}

func cliHome(t *testing.T, config string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(dirEnv, "")
	t.Setenv(passEnv, "")
	cfg := filepath.Join(home, ".config", "katbackup", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestBackupStrict(t *testing.T) {
	home := cliHome(t, `include = ["~/.zshrc", "~/.missing"]`)
	snaps := func() int {
		list, err := snapshot.List(filepath.Join(home, ".katConfigBackup"))
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	if err := runCLI(t, "backup"); err != nil {
		t.Fatalf("warnings without --strict failed: %v", err)
	}
	err := runCLI(t, "backup", "--strict", "--always")
	if err == nil || !strings.Contains(err.Error(), "warning") {
		t.Errorf("--strict with a warning: got %v", err)
	}
	if got := snaps(); got != 2 {
		t.Errorf("--strict should still write the snapshot; have %d", got)
	}
}

func TestBackupStrictPassesWithOnlyNotes(t *testing.T) {
	cliHome(t, `include = ["~/.zshrc"]
secrets = ["~/.ssh"]`)
	if err := runCLI(t, "backup", "--strict"); err != nil {
		t.Errorf("a note failed --strict: %v", err)
	}
}

func TestStatusDiffLogRun(t *testing.T) {
	home := cliHome(t, `include = ["~/.zshrc"]`)
	if err := runCLI(t, "status"); !errors.Is(err, snapshot.ErrNone) {
		t.Errorf("status with no snapshots: %v", err)
	}
	if err := runCLI(t, "backup"); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(t, "diff"); err == nil {
		t.Error("diff with one snapshot should say it needs two")
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Snapshot names have one-second resolution; NewPath adds a suffix if needed.
	if err := runCLI(t, "backup"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"status"}, {"status", "--json"},
		{"diff"}, {"diff", "--json"}, {"diff", "-p", ".zshrc"},
		{"log", ".zshrc"}, {"log", ".zshrc", "--json"},
	} {
		if err := runCLI(t, args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
}
