// Package tui is katbackup's interactive restore browser and init picker.
package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/restore"
	"github.com/devinthemtn/katbackup/internal/snapshot"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	cursorStyle = lipgloss.NewStyle().Background(lipgloss.Color("237")).Bold(true)
	newStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	modStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	sameStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	dirStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
)

type screen int

const (
	screenSnapshots screen = iota
	screenPassphrase
	screenLoading
	screenTree
	screenConfirm
	screenApplying
	screenDone
)

type snapItem struct{ s snapshot.Snapshot }

func (i snapItem) Title() string { return i.s.Name }
func (i snapItem) Description() string {
	d := i.s.Time.Format("Mon Jan 2 2006 15:04") + fmt.Sprintf(" · %.1f MiB", float64(i.s.Size)/(1<<20))
	if i.s.Encrypted {
		d += " · 🔒 encrypted"
	}
	return d
}
func (i snapItem) FilterValue() string { return i.s.Name }

type loadedMsg struct {
	idx  archive.Index
	pass string
	plan []restore.Action
	err  error
}
type progressMsg struct{ done, total int }
type appliedMsg struct {
	moved []string
	err   error
}

type model struct {
	home   string
	screen screen
	width  int
	height int
	err    error

	snaps list.Model
	snap  snapshot.Snapshot
	pass  textinput.Model

	idx      archive.Index
	password string
	root     *node
	rows     []*node
	cursor   int
	offset   int
	filter   textinput.Model
	filterOn bool

	pending    []restore.Action
	keepBackup bool
	confOffset int

	bar      progress.Model
	progCh   chan tea.Msg
	done     int
	total    int
	moved    []string
	applyErr error
}

// Run starts the restore browser.
func Run(home, backupDir string) error {
	snaps, err := snapshot.List(backupDir)
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		return fmt.Errorf("no snapshots in %s; run `katbackup backup` first", backupDir)
	}
	items := make([]list.Item, len(snaps))
	for i, s := range snaps {
		items[i] = snapItem{s}
	}
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "katbackup · choose a snapshot to restore from"
	l.Styles.Title = titleStyle

	pass := textinput.New()
	pass.EchoMode = textinput.EchoPassword
	pass.Placeholder = "passphrase"

	filter := textinput.New()
	filter.Prompt = "/"
	filter.Placeholder = "filter paths"

	m := model{
		home: home, snaps: l, pass: pass, filter: filter, keepBackup: true,
		bar: progress.New(progress.WithDefaultGradient()),
	}
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m model) Init() tea.Cmd { return nil }

func (m model) load(pass string) tea.Cmd {
	s, home := m.snap, m.home
	return func() tea.Msg {
		idx, err := archive.ReadIndex(s.Path, pass)
		if err != nil {
			return loadedMsg{err: err}
		}
		return loadedMsg{idx: idx, pass: pass, plan: restore.Plan(idx, nil, home)}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.snaps.SetSize(msg.Width, msg.Height)
		m.bar.Width = min(msg.Width-4, 60)
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case loadedMsg:
		if msg.err != nil {
			m.err = msg.err
			if errors.Is(msg.err, archive.ErrBadPassphrase) {
				m.screen = screenPassphrase
				m.pass.SetValue("")
				return m, m.pass.Focus()
			}
			m.screen = screenSnapshots
			return m, nil
		}
		m.err = nil
		m.idx, m.password = msg.idx, msg.pass
		m.root = buildTree(msg.idx.Entries, msg.plan)
		m.cursor, m.offset = 0, 0
		m.refresh()
		m.screen = screenTree
		return m, nil
	case progressMsg:
		m.done, m.total = msg.done, msg.total
		return m, waitFor(m.progCh)
	case appliedMsg:
		m.moved, m.applyErr = msg.moved, msg.err
		m.screen = screenDone
		return m, nil
	}

	switch m.screen {
	case screenSnapshots:
		return m.updateSnapshots(msg)
	case screenPassphrase:
		return m.updatePassphrase(msg)
	case screenTree:
		return m.updateTree(msg)
	case screenConfirm:
		return m.updateConfirm(msg)
	case screenDone:
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "q", "esc":
				return m, tea.Quit
			case "enter":
				m.screen = screenSnapshots
			}
		}
	}
	return m, nil
}

