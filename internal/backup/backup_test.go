package backup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/backup"
	"github.com/devinthemtn/katbackup/internal/config"
	"github.com/devinthemtn/katbackup/internal/restore"
	"github.com/devinthemtn/katbackup/internal/snapshot"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fakeHome(t *testing.T) (string, config.Config) {
	home := t.TempDir()
	write(t, home, ".zshrc", "export EDITOR=nvim\n")
	write(t, home, ".config/nvim/init.lua", "vim.o.number = true\n")
	write(t, home, ".config/nvim/lua/plugins.lua", "return {}\n")
	write(t, home, ".config/nvim/node_modules/junk.js", "x")
	write(t, home, ".config/gh/config.yml", "editor: nvim\n")
	write(t, home, ".config/gh/hosts.yml", "oauth_token: SECRET\n")
	write(t, home, ".ssh/id_ed25519", "PRIVATE\n")
	if err := os.Symlink("init.lua", filepath.Join(home, ".config/nvim/link.lua")); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Include = []string{"~/.zshrc", "~/.config/nvim", "~/.config/gh", "~/.missing"}
	cfg.Secrets = []string{"~/.ssh", "~/.config/gh/hosts.yml"}
	return home, cfg
}

func paths(idx archive.Index) map[string]archive.Entry {
	m := map[string]archive.Entry{}
	for _, e := range idx.Entries {
		m[e.Path] = e
	}
	return m
}

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	home, cfg := fakeHome(t)
	res, err := backup.Run(backup.Options{Home: home, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.Archive, ".tar.zst") {
		t.Fatalf("archive %s", res.Archive)
	}
	if _, err := os.Stat(filepath.Join(home, ".katConfigBackup/staging")); !os.IsNotExist(err) {
		t.Fatal("staging dir should be removed")
	}

	idx, err := archive.ReadIndex(res.Archive, "")
	if err != nil {
		t.Fatal(err)
	}
	got := paths(idx)
	for _, want := range []string{".zshrc", ".config/nvim/init.lua", ".config/nvim/lua/plugins.lua", ".config/gh/config.yml"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	for _, bad := range []string{".config/nvim/node_modules/junk.js", ".config/gh/hosts.yml", ".ssh/id_ed25519"} {
		if _, ok := got[bad]; ok {
			t.Errorf("%s should be excluded", bad)
		}
	}
	if l := got[".config/nvim/link.lua"]; l.Type != archive.TypeSymlink || l.LinkTarget != "init.lua" {
		t.Errorf("symlink not preserved: %+v", l)
	}

	out := t.TempDir()
	actions := restore.Plan(idx, nil, out)
	if _, err := restore.Apply(res.Archive, "", actions, restore.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(out, ".config/nvim/init.lua")); got != "vim.o.number = true\n" {
		t.Errorf("restored content %q", got)
	}
	info, _ := os.Stat(filepath.Join(out, ".zshrc"))
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if link, _ := os.Readlink(filepath.Join(out, ".config/nvim/link.lua")); link != "init.lua" {
		t.Errorf("link %q", link)
	}
	// A second plan against the restored tree should be a no-op.
	if p := restore.Pending(restore.Plan(idx, nil, out)); len(p) != 0 {
		t.Errorf("expected nothing pending, got %d", len(p))
	}
}

func TestSelectiveRestoreKeepsBackup(t *testing.T) {
	home, cfg := fakeHome(t)
	res, err := backup.Run(backup.Options{Home: home, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	write(t, home, ".zshrc", "broken\n")
	write(t, home, ".config/nvim/init.lua", "also changed\n")

	idx, _ := archive.ReadIndex(res.Archive, "")
	actions := restore.Plan(idx, restore.Filter([]string{"~/.zshrc"}, home), home)
	if len(actions) != 1 || actions[0].Kind != restore.Overwrite {
		t.Fatalf("plan %+v", actions)
	}
	moved, err := restore.Apply(res.Archive, "", actions, restore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(home, ".zshrc")) != "export EDITOR=nvim\n" {
		t.Error(".zshrc not restored")
	}
	if len(moved) != 1 || read(t, moved[0]) != "broken\n" {
		t.Errorf("previous version not kept: %v", moved)
	}
	if read(t, filepath.Join(home, ".config/nvim/init.lua")) != "also changed\n" {
		t.Error("unselected file was touched")
	}
}

func TestEncryptedIncludesSecrets(t *testing.T) {
	home, cfg := fakeHome(t)
	res, err := backup.Run(backup.Options{Home: home, Config: cfg, Passphrase: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.Archive, ".tar.zst.age") {
		t.Fatalf("archive %s", res.Archive)
	}
	if _, err := archive.ReadIndex(res.Archive, "wrong"); err != archive.ErrBadPassphrase {
		t.Fatalf("want ErrBadPassphrase, got %v", err)
	}
	idx, err := archive.ReadIndex(res.Archive, "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	got := paths(idx)
	for _, want := range []string{".ssh/id_ed25519", ".config/gh/hosts.yml"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing secret %s", want)
		}
	}
	out := t.TempDir()
	if _, err := restore.Apply(res.Archive, "hunter2", restore.Plan(idx, restore.Filter([]string{".ssh"}, home), out), restore.Options{}); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(out, ".ssh/id_ed25519")) != "PRIVATE\n" {
		t.Error("secret not restored")
	}
}

// A dry run of an encrypted backup must list what that backup would contain,
// secrets included, even though no passphrase has been asked for yet.
func TestEncryptedDryRunListsSecrets(t *testing.T) {
	home, cfg := fakeHome(t)
	res, err := backup.Run(backup.Options{Home: home, Config: cfg, Encrypt: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range res.Entries {
		found = found || e.Path == ".ssh/id_ed25519"
	}
	if !found {
		t.Error("secret missing from the encrypted dry run")
	}
	if _, err := os.Stat(filepath.Join(home, ".katConfigBackup")); !os.IsNotExist(err) {
		t.Error("dry run wrote to disk")
	}
}

func TestEncryptWithoutPassphraseFails(t *testing.T) {
	home, cfg := fakeHome(t)
	if _, err := backup.Run(backup.Options{Home: home, Config: cfg, Encrypt: true}); err == nil {
		t.Fatal("an encrypted backup ran with no passphrase")
	}
}

// failingBackup runs an encrypted backup whose archive can't be created,
// because a directory already sits at its temporary path.
func failingBackup(t *testing.T, keepDir bool) (string, backup.Result, error) {
	t.Helper()
	home, cfg := fakeHome(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)
	dir := filepath.Join(home, ".katConfigBackup")
	blocker := filepath.Join(dir, now.Format(snapshot.TimeFormat)+snapshot.EncExt+".partial")
	if err := os.MkdirAll(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := backup.Run(backup.Options{Home: home, Config: cfg, Passphrase: "pw", Now: now, KeepDir: keepDir})
	return filepath.Join(dir, "staging"), res, err
}

// Staging holds plaintext copies of secrets, so a failure must not leave it behind.
func TestFailedBackupRemovesStaging(t *testing.T) {
	staging, _, err := failingBackup(t, false)
	if err == nil {
		t.Fatal("expected the backup to fail")
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("plaintext staging left behind: %v", err)
	}
}

func TestKeepDirWithEncryptionWarns(t *testing.T) {
	home, cfg := fakeHome(t)
	res, err := backup.Run(backup.Options{Home: home, Config: cfg, Passphrase: "pw", KeepDir: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "unencrypted") {
		t.Errorf("no warning about the unencrypted staging copy: %v", res.Warnings)
	}
	if _, err := os.Stat(filepath.Join(res.Staging, ".ssh/id_ed25519")); err != nil {
		t.Errorf("staging not kept: %v", err)
	}
}

func TestPrune(t *testing.T) {
	home, cfg := fakeHome(t)
	cfg.Keep = 2
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)
	for i := 0; i < 4; i++ {
		if _, err := backup.Run(backup.Options{Home: home, Config: cfg, Now: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	snaps, _ := snapshot.List(filepath.Join(home, ".katConfigBackup"))
	if len(snaps) != 2 || snaps[0].Name != "2026-10-26_120300" {
		t.Fatalf("snapshots after prune: %+v", snaps)
	}
	s, err := snapshot.Find(filepath.Join(home, ".katConfigBackup"), "2026-10-26_1202")
	if err != nil || s.Name != "2026-10-26_120200" {
		t.Fatalf("find: %v %v", s, err)
	}
}

func TestCustomLocation(t *testing.T) {
	home, cfg := fakeHome(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	res, err := backup.Run(backup.Options{Home: home, Config: cfg, Dir: dir, KeepDir: true})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(res.Archive) != dir {
		t.Errorf("archive went to %s, want %s", res.Archive, dir)
	}
	if want := filepath.Join(home, ".katConfigBackup", "staging"); res.Staging != want {
		t.Errorf("staging is at %s, want %s", res.Staging, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "staging")); !os.IsNotExist(err) {
		t.Errorf("plaintext staging in the backup location: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Errorf("custom location's mode changed to %o", got)
	}
}

func TestMissingCustomLocationIsNotCreated(t *testing.T) {
	home, cfg := fakeHome(t)
	dir := filepath.Join(t.TempDir(), "unplugged", "drive")
	for name, dry := range map[string]bool{"backup": false, "dry run": true} {
		t.Run(name, func(t *testing.T) {
			if _, err := backup.Run(backup.Options{Home: home, Config: cfg, Dir: dir, DryRun: dry}); err == nil {
				t.Fatal("no error for a missing location")
			}
			if _, err := os.Stat(filepath.Dir(dir)); !os.IsNotExist(err) {
				t.Errorf("location was created: %v", err)
			}
		})
	}
}

func TestLocationInsideHomeIsNotBackedUp(t *testing.T) {
	home, cfg := fakeHome(t)
	// Brackets would be a glob if this were matched as a pattern.
	dir := filepath.Join(home, ".config", "nvim", "back[ups]")
	write(t, dir, "old-file", "from an earlier snapshot")
	res, err := backup.Run(backup.Options{Home: home, Config: cfg, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if strings.Contains(e.Path, "back[ups]") {
			t.Errorf("backup location was backed up: %s", e.Path)
		}
	}
	if len(res.Entries) == 0 {
		t.Error("nothing was backed up")
	}
}
