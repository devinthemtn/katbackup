// Package tui is katbackup's interactive restore browser and init picker.
package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	bubblekey "github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/backup"
	"github.com/devinthemtn/katbackup/internal/config"
	"github.com/devinthemtn/katbackup/internal/restore"
	"github.com/devinthemtn/katbackup/internal/snapshot"
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	helpStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	cursorStyle  = lipgloss.NewStyle().Background(lipgloss.Color("237")).Bold(true)
	newStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	modStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	sameStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	dirStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
	versionStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1).
			Foreground(lipgloss.Color("16")).Background(lipgloss.Color("81"))
	helpBoxStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("212")).Padding(0, 2)
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
	screenExtracting
	screenHelp
	screenNewPass
	screenBackingUp
	screenBackupDone
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
	home      string
	backupDir string
	cfg       config.Config
	cfgErr    error // why backups can't run, such as no config file yet
	screen    screen
	width     int
	height    int
	err       error

	snaps    list.Model
	snap     snapshot.Snapshot
	deleting *snapshot.Snapshot // asking whether to delete this one
	pass     textinput.Model

	idx      archive.Index
	password string
	root     *node
	rows     []*node
	cursor   int
	offset   int
	filter   textinput.Model
	filterOn bool

	diffTool string
	diffRoot string // private temp folder for extracted snapshot files
	diffing  string // path being extracted for comparison

	version    string
	helpOffset int
	helpReturn screen // the screen to go back to when help closes

	newPass    [2]textinput.Model // passphrase and confirmation for a new encrypted snapshot
	newFocus   int
	backup     *backupRun // the backup in progress, if any
	bkEncrypt  bool
	bkPass     string // kept until the result screen closes, for "write anyway"
	bkStopping bool
	bkQuit     bool // quit once the running backup has stopped
	bkRes      backup.Result
	bkErr      error

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

