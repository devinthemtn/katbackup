package tui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/devinthemtn/katbackup/internal/config"
	"github.com/devinthemtn/katbackup/internal/paths"
)

func backupModel(t *testing.T) model {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("alias ll='ls -l'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".netrc"), []byte("machine x password y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pass := textinput.New()
	pass.EchoMode = textinput.EchoPassword
	return model{
		pass: pass, home: home, backupDir: paths.BackupDir(home), screen: screenSnapshots,
		cfg:   config.Config{Include: []string{"~/.zshrc"}, Secrets: []string{"~/.netrc"}},
		snaps: list.New(nil, list.NewDefaultDelegate(), 80, 20), width: 80, height: 20,
	}
}

// finish waits for the running backup and feeds its result back in.
func finish(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if m.screen != screenBackingUp || cmd == nil {
		t.Fatalf("backup didn't start: screen %d, err %v", m.screen, m.err)
	}
	_ = m.View()
	n, _ := m.Update(cmd())
	m = n.(model)
	if m.screen != screenBackupDone {
		t.Fatalf("screen %d after backup", m.screen)
	}
	_ = m.View()
	return m
}

func send(m model, msg tea.Msg) (model, tea.Cmd) {
	n, cmd := m.Update(msg)
	return n.(model), cmd
}

func TestNewSnapshot(t *testing.T) {
	m, cmd := send(backupModel(t), key("n"))
	m = finish(t, m, cmd)
	if m.bkErr != nil {
		t.Fatal(m.bkErr)
	}
	items := m.snaps.Items()
	if len(items) != 1 || items[0].(snapItem).s.Encrypted {
		t.Fatalf("want one plain snapshot in the list, got %v", items)
	}

	// Nothing changed, so a second one is skipped until asked for anyway.
	m, _ = send(m, key("enter"))
	m, cmd = send(m, key("n"))
	m = finish(t, m, cmd)
	if m.bkRes.UnchangedSince == nil || len(m.snaps.Items()) != 1 {
		t.Fatalf("expected an unchanged skip, got %+v", m.bkRes)
	}
	m, cmd = send(m, key("a"))
	m = finish(t, m, cmd)
	if m.bkErr != nil || len(m.snaps.Items()) != 2 {
		t.Fatalf("write anyway: err %v, %d snapshots", m.bkErr, len(m.snaps.Items()))
	}
}

func TestNewEncryptedSnapshot(t *testing.T) {
	m, cmd := send(backupModel(t), key("e"))
	if m.screen != screenNewPass {
		t.Fatalf("screen %d, want passphrase entry", m.screen)
	}
	m, _ = send(m, key("hunter2"))
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = send(m, key("wrong"))
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenNewPass || m.err == nil {
		t.Fatalf("mismatched passphrases weren't caught")
	}
	_ = m.View()
	m, _ = send(m, key("hunter2"))
	m, cmd = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = finish(t, m, cmd)
	if m.bkErr != nil {
		t.Fatal(m.bkErr)
	}
	s := m.snaps.SelectedItem().(snapItem).s
	if !s.Encrypted {
		t.Fatalf("snapshot isn't encrypted: %s", s.Path)
	}
	// Opening it with the passphrase shows the secret was included.
	m, _ = send(m, key("enter"))
	m, _ = send(m, key("enter"))
	m, _ = send(m, key("hunter2"))
	m, cmd = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = send(m, cmd())
	if m.screen != screenTree {
		t.Fatalf("couldn't open the snapshot: %v", m.err)
	}
	found := false
	for _, e := range m.idx.Entries {
		found = found || e.Path == ".netrc"
	}
	if !found {
		t.Fatal("encrypted snapshot is missing the secret")
	}
}

func TestNewSnapshotWithoutConfig(t *testing.T) {
	m := backupModel(t)
	m.cfgErr = errors.New("no config file yet")
	for _, k := range []string{"n", "e"} {
		next, _ := send(m, key(k))
		if next.screen != screenSnapshots || next.err == nil {
			t.Fatalf("%q: expected an error on the snapshot list", k)
		}
		_ = next.View()
	}
}

func TestQuitDuringBackupWaitsForCleanup(t *testing.T) {
	m, cmd := send(backupModel(t), key("n"))
	m, quit := send(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit != nil || !m.bkQuit {
		t.Fatal("ctrl+c quit before the backup stopped")
	}
	m, quit = send(m, cmd())
	if quit == nil {
		t.Fatal("didn't quit once the backup finished")
	}
	staging := filepath.Join(m.backupDir, paths.StagingName)
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging left behind: %v", err)
	}
}
