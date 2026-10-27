package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/devinthemtn/katbackup/internal/scan"
)

type initModel struct {
	cands    []scan.Candidate
	selected []bool
	cursor   int
	offset   int
	height   int
	saved    bool
}

// RunInit lets the user pick which scanned paths go in the manifest.
// It returns "~/"-prefixed includes and secrets, and whether the user saved.
func RunInit(cands []scan.Candidate) (includes, secrets []string, ok bool, err error) {
	m := initModel{cands: cands, selected: make([]bool, len(cands)), height: 20}
	for i, c := range cands {
		m.selected[i] = c.Suggested
	}
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, nil, false, err
	}
	fm := final.(initModel)
	if !fm.saved {
		return nil, nil, false, nil
	}
	for i, c := range fm.cands {
		if !fm.selected[i] {
			continue
		}
		if c.Has(scan.FlagSecret) {
			secrets = append(secrets, "~/"+c.Path)
		} else {
			includes = append(includes, "~/"+c.Path)
		}
	}
	return includes, secrets, true, nil
}

func (m initModel) Init() tea.Cmd { return nil }

func (m initModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = max(3, msg.Height-5)
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(len(m.cands)-1, m.cursor+1)
		case "pgup":
			m.cursor = max(0, m.cursor-m.height)
		case "pgdown":
			m.cursor = min(len(m.cands)-1, m.cursor+m.height)
		case " ", "x":
			if len(m.cands) > 0 {
				m.selected[m.cursor] = !m.selected[m.cursor]
			}
		case "enter", "s":
			m.saved = true
			return m, tea.Quit
		}
		if m.cursor < m.offset {
			m.offset = m.cursor
		}
		if m.cursor >= m.offset+m.height {
			m.offset = m.cursor - m.height + 1
		}
	}
	return m, nil
}

func (m initModel) View() string {
	var b strings.Builder
	n := 0
	for _, s := range m.selected {
		if s {
			n++
		}
	}
	b.WriteString(titleStyle.Render(fmt.Sprintf("katbackup init · %d paths selected", n)) + "\n\n")
	for i := m.offset; i < len(m.cands) && i < m.offset+m.height; i++ {
		c := m.cands[i]
		box := "[ ]"
		if m.selected[i] {
			box = "[x]"
		}
		name := "~/" + c.Path
		if c.IsDir {
			name = dirStyle.Render(name + "/")
		}
		size := ""
		if c.Size >= 0 {
			size = sameStyle.Render(fmt.Sprintf("%.1f MiB", float64(c.Size)/(1<<20)))
		}
		var tags []string
		for _, f := range c.Flags {
			switch f {
			case scan.FlagSecret:
				tags = append(tags, modStyle.Render("🔒 secret (encrypted only)"))
			default:
				tags = append(tags, sameStyle.Render(string(f)))
			}
		}
		line := fmt.Sprintf("%s %s  %s  %s", box, name, size, strings.Join(tags, " "))
		if i == m.cursor {
			line = cursorStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + helpStyle.Render("space toggle · enter save · q cancel"))
	return b.String()
}