// Run starts the snapshot browser. cfg is used for making snapshots, which
// cfgErr (such as config.ErrNotFound) rules out; restoring works either way.
// version is shown on the help screen.
func Run(home, backupDir string, cfg config.Config, cfgErr error, version string) error {
	snaps, err := snapshot.List(backupDir)
	if err != nil {
		return err
	}
	if len(snaps) == 0 && cfgErr != nil {
		return fmt.Errorf("no snapshots in %s; run `katbackup init`, then `katbackup backup` or `katbackup tui`", backupDir)
	}
	items := make([]list.Item, len(snaps))
	for i, s := range snaps {
		items[i] = snapItem{s}
	}
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "katbackup · choose a snapshot to restore from"
	l.Styles.Title = titleStyle
	// updateSnapshots opens katbackup's help on this key, not the list's own.
	l.KeyMap.ShowFullHelp = bubblekey.NewBinding(bubblekey.WithKeys("?"), bubblekey.WithHelp("?", "help"))
	deleteKey := bubblekey.NewBinding(bubblekey.WithKeys("x"), bubblekey.WithHelp("x", "delete"))
	newKey := bubblekey.NewBinding(bubblekey.WithKeys("n"), bubblekey.WithHelp("n", "new snapshot"))
	encKey := bubblekey.NewBinding(bubblekey.WithKeys("e"), bubblekey.WithHelp("e", "new encrypted"))
	l.AdditionalShortHelpKeys = func() []bubblekey.Binding { return []bubblekey.Binding{newKey, encKey, deleteKey} }

	pass := textinput.New()
	pass.EchoMode = textinput.EchoPassword
	pass.Placeholder = "passphrase"

	filter := textinput.New()
	filter.Prompt = "/"
	filter.Placeholder = "filter paths"

	// Removed on every way out of the program, including ctrl+c mid-extract. A
	// failure only matters if a comparison is requested, which then reports it.
	diffRoot, err := newDiffRoot()
	if err == nil {
		defer os.RemoveAll(diffRoot)
	}

	m := model{
		home: home, backupDir: backupDir, cfg: cfg, cfgErr: cfgErr, snaps: l, pass: pass, filter: filter, keepBackup: true,
		bar:      progress.New(progress.WithDefaultGradient()),
		diffTool: cfg.DiffTool, diffRoot: diffRoot, version: version,
	}
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	// The program can end mid-backup, on a signal for instance. Let the backup
	// clean up its plaintext staging copy before the process exits.
	if fm, ok := final.(model); ok && fm.backup != nil {
		fm.backup.wait()
	}
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
			if m.backup != nil {
				m.stopBackup()
				m.bkQuit = true
				return m, nil
			}
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
	case diffReadyMsg:
		return m.diffReady(msg)
	case diffDoneMsg:
		return m.diffDone(msg)
	case backupDoneMsg:
		return m.backupDone(msg)
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
	case screenHelp:
		return m.updateHelp(msg)
	case screenNewPass:
		return m.updateNewPass(msg)
	case screenBackingUp:
		if k, ok := msg.(tea.KeyMsg); ok && (k.String() == "esc" || k.String() == "q") {
			m.stopBackup()
		}
		return m, nil
	case screenBackupDone:
		return m.updateBackupDone(msg)
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
	k, isKey := msg.(tea.KeyMsg)
	if isKey && m.deleting != nil {
		s := *m.deleting
		m.deleting = nil
		if k.String() == "y" {
			m.err = m.deleteSnapshot(s)
		}
		return m, nil
	}
	if isKey && m.snaps.FilterState() != list.Filtering {
		m.err = nil
		switch k.String() {
		case "q":
			return m, tea.Quit
		case "n", "e":
			return m.startNewSnapshot(k.String() == "e")
		case "x":
			if it, ok := m.snaps.SelectedItem().(snapItem); ok {
				m.deleting = &it.s
			}
			return m, nil
		case "?":
			m.openHelp()
			return m, nil
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

// deleteSnapshot removes s's archive and drops it from the list.
func (m *model) deleteSnapshot(s snapshot.Snapshot) error {
	if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete %s: %w", s.Name, err)
	}
	// Rebuild rather than RemoveItem: with a filter applied, the list's index
	// counts only the visible items, not the ones RemoveItem works on.
	var keep []list.Item
	for _, it := range m.snaps.Items() {
		if si, ok := it.(snapItem); !ok || si.s.Path != s.Path {
			keep = append(keep, it)
		}
	}
	m.snaps.SetItems(keep)
	return nil
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
	// Clamp below as well as above: with no rows, moving down computes -1.
	m.cursor = max(0, min(m.cursor, len(m.rows)-1))
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
	m.err = nil // a message is about the last key, so don't leave it up
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
	case "d":
		return m.startDiff(cur)
	case "?":
		m.openHelp()
		return m, nil
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
		var footer string
		switch {
		case m.deleting != nil:
			footer = errStyle.Render(fmt.Sprintf("Delete snapshot %s? This can't be undone. y delete · any other key cancel", m.deleting.Name))
		case m.err != nil:
			footer = errStyle.Render(m.err.Error())
		case len(m.snaps.Items()) == 0:
			footer = helpStyle.Render("No snapshots yet. Press n for a new snapshot or e for an encrypted one.")
		}
		if footer == "" {
			return m.snaps.View()
		}
		// Shrink the list so the footer doesn't push its title off screen.
		if m.height > 0 {
			m.snaps.SetHeight(max(1, m.height-lipgloss.Height(footer)-1))
		}
		return m.snaps.View() + "\n" + footer
	case screenPassphrase:
		v := titleStyle.Render("🔒 "+m.snap.Name) + "\n\n" + m.pass.View() + "\n\n"
		if m.err != nil {
			v += errStyle.Render(m.err.Error()) + "\n"
		}
		return v + helpStyle.Render("enter unlock · esc back")
	case screenLoading:
		return titleStyle.Render("Reading "+m.snap.Name+" and comparing with your files…") + "\n"
	case screenHelp:
		return m.viewHelp()
	case screenNewPass:
		return m.viewNewPass()
	case screenBackingUp:
		return m.viewBackingUp()
	case screenBackupDone:
		return m.viewBackupDone()
	case screenExtracting:
		return titleStyle.Render("Extracting "+m.diffing+" from "+m.snap.Name+"…") + "\n"
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
	b.WriteString(helpStyle.Render("? help · space select · a all · d compare · / filter · enter restore · q quit"))
	return b.String()
}

// HelpText describes the file browser. `katbackup tui --help` prints it too.
const HelpText = `Keys on the snapshot list:
  enter        open the snapshot
  n            make a new snapshot now
  e            make a new encrypted snapshot (includes secrets)
  x            delete the snapshot (asks first)
  /            filter snapshots
  ?            show this help
  q            quit

Keys when browsing a snapshot's files:
  space, x     select or unselect the row
  a            select or unselect everything
  c            select only files that differ from disk
  d            compare the file on disk with the snapshot's copy
  ↑/↓, j/k     move (pgup/pgdown, g/G for first and last)
  ←/→, h/l     fold or unfold a folder
  /            filter paths
  enter        review and restore what's selected
  esc          back to the snapshot list
  ?            show this help
  q            quit

Comparing files (d):
  Works on files marked "modified". The TUI comes back when the tool
  exits. Set diff_tool in the config file to choose the tool:
    diff_tool = 'nvim -d "$LOCAL" "$REMOTE"'
  $LOCAL is the file on disk and $REMOTE is the snapshot's copy. If the
  command uses neither, both are added to the end. Without diff_tool,
  it runs git difftool if git has diff.tool or merge.tool set, otherwise
  git diff --no-index.`

// The help box's border and side padding, and the rows around its body: the
// title and footer, each with a blank line.
const (
	helpBoxFrameW = 6
	helpBoxFrameH = 2
	helpBoxChrome = 4
	// helpBoxMinW fits HelpText's longest line without wrapping. helpBoxMaxW
	// stops the box being mostly empty space on a very wide terminal.
	helpBoxMinW = 78
	helpBoxMaxW = 100
)

// helpText is the help screen's body before wrapping.
func (m model) helpText() string {
	tool := "diff_tool isn't set, so comparing follows your git setup."
	if strings.TrimSpace(m.diffTool) != "" {
		tool = "Your diff_tool: " + m.diffTool
	}
	return HelpText + "\n\n" + tool
}

// helpBoxWidth is the box's outer width: most of the screen up to helpBoxMaxW,
// and wide enough for the text if the terminal is.
func (m model) helpBoxWidth() int {
	return min(m.width, max(helpBoxMinW, min(helpBoxMaxW, m.width*8/10)))
}

// helpLines is the help screen's scrollable body, wrapped to fit the box.
func (m model) helpLines() []string {
	body := m.helpText()
	if m.width > 0 {
		body = lipgloss.NewStyle().Width(max(1, m.helpBoxWidth()-helpBoxFrameW)).Render(body)
	}
	return strings.Split(body, "\n")
}

// helpBoxHeight is the box's outer height: most of the screen, growing to show
// everything without scrolling when there's room.
func (m model) helpBoxHeight() int {
	need := len(m.helpLines()) + helpBoxChrome + helpBoxFrameH
	return min(m.height, max(need, m.height*8/10))
}

// helpHeight is the room for the body inside the box.
func (m model) helpHeight() int {
	return max(1, m.helpBoxHeight()-helpBoxFrameH-helpBoxChrome)
}

// helpMaxOffset is how far the body can scroll. The terminal can shrink after
// scrolling, so readers clamp helpOffset to this rather than trusting it.
func (m model) helpMaxOffset() int {
	if m.height == 0 { // no size reported yet: nothing to fit it to
		return 0
	}
	return max(0, len(m.helpLines())-m.helpHeight())
}

func (m *model) openHelp() {
	m.helpReturn, m.screen, m.helpOffset = m.screen, screenHelp, 0
}

func (m model) updateHelp(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	last := m.helpMaxOffset()
	off := min(m.helpOffset, last)
	switch k.String() {
	case "esc", "q", "?", "enter":
		m.screen = m.helpReturn
	case "up", "k":
		off = max(0, off-1)
	case "down", "j":
		off = min(last, off+1)
	case "pgup":
		off = max(0, off-m.helpHeight())
	case "pgdown":
		off = min(last, off+m.helpHeight())
	case "home", "g":
		off = 0
	case "end", "G":
		off = last
	}
	m.helpOffset = off
	return m, nil
}

func (m model) viewHelp() string {
	lines := m.helpLines()
	last := m.helpMaxOffset()
	off := min(m.helpOffset, last)
	end := len(lines)
	hint := "esc back"
	if last > 0 {
		end = off + m.helpHeight()
		hint = fmt.Sprintf("↑/↓ scroll · lines %d-%d of %d · esc back", off+1, end, len(lines))
		if m.width > 0 && lipgloss.Width(hint) > m.helpBoxWidth()-helpBoxFrameW {
			hint = "↑/↓ scroll · esc back" // a wrapped footer would push the box off screen
		}
	}
	body := lines[off:end]
	// Pad a short body so the footer stays at the bottom of the box.
	for len(body) < m.helpHeight() && m.height > 0 {
		body = append(body[:len(body):len(body)], "")
	}
	content := titleStyle.Render("katbackup help") + "  " + versionStyle.Render("version "+m.version) + "\n\n" +
		strings.Join(body, "\n") + "\n\n" + helpStyle.Render(hint)
	if m.width == 0 || m.height == 0 { // no size reported yet
		return content
	}
	// Width and Height exclude the border, so take off only its 2 cells.
	box := helpBoxStyle.Width(m.helpBoxWidth() - 2).Height(m.helpBoxHeight() - 2).
		MaxWidth(m.width).MaxHeight(m.height).Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
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