func (m model) updateSnapshots(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && m.snaps.FilterState() != list.Filtering {
		switch k.String() {
		case "q":
			return m, tea.Quit
		case "enter":
			it, ok := m.snaps.SelectedItem().(snapItem)
			if !ok {
				return m, nil
			}
			m.snap, m.err = it.s, nil
			if it.s.Encrypted {
				m.screen = screenPassphrase
				m.pass.SetValue("")
				return m, m.pass.Focus()
			}
			m.screen = screenLoading
			return m, m.load("")
		}
	}
	var cmd tea.Cmd
	m.snaps, cmd = m.snaps.Update(msg)
	return m, cmd
}

func (m model) updatePassphrase(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			m.pass.Blur()
			m.screen = screenSnapshots
			return m, nil
		case "enter":
			m.pass.Blur()
			m.screen = screenLoading
			return m, m.load(m.pass.Value())
		}
	}
	var cmd tea.Cmd
	m.pass, cmd = m.pass.Update(msg)
	return m, cmd
}

func (m *model) refresh() {
	m.rows = flatten(m.root, m.filter.Value())
	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
}

func (m model) treeHeight() int { return max(3, m.height-6) }

func (m *model) clampScroll() {
	h := m.treeHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
}

func (m model) updateTree(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.filterOn {
		switch k.String() {
		case "enter", "esc":
			m.filterOn = false
			m.filter.Blur()
			if k.String() == "esc" {
				m.filter.SetValue("")
			}
			m.refresh()
			return m, nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.cursor, m.offset = 0, 0
		m.refresh()
		return m, cmd
	}
	var cur *node
	if m.cursor < len(m.rows) {
		cur = m.rows[m.cursor]
	}
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		m.screen = screenSnapshots
		return m, nil
	case "up", "k":
		m.cursor = max(0, m.cursor-1)
	case "down", "j":
		m.cursor = min(len(m.rows)-1, m.cursor+1)
	case "pgup":
		m.cursor = max(0, m.cursor-m.treeHeight())
	case "pgdown":
		m.cursor = min(len(m.rows)-1, m.cursor+m.treeHeight())
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = len(m.rows) - 1
	case "right", "l":
		if cur != nil && cur.isDir() {
			cur.expanded = true
		}
	case "left", "h":
		if cur != nil && cur.isDir() && cur.expanded {
			cur.expanded = false
		} else if cur != nil && cur.parent != nil && cur.parent.depth >= 0 {
			for i, r := range m.rows {
				if r == cur.parent {
					m.cursor = i
				}
			}
		}
	case " ", "x":
		if cur != nil {
			sel, total := cur.selection()
			cur.setSelected(sel < total)
		}
	case "a":
		sel, total := m.root.selection()
		m.root.setSelected(sel < total)
	case "c":
		// Select only what differs from the files on disk.
		m.root.walk(func(n *node) {
			n.selected = n.entry != nil && n.status != restore.Unchanged
		})
	case "/":
		m.filterOn = true
		return m, m.filter.Focus()
	case "enter":
		keep := selectedPaths(m.root)
		if len(keep) == 0 {
			m.err = errors.New("nothing selected (space to select, a for all)")
			return m, nil
		}
		m.err = nil
		plan := restore.Plan(m.idx, func(e archive.Entry) bool { return keep[e.Path] }, m.home)
		m.pending = restore.Pending(plan)
		m.confOffset = 0
		m.screen = screenConfirm
		return m, nil
	}
	m.refresh()
	m.clampScroll()
	return m, nil
}

func (m model) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "esc", "n":
		m.screen = screenTree
	case "q":
		return m, tea.Quit
	case "b":
		m.keepBackup = !m.keepBackup
	case "up", "k":
		m.confOffset = max(0, m.confOffset-1)
	case "down", "j":
		m.confOffset = min(max(0, len(m.pending)-1), m.confOffset+1)
	case "enter", "y":
		if len(m.pending) == 0 {
			m.screen = screenTree
			return m, nil
		}
		m.screen = screenApplying
		m.done, m.total = 0, len(m.pending)
		m.progCh = make(chan tea.Msg, 64)
		go func(ch chan tea.Msg, path, pass string, actions []restore.Action, force bool) {
			moved, err := restore.Apply(path, pass, actions, restore.Options{
				Force:    force,
				Progress: func(d, t int) { ch <- progressMsg{d, t} },
			})
			ch <- appliedMsg{moved, err}
		}(m.progCh, m.snap.Path, m.password, m.pending, !m.keepBackup)
		return m, waitFor(m.progCh)
	}
	return m, nil
}

