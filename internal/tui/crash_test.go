package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/devinthemtn/katbackup/internal/archive"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// With a filter that matches nothing there are no rows, so moving the cursor
// must not push it below zero.
func TestTreeKeysWithNoRows(t *testing.T) {
	for _, k := range []string{"j", "down", "pgdown", "G", "k", "g", " ", "h", "l", "c", "a", "d", "?"} {
		t.Run(k, func(t *testing.T) {
			filter := textinput.New()
			filter.SetValue("matches-nothing")
			m := model{
				screen: screenTree, height: 20, filter: filter,
				root: buildTree([]archive.Entry{{Path: ".zshrc", Type: archive.TypeFile}}, nil),
			}
			m.refresh()
			if len(m.rows) != 0 {
				t.Fatalf("expected no rows, got %d", len(m.rows))
			}
			next, _ := m.Update(key(k))
			next, _ = next.Update(key(k)) // the second key reads the moved cursor
			_ = next.View()
		})
	}
}

func TestInitKeysWithNoCandidates(t *testing.T) {
	for _, k := range []string{"j", "down", "pgdown", "k", " "} {
		t.Run(k, func(t *testing.T) {
			var m tea.Model = initModel{height: 20}
			m, _ = m.Update(key(k))
			m, _ = m.Update(key(k))
			_ = m.View()
		})
	}
}
