package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/restore"
)

// defaultDiffTool follows the user's git setup. --no-index compares the two
// paths as plain files, whether or not the working directory is a repository.
//
// `git difftool` only runs if a tool is configured, because its guess can't be
// relied on: it takes the bc calculator for Beyond Compare, then fails. The
// fallback sets LESS because git's default adds F, which would quit on a short
// diff and put the TUI straight back over it.
const defaultDiffTool = `if git config --get diff.tool >/dev/null || git config --get merge.tool >/dev/null; then ` +
	`git difftool --no-index --no-prompt -- "$LOCAL" "$REMOTE"; ` +
	`else LESS="${LESS:-R}" git diff --no-index -- "$LOCAL" "$REMOTE"; fi`

type diffReadyMsg struct {
	node          *node
	dir           string
	local, remote string
	err           error
}

type diffDoneMsg struct {
	node *node
	dir  string
	err  error
}

// newDiffRoot makes the private folder that holds extracted snapshot files for
// the session. It prefers $XDG_RUNTIME_DIR, usually a tmpfs, because a file from
// an encrypted snapshot sits here unencrypted while it is being compared.
func newDiffRoot() (string, error) {
	return os.MkdirTemp(os.Getenv("XDG_RUNTIME_DIR"), "katbackup-diff-*")
}

// diffCommand builds the shell command that compares local (the file on disk)
// with remote (the snapshot's copy). The paths are only ever passed as
// environment variables, so a snapshot's filenames can't inject shell commands.
func diffCommand(tool, local, remote string) *exec.Cmd {
	if strings.TrimSpace(tool) == "" {
		tool = defaultDiffTool
	}
	if !mentionsDiffVars(tool) {
		tool += ` "$LOCAL" "$REMOTE"`
	}
	cmd := exec.Command("sh", "-c", tool)
	cmd.Env = append(os.Environ(), "LOCAL="+local, "REMOTE="+remote)
	return cmd
}

func mentionsDiffVars(tool string) bool {
	for _, v := range []string{"$LOCAL", "${LOCAL", "$REMOTE", "${REMOTE"} {
		if strings.Contains(tool, v) {
			return true
		}
	}
	return false
}

// diffToolError interprets how the tool exited. Exit status 1 isn't a failure:
// diff tools commonly use it to say the files differ.
func diffToolError(err error) error {
	var exit *exec.ExitError
	if err == nil || (errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil
	}
	return fmt.Errorf("diff tool failed (%w); check diff_tool in your config", err)
}

// startDiff compares the row under the cursor, or explains why it can't.
func (m model) startDiff(n *node) (tea.Model, tea.Cmd) {
	if n == nil {
		return m, nil
	}
	if reason := m.cantDiff(n); reason != "" {
		m.err = errors.New(reason)
		return m, nil
	}
	m.err = nil
	m.diffing = n.path
	m.screen = screenExtracting
	return m, m.extractForDiff(n)
}

// cantDiff returns why n can't be compared, or "" if it can.
func (m model) cantDiff(n *node) string {
	if n.isDir() || n.entry == nil {
		return "pick a file to compare"
	}
	switch n.status {
	case restore.Create:
		return n.name + " isn't on disk, so there's nothing to compare"
	case restore.Unchanged:
		return n.name + " is identical to the snapshot"
	}
	local := filepath.Join(m.home, filepath.FromSlash(n.path))
	if n.entry.Type == archive.TypeSymlink {
		if link, err := os.Readlink(local); err == nil {
			return fmt.Sprintf("%s is a symlink: %s on disk, %s in the snapshot", n.name, link, n.entry.LinkTarget)
		}
		return n.name + " is a symlink in the snapshot but not on disk, so it can't be compared as a file"
	}
	if info, err := os.Lstat(local); err != nil || !info.Mode().IsRegular() {
		return n.name + " isn't a regular file on disk, so it can't be compared"
	}
	return ""
}

func (m model) extractForDiff(n *node) tea.Cmd {
	snapPath, pass, root := m.snap.Path, m.password, m.diffRoot
	local := filepath.Join(m.home, filepath.FromSlash(n.path))
	entry := *n.entry
	return func() tea.Msg {
		if root == "" {
			return diffReadyMsg{node: n, err: errors.New("no temporary folder for comparing files")}
		}
		// A folder per comparison lets the copy keep its name, so tools can
		// still highlight it by extension.
		dir, err := os.MkdirTemp(root, "file-*")
		if err != nil {
			return diffReadyMsg{node: n, err: err}
		}
		remote := filepath.Join(dir, filepath.Base(local))
		if err := restore.ExtractFile(snapPath, pass, entry, remote); err != nil {
			os.RemoveAll(dir)
			return diffReadyMsg{node: n, err: err}
		}
		return diffReadyMsg{node: n, dir: dir, local: local, remote: remote}
	}
}

// diffReady hands the terminal to the tool once the snapshot's copy is on disk.
func (m model) diffReady(msg diffReadyMsg) (tea.Model, tea.Cmd) {
	m.screen = screenTree
	m.diffing = ""
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	n, dir := msg.node, msg.dir
	return m, tea.ExecProcess(diffCommand(m.diffTool, msg.local, msg.remote), func(err error) tea.Msg {
		return diffDoneMsg{node: n, dir: dir, err: err}
	})
}

// diffDone runs when the tool exits and the TUI has the terminal back.
func (m model) diffDone(msg diffDoneMsg) (tea.Model, tea.Cmd) {
	os.RemoveAll(msg.dir)
	m.err = diffToolError(msg.err)
	m.refreshStatus(msg.node)
	return m, nil
}

// refreshStatus re-checks one file against the disk, since a diff tool can edit
// the current version, then updates the folders above it.
func (m *model) refreshStatus(n *node) {
	plan := restore.Plan(m.idx, func(e archive.Entry) bool { return e.Path == n.path }, m.home)
	if len(plan) != 1 {
		return
	}
	n.status = plan[0].Kind
	for p := n.parent; p != nil; p = p.parent {
		p.status = restore.Unchanged
		for _, c := range p.children {
			p.status = worse(p.status, c.status)
		}
	}
}
