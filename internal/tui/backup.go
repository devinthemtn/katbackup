package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/devinthemtn/katbackup/internal/backup"
	"github.com/devinthemtn/katbackup/internal/snapshot"
)

type backupDoneMsg struct {
	res backup.Result
	err error
}

// backupRun is a backup in progress. Run waits on it after the program exits,
// so quitting mid-backup still cleans up the plaintext staging copy.
type backupRun struct {
	cancel   context.CancelFunc
	done     chan backupDoneMsg
	finished chan struct{} // closed once backup.Run has returned and cleaned up
}

// wait stops the backup and blocks until it has cleaned up.
func (r *backupRun) wait() {
	r.cancel()
	<-r.finished
}

func newPassInputs() [2]textinput.Model {
	var in [2]textinput.Model
	for i, ph := range []string{"new passphrase", "confirm passphrase"} {
		in[i] = textinput.New()
		in[i].EchoMode = textinput.EchoPassword
		in[i].Placeholder = ph
	}
	return in
}

// startNewSnapshot begins a backup, asking for a passphrase first if encrypted.
func (m model) startNewSnapshot(encrypt bool) (tea.Model, tea.Cmd) {
	if m.cfgErr != nil {
		m.err = fmt.Errorf("can't back up: %w", m.cfgErr)
		return m, nil
	}
	if !encrypt {
		return m.runBackup(false, "", true)
	}
	m.newPass = newPassInputs()
	m.newFocus = 0
	m.screen = screenNewPass
	return m, m.newPass[0].Focus()
}

func (m model) updateNewPass(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			m.newPass = newPassInputs() // don't keep a typed passphrase around
			m.screen, m.err = screenSnapshots, nil
			return m, nil
		case "tab", "shift+tab", "up", "down":
			m.newPass[m.newFocus].Blur()
			m.newFocus = 1 - m.newFocus
			return m, m.newPass[m.newFocus].Focus()
		case "enter":
			p, again := m.newPass[0].Value(), m.newPass[1].Value()
			switch {
			case p == "":
				m.err = errors.New("enter a passphrase")
			case m.newFocus == 0:
				m.err = nil
				m.newPass[0].Blur()
				m.newFocus = 1
				return m, m.newPass[1].Focus()
			case again != p:
				m.err = errors.New("passphrases do not match")
				m.newPass[1].SetValue("")
				return m, nil
			default:
				m.newPass = newPassInputs()
				return m.runBackup(true, p, true)
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.newPass[m.newFocus], cmd = m.newPass[m.newFocus].Update(msg)
	return m, cmd
}

func (m model) runBackup(encrypt bool, pass string, skipUnchanged bool) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	run := &backupRun{cancel: cancel, done: make(chan backupDoneMsg, 1), finished: make(chan struct{})}
	opts := backup.Options{
		Ctx: ctx, Home: m.home, Dir: m.backupDir, Config: m.cfg,
		Encrypt: encrypt, Passphrase: pass, SkipUnchanged: skipUnchanged,
	}
	go func() {
		res, err := backup.Run(opts)
		run.done <- backupDoneMsg{res, err}
		close(run.finished)
	}()
	m.backup, m.bkEncrypt, m.bkPass = run, encrypt, pass
	m.bkStopping, m.err = false, nil
	m.screen = screenBackingUp
	return m, func() tea.Msg { return <-run.done }
}

// stopBackup cancels a running backup; backupDoneMsg arrives once it's cleaned up.
func (m *model) stopBackup() {
	if m.backup != nil {
		m.backup.cancel()
		m.bkStopping = true
	}
}

func (m model) backupDone(msg backupDoneMsg) (tea.Model, tea.Cmd) {
	m.backup.cancel() // releases the context
	m.backup = nil
	m.bkRes, m.bkErr = msg.res, msg.err
	if errors.Is(msg.err, context.Canceled) {
		m.bkErr = errors.New("backup stopped; no snapshot was written")
	}
	if m.bkQuit {
		return m, tea.Quit
	}
	m.reloadSnapshots(msg.res.Archive)
	m.screen = screenBackupDone
	return m, nil
}

// reloadSnapshots rereads the snapshot folder, which a backup adds to and may
// prune, and highlights the snapshot at path if it's there.
func (m *model) reloadSnapshots(path string) {
	snaps, err := snapshot.List(m.backupDir)
	if err != nil {
		m.err = err
		return
	}
	items := make([]list.Item, len(snaps))
	sel := 0
	for i, s := range snaps {
		items[i] = snapItem{s}
		if s.Path == path {
			sel = i
		}
	}
	m.snaps.ResetFilter()
	m.snaps.SetItems(items)
	m.snaps.Select(sel)
}

func (m model) updateBackupDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "a":
		if m.bkErr == nil && m.bkRes.UnchangedSince != nil {
			return m.runBackup(m.bkEncrypt, m.bkPass, false)
		}
	case "enter", "esc":
		m.bkPass = ""
		m.screen = screenSnapshots
	}
	return m, nil
}

func (m model) viewNewPass() string {
	v := titleStyle.Render("🔒 New encrypted snapshot") + "\n\n" +
		"Encrypted snapshots also include the paths listed under secrets.\n" +
		"You'll need this passphrase to restore it; it can't be recovered.\n\n" +
		m.newPass[0].View() + "\n" + m.newPass[1].View() + "\n\n"
	if m.err != nil {
		v += errStyle.Render(m.err.Error()) + "\n"
	}
	return v + helpStyle.Render("enter next/start · tab switch field · esc cancel")
}

func (m model) viewBackingUp() string {
	what := "snapshot"
	if m.bkEncrypt {
		what = "encrypted snapshot"
	}
	if m.bkStopping {
		return titleStyle.Render("Stopping and cleaning up…") + "\n"
	}
	return titleStyle.Render("Making a new "+what+"…") + "\n\n" + helpStyle.Render("esc stop")
}

func (m model) viewBackupDone() string {
	var b strings.Builder
	r := m.bkRes
	hint := "enter back to snapshots · q quit"
	switch {
	case m.bkErr != nil:
		b.WriteString(errStyle.Render("Backup failed: "+m.bkErr.Error()) + "\n\n")
	case r.UnchangedSince != nil:
		b.WriteString(titleStyle.Render("No changes since "+r.UnchangedSince.Name+", so nothing was written") + "\n\n")
		hint = "a write a snapshot anyway · " + hint
	default:
		var size int64
		if info, err := os.Stat(r.Archive); err == nil {
			size = info.Size()
		}
		s, _ := snapshot.Parse(r.Archive)
		fmt.Fprintf(&b, "%s\n\n%d entries, %.1f MiB compressed\n",
			titleStyle.Render("Created "+s.Name+" ✓"), len(r.Entries), float64(size)/(1<<20))
		for _, p := range r.Pruned {
			b.WriteString("pruned " + p.Name + " (keep setting)\n")
		}
		b.WriteString("\n")
	}
	for _, n := range r.Notes {
		b.WriteString(helpStyle.Render("note: "+n) + "\n")
	}
	for _, w := range r.Warnings {
		b.WriteString(modStyle.Render("warning: "+w) + "\n")
	}
	return b.String() + "\n" + helpStyle.Render(hint)
}
