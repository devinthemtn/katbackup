package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/devinthemtn/katbackup/internal/snapshot"
)

func snapModel(t *testing.T, names ...string) (model, []string) {
	t.Helper()
	dir := t.TempDir()
	var items []list.Item
	var paths []string
	for _, n := range names {
		p := filepath.Join(dir, n+snapshot.Ext)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		s, _ := snapshot.Parse(p)
		items = append(items, snapItem{s})
		paths = append(paths, p)
	}
	l := list.New(items, list.NewDefaultDelegate(), 80, 20)
	return model{screen: screenSnapshots, snaps: l, width: 80, height: 20}, paths
}

func TestDeleteSnapshotConfirmed(t *testing.T) {
	m, paths := snapModel(t, "2026-10-27_100000", "2026-10-26_100000")
	next := press(m, "x", "y")
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("highlighted snapshot still exists: %v", err)
	}
	if _, err := os.Stat(paths[1]); err != nil {
		t.Fatalf("other snapshot was touched: %v", err)
	}
	if got := len(next.snaps.Items()); got != 1 {
		t.Fatalf("list has %d items, want 1", got)
	}
	if next.err != nil || next.deleting != nil {
		t.Fatalf("err=%v deleting=%v", next.err, next.deleting)
	}
}

func TestDeleteSnapshotCancelled(t *testing.T) {
	for _, k := range []string{"n", "esc", "q", "x"} {
		m, paths := snapModel(t, "2026-10-27_100000")
		next := press(m, "x", k)
		if _, err := os.Stat(paths[0]); err != nil {
			t.Fatalf("%q: snapshot deleted without y: %v", k, err)
		}
		if next.deleting != nil || len(next.snaps.Items()) != 1 {
			t.Fatalf("%q: prompt still open or item gone", k)
		}
	}
}

func TestDeleteLastSnapshot(t *testing.T) {
	m, _ := snapModel(t, "2026-10-27_100000")
	next := press(m, "x", "y", "x", "enter")
	if len(next.snaps.Items()) != 0 || next.deleting != nil || next.screen != screenSnapshots {
		t.Fatalf("unexpected state after deleting the only snapshot")
	}
	_ = next.View()
}

func press(m model, keys ...string) model {
	for _, k := range keys {
		km := key(k)
		if k == "esc" {
			km = tea.KeyMsg{Type: tea.KeyEsc}
		}
		n, _ := m.Update(km)
		m = n.(model)
		_ = m.View()
	}
	return m
}