func waitFor(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func (m model) View() string {
	switch m.screen {
	case screenSnapshots:
		v := m.snaps.View()
		if m.err != nil {
			v += "\n" + errStyle.Render(m.err.Error())
		}
		return v
	case screenPassphrase:
		v := titleStyle.Render("🔒 "+m.snap.Name) + "\n\n" + m.pass.View() + "\n\n"
		if m.err != nil {
			v += errStyle.Render(m.err.Error()) + "\n"
		}
		return v + helpStyle.Render("enter unlock · esc back")
	case screenLoading:
		return titleStyle.Render("Reading "+m.snap.Name+" and comparing with your files…") + "\n"
	case screenTree:
		return m.viewTree()
	case screenConfirm:
		return m.viewConfirm()
	case screenApplying:
		pct := 0.0
		if m.total > 0 {
			pct = float64(m.done) / float64(m.total)
		}
		return titleStyle.Render("Restoring…") + "\n\n" + m.bar.ViewAs(pct) +
			fmt.Sprintf("\n\n%d / %d", m.done, m.total)
	case screenDone:
		var b strings.Builder
		if m.applyErr != nil {
			b.WriteString(errStyle.Render("Restore failed: "+m.applyErr.Error()) + "\n\n")
		} else {
			b.WriteString(titleStyle.Render(fmt.Sprintf("Restored %d entries ✓", m.total)) + "\n\n")
		}
		for _, p := range m.moved {
			b.WriteString("kept previous version at " + p + "\n")
		}
		b.WriteString("\n" + helpStyle.Render("enter back to snapshots · q quit"))
		return b.String()
	}
	return ""
}

func (m model) viewTree() string {
	var b strings.Builder
	sel, total := m.root.selection()
	b.WriteString(titleStyle.Render(fmt.Sprintf("%s · %d/%d selected", m.snap.Name, sel, total)) + "\n")
	if m.filterOn || m.filter.Value() != "" {
		b.WriteString(m.filter.View())
	}
	b.WriteString("\n")
	h := m.treeHeight()
	for i := m.offset; i < len(m.rows) && i < m.offset+h; i++ {
		n := m.rows[i]
		s, t := n.selection()
		box := "[ ]"
		switch {
		case t > 0 && s == t:
			box = "[x]"
		case s > 0:
			box = "[-]"
		}
		arrow := "  "
		name := n.name
		if n.isDir() {
			arrow = "▸ "
			if n.expanded || m.filter.Value() != "" {
				arrow = "▾ "
			}
			name = dirStyle.Render(name + "/")
		} else if n.entry != nil && n.entry.Type == archive.TypeSymlink {
			name += " → " + n.entry.LinkTarget
		}
		line := fmt.Sprintf("%s %s%s%s  %s", box, strings.Repeat("  ", n.depth), arrow, name, statusTag(n.status))
		if i == m.cursor {
			line = cursorStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	for i := len(m.rows) - m.offset; i < h; i++ {
		b.WriteString("\n")
	}
	if m.err != nil {
		b.WriteString(errStyle.Render(m.err.Error()) + "\n")
	}
	b.WriteString(helpStyle.Render("space select · a all · c changed only · ←/→ fold · / filter · enter restore · esc back · q quit"))
	return b.String()
}

func statusTag(k restore.Kind) string {
	switch k {
	case restore.Create:
		return newStyle.Render("new")
	case restore.Overwrite:
		return modStyle.Render("modified")
	default:
		return sameStyle.Render("same")
	}
}

func (m model) viewConfirm() string {
	var b strings.Builder
	var creates, overwrites int
	for _, a := range m.pending {
		if a.Kind == restore.Create {
			creates++
		} else {
			overwrites++
		}
	}
	b.WriteString(titleStyle.Render("Confirm restore") + "\n\n")
	if len(m.pending) == 0 {
		b.WriteString("Everything selected already matches the snapshot. Nothing to do.\n\n")
		b.WriteString(helpStyle.Render("enter/esc back"))
		return b.String()
	}
	fmt.Fprintf(&b, "%s new · %s overwrite\n", newStyle.Render(fmt.Sprint(creates)), modStyle.Render(fmt.Sprint(overwrites)))
	check := "[ ]"
	if m.keepBackup {
		check = "[x]"
	}
	fmt.Fprintf(&b, "%s keep a .katbak copy of files that get overwritten (b)\n\n", check)
	h := max(3, m.height-10)
	for i := m.confOffset; i < len(m.pending) && i < m.confOffset+h; i++ {
		a := m.pending[i]
		fmt.Fprintf(&b, "%s %s\n", statusTag(a.Kind), a.Target)
	}
	b.WriteString("\n" + helpStyle.Render("enter/y restore · b toggle backup · ↑/↓ scroll · esc back"))
	return b.String()
}
