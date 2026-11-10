package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/restore"
	"github.com/devinthemtn/katbackup/internal/snapshot"
)

func TestDiffCommandPassesPathsSafely(t *testing.T) {
	local := filepath.Join(t.TempDir(), `a b; touch pwned $(id) "q".txt`)
	const remote = "/tmp/remote copy"
	want := local + "\n" + remote + "\n"
	for name, tool := range map[string]string{
		"appended":    `printf '%s\n'`,
		"placeholder": `printf '%s\n' "$LOCAL" "$REMOTE"`,
		"braces":      `printf '%s\n' "${LOCAL}" "${REMOTE}"`,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := diffCommand(tool, local, remote).Output()
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != want {
				t.Errorf("got %q, want %q", out, want)
			}
		})
	}
}

func TestDiffCommandDefaultsToGit(t *testing.T) {
	for _, tool := range []string{"", "   "} {
		cmd := diffCommand(tool, "/l", "/r")
		if got := cmd.Args[len(cmd.Args)-1]; got != defaultDiffTool {
			t.Errorf("tool %q ran %q", tool, got)
		}
	}
}

func TestDefaultDiffToolFollowsGitConfig(t *testing.T) {
	dir := t.TempDir()
	local, remote := filepath.Join(dir, "cur rent"), filepath.Join(dir, "snap shot")
	write(t, local, "current\n")
	write(t, remote, "snapshot\n")
	gitconfig := filepath.Join(dir, "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	for _, tc := range []struct{ name, config, want string }{
		{"no tool configured falls back to git diff", "", "+snapshot"},
		{"diff.tool", "[diff]\n\ttool = rec\n[difftool \"rec\"]\n\tcmd = echo rec-ran\n", "rec-ran"},
		{"merge.tool", "[merge]\n\ttool = rec\n[mergetool \"rec\"]\n\tcmd = echo rec-ran\n[difftool \"rec\"]\n\tcmd = echo rec-ran\n", "rec-ran"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(t, gitconfig, tc.config)
			out, err := diffCommand("", local, remote).CombinedOutput()
			if diffToolError(err) != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("output doesn't contain %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestDiffToolError(t *testing.T) {
	for _, tc := range []struct {
		tool    string
		wantErr bool
	}{
		{"exit 0", false},
		{"exit 1", false},
		{"exit 2", true},
		{"katbackup-no-such-tool", true},
	} {
		err := diffToolError(exec.Command("sh", "-c", tc.tool).Run())
		if (err != nil) != tc.wantErr {
			t.Errorf("%q: err = %v", tc.tool, err)
		}
		if err != nil && !strings.Contains(err.Error(), "diff_tool") {
			t.Errorf("%q: error doesn't name the setting: %v", tc.tool, err)
		}
	}
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// diffModel builds a real snapshot and a home folder with one of each kind of
// row, and returns a model on the tree screen with every folder expanded.
func diffModel(t *testing.T) model {
	t.Helper()
	const old = "snapshot version\n"
	file := func(p string) archive.Entry {
		return archive.Entry{
			Path: p, Type: archive.TypeFile, Mode: 0o644, Size: int64(len(old)),
			ModTime: time.Now(), SHA256: sum(old),
		}
	}
	entries := []archive.Entry{
		{Path: "conf", Type: archive.TypeDir, Mode: 0o755, ModTime: time.Now()},
		file("conf/changed file.toml"), file("conf/same"), file("conf/new"), file("conf/swapped"),
		{Path: "conf/link", Type: archive.TypeSymlink, Mode: 0o777, ModTime: time.Now(), LinkTarget: "old-target"},
	}
	src := t.TempDir()
	for _, e := range entries {
		if e.Type == archive.TypeFile {
			write(t, filepath.Join(src, e.Path), old)
		}
	}
	snap := filepath.Join(t.TempDir(), "2026-01-01_000000.tar.zst")
	f, err := os.Create(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Write(f, src, archive.Index{Entries: entries}, ""); err != nil {
		t.Fatal(err)
	}
	f.Close()
	idx, err := archive.ReadIndex(snap, "")
	if err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	write(t, filepath.Join(home, "conf/changed file.toml"), "current version\n")
	write(t, filepath.Join(home, "conf/same"), old)
	if err := os.MkdirAll(filepath.Join(home, "conf/swapped"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("new-target", filepath.Join(home, "conf/link")); err != nil {
		t.Fatal(err)
	}

	m := model{
		screen: screenTree, height: 40, home: home, idx: idx, filter: textinput.New(),
		snap:     snapshot.Snapshot{Name: "snap", Path: snap},
		diffRoot: t.TempDir(),
		root:     buildTree(idx.Entries, restore.Plan(idx, nil, home)),
	}
	m.root.walk(func(n *node) { n.expanded = true })
	m.refresh()
	return m
}

func (m model) rowFor(t *testing.T, name string) *node {
	t.Helper()
	for _, n := range m.rows {
		if n.name == name {
			return n
		}
	}
	t.Fatalf("no row %q", name)
	return nil
}

func (m model) pressD(t *testing.T, name string) (model, func() any) {
	t.Helper()
	for i, n := range m.rows {
		if n.name == name {
			m.cursor = i
		}
	}
	next, cmd := m.Update(key("d"))
	if cmd == nil {
		return next.(model), nil
	}
	return next.(model), func() any { return cmd() }
}

func TestDiffRefusesWhatItCantCompare(t *testing.T) {
	for name, want := range map[string]string{
		"conf":    "pick a file",
		"new":     "isn't on disk",
		"same":    "identical",
		"swapped": "isn't a regular file",
		"link":    "new-target on disk, old-target in the snapshot",
	} {
		t.Run(name, func(t *testing.T) {
			m, cmd := diffModel(t).pressD(t, name)
			if cmd != nil || m.screen != screenTree {
				t.Fatalf("screen = %v, started a comparison: %v", m.screen, cmd != nil)
			}
			if m.err == nil || !strings.Contains(m.err.Error(), want) {
				t.Errorf("err = %v, want it to mention %q", m.err, want)
			}
			if next, _ := m.Update(key("j")); next.(model).err != nil {
				t.Error("the message should clear on the next key")
			}
		})
	}
}

func TestDiffFlow(t *testing.T) {
	m, cmd := diffModel(t).pressD(t, "changed file.toml")
	if cmd == nil || m.screen != screenExtracting {
		t.Fatalf("screen = %v, cmd = %v", m.screen, cmd != nil)
	}
	if !strings.Contains(m.View(), "changed file.toml") {
		t.Errorf("view: %q", m.View())
	}
	// Keys do nothing until the copy is ready.
	if next, c := m.Update(key("q")); c != nil || next.(model).screen != screenExtracting {
		t.Error("key handled while extracting")
	}

	ready, ok := cmd().(diffReadyMsg)
	if !ok || ready.err != nil {
		t.Fatalf("ready = %+v", ready)
	}
	if ready.local != filepath.Join(m.home, "conf/changed file.toml") {
		t.Errorf("local = %q", ready.local)
	}
	if filepath.Base(ready.remote) != "changed file.toml" {
		t.Errorf("remote should keep the file's name: %q", ready.remote)
	}
	if got, _ := os.ReadFile(ready.remote); string(got) != "snapshot version\n" {
		t.Errorf("remote content = %q", got)
	}
	if info, err := os.Stat(ready.remote); err != nil || info.Mode().Perm() != 0o400 {
		t.Errorf("remote mode: %v, %v", info, err)
	}

	next, exec := m.Update(ready)
	m = next.(model)
	if m.screen != screenTree || exec == nil {
		t.Fatalf("screen = %v, exec = %v", m.screen, exec != nil)
	}

	next, _ = m.Update(diffDoneMsg{node: ready.node, dir: ready.dir})
	m = next.(model)
	if m.err != nil {
		t.Errorf("err = %v", m.err)
	}
	if _, err := os.Lstat(ready.dir); !os.IsNotExist(err) {
		t.Errorf("temp copy left behind: %v", err)
	}
}

func TestDiffExtractFailureReturnsToTree(t *testing.T) {
	base := diffModel(t)
	base.snap.Path = filepath.Join(t.TempDir(), "gone.tar.zst")
	m, cmd := base.pressD(t, "changed file.toml")
	next, _ := m.Update(cmd().(diffReadyMsg))
	m = next.(model)
	if m.screen != screenTree || m.err == nil {
		t.Errorf("screen = %v, err = %v", m.screen, m.err)
	}
	if left, _ := os.ReadDir(m.diffRoot); len(left) != 0 {
		t.Errorf("left in temp folder: %v", left)
	}
}

// A diff tool can edit the current file, which changes what a restore would do.
func TestDiffRefreshesStatusAfterEdit(t *testing.T) {
	m := diffModel(t)
	n := m.rowFor(t, "changed file.toml")
	for _, row := range []string{"new", "swapped", "link"} {
		m.rowFor(t, row).status = restore.Unchanged // leave only this file to change the folder's status
	}
	conf := n.parent
	if conf.status != restore.Overwrite {
		t.Fatalf("folder status = %v", conf.status)
	}

	write(t, filepath.Join(m.home, "conf/changed file.toml"), "snapshot version\n")
	next, _ := m.Update(diffDoneMsg{node: n, dir: t.TempDir()})
	m = next.(model)
	if n.status != restore.Unchanged {
		t.Errorf("file status = %v", n.status)
	}
	if conf.status != restore.Unchanged {
		t.Errorf("folder status = %v", conf.status)
	}
}

func TestHelpScreen(t *testing.T) {
	m := diffModel(t)
	if !strings.Contains(m.View(), "? help") {
		t.Error("the tree's hint line doesn't mention help")
	}
	next, _ := m.Update(key("?"))
	help := next.(model)
	if help.screen != screenHelp {
		t.Fatalf("screen = %v", help.screen)
	}
	help.version = "1.2.3"
	for _, want := range []string{"version 1.2.3", "compare the file on disk", "diff_tool", "$REMOTE", "git difftool", "isn't set"} {
		if !strings.Contains(help.View(), want) {
			t.Errorf("help doesn't mention %q", want)
		}
	}
	help.diffTool = "nvim -d"
	if !strings.Contains(help.View(), "Your diff_tool: nvim -d") {
		t.Error("help doesn't show the configured tool")
	}
	// q closes the help rather than quitting from behind it.
	next, cmd := help.Update(key("q"))
	if next.(model).screen != screenTree || cmd != nil {
		t.Errorf("screen = %v, cmd = %v", next.(model).screen, cmd != nil)
	}
}

func TestHelpScrolls(t *testing.T) {
	var m tea.Model = model{screen: screenHelp, helpReturn: screenTree, width: 80, height: 10, version: "1.2.3",
		filter: textinput.New(), root: buildTree(nil, nil)}
	press := func(k string) string {
		t.Helper()
		m, _ = m.Update(key(k))
		if got := m.(model).screen; got != screenHelp {
			t.Fatalf("%q left the help screen: %v", k, got)
		}
		return m.View()
	}
	offset := func() int { return m.(model).helpOffset }
	resize := func(w, h int) {
		next := m.(model)
		next.width, next.height = w, h
		m = next
	}

	top := m.View()
	if !strings.Contains(top, "Keys on the snapshot list") || strings.Contains(top, "Your diff_tool") || strings.Contains(top, "isn't set") {
		t.Errorf("should start at the top:\n%s", top)
	}
	if strings.Count(top, "\n")+1 > 10 {
		t.Errorf("view is taller than the terminal:\n%s", top)
	}

	press("k")
	if offset() != 0 {
		t.Errorf("scrolled above the top: %d", offset())
	}
	v := press("j")
	if offset() != 1 || strings.Contains(v, "Keys on the snapshot list") {
		t.Errorf("offset = %d after one line down:\n%s", offset(), v)
	}
	if !strings.Contains(v, "version 1.2.3") || !strings.Contains(v, "scroll") {
		t.Errorf("title or footer scrolled away:\n%s", v)
	}

	bottom := press("G")
	if !strings.Contains(bottom, "isn't set") {
		t.Errorf("end doesn't reach the last line:\n%s", bottom)
	}
	end := offset()
	press("j")
	if offset() != end {
		t.Errorf("scrolled past the end: %d > %d", offset(), end)
	}

	// Growing the terminal after scrolling must not leave a stale offset.
	resize(80, 100)
	big := m.View()
	if !strings.Contains(big, "Keys on the snapshot list") || !strings.Contains(big, "isn't set") || strings.Contains(big, "scroll") {
		t.Errorf("everything should fit, with no scroll hint:\n%s", big)
	}
	press("k")
	if offset() != 0 {
		t.Errorf("offset = %d", offset())
	}

	// Long lines wrap, so a narrow terminal loses nothing.
	resize(30, 100)
	if narrow := m.View(); !strings.Contains(narrow, "--no-index") {
		t.Errorf("narrow view lost text:\n%s", narrow)
	}

	// Reopening starts from the top again.
	resize(80, 10)
	press("G")
	m, _ = m.Update(key("q"))
	tree := m.(model)
	tree.filter, tree.root = textinput.New(), buildTree(nil, nil)
	next, _ := tree.Update(key("?"))
	if next.(model).helpOffset != 0 {
		t.Errorf("reopened at offset %d", next.(model).helpOffset)
	}
}

func TestHelpBoxIsCentered(t *testing.T) {
	for _, tc := range []struct{ w, h int }{{200, 60}, {120, 40}, {80, 24}, {60, 14}, {40, 10}} {
		l := list.New([]list.Item{testItem("snap")}, list.NewDefaultDelegate(), tc.w, tc.h)
		m := model{screen: screenHelp, snaps: l, width: tc.w, height: tc.h, version: "1.2.3"}
		lines := strings.Split(m.View(), "\n")
		if len(lines) != tc.h {
			t.Errorf("%dx%d: %d rows", tc.w, tc.h, len(lines))
		}
		top, bottom := -1, -1
		for i, l := range lines {
			if lipgloss.Width(l) != tc.w {
				t.Errorf("%dx%d: row %d is %d wide", tc.w, tc.h, i, lipgloss.Width(l))
			}
			if strings.Contains(l, "╭") {
				top = i
			}
			if strings.Contains(l, "╰") {
				bottom = i
			}
		}
		if top < 0 || bottom < 0 {
			t.Fatalf("%dx%d: no complete box:\n%s", tc.w, tc.h, m.View())
		}
		if above, below := top, tc.h-1-bottom; above-below > 1 || below-above > 1 {
			t.Errorf("%dx%d: not centered vertically: %d above, %d below", tc.w, tc.h, above, below)
		}
		row := lines[top]
		left := lipgloss.Width(row[:strings.Index(row, "╭")])
		right := tc.w - lipgloss.Width(row[:strings.Index(row, "╮")+len("╮")])
		if left-right > 1 || right-left > 1 {
			t.Errorf("%dx%d: not centered horizontally: %d left, %d right", tc.w, tc.h, left, right)
		}
		// Sized to the text, not the terminal, so the screen behind shows around it.
		if boxW := tc.w - left - right; boxW != min(tc.w, helpBoxMinW) {
			t.Errorf("%dx%d: box is %d wide", tc.w, tc.h, boxW)
		}
		need := len(m.helpLines()) + helpBoxChrome + helpBoxFrameH
		if boxH := bottom - top + 1; boxH != min(m.helpRoom(), need) {
			t.Errorf("%dx%d: box is %d tall, want %d", tc.w, tc.h, boxH, min(m.helpRoom(), need))
		}
		if !strings.Contains(m.View(), "version 1.2.3") || !strings.Contains(m.View(), "esc back") {
			t.Errorf("%dx%d: title or footer missing:\n%s", tc.w, tc.h, m.View())
		}
		if tc.h > need && !strings.Contains(m.View(), "katbackup · choose") && !strings.Contains(m.View(), "List") {
			t.Errorf("%dx%d: the snapshot list doesn't show behind the box:\n%s", tc.w, tc.h, m.View())
		}
	}
}

func TestHelpFitsWithoutWrappingAt80Columns(t *testing.T) {
	for _, l := range strings.Split(HelpText, "\n") {
		if w := lipgloss.Width(l); w > helpBoxMinW-helpBoxFrameW {
			t.Errorf("%d wide: %q", w, l)
		}
	}
}

type testItem string

func (i testItem) FilterValue() string { return string(i) }
func (i testItem) Title() string       { return string(i) }
func (i testItem) Description() string { return "" }

// The snapshot list is the first screen, so its ? must open the same help, and
// closing it must go back there rather than to a tree that isn't loaded.
func TestHelpFromSnapshotList(t *testing.T) {
	l := list.New([]list.Item{testItem("snap")}, list.NewDefaultDelegate(), 80, 20)
	var m tea.Model = model{screen: screenSnapshots, snaps: l, width: 80, height: 24, version: "1.2.3"}

	m, _ = m.Update(key("?"))
	if m.(model).screen != screenHelp || !strings.Contains(m.View(), "version 1.2.3") {
		t.Fatalf("screen = %v:\n%s", m.(model).screen, m.View())
	}
	for _, k := range []string{"q", "?"} {
		next, cmd := m.Update(key(k))
		if next.(model).screen != screenSnapshots || cmd != nil {
			t.Errorf("%q: screen = %v, cmd = %v", k, next.(model).screen, cmd != nil)
		}
	}

	// While filtering the list, ? is just a character to type.
	m, _ = m.Update(key("q"))
	m, _ = m.Update(key("/"))
	m, _ = m.Update(key("?"))
	if m.(model).screen != screenSnapshots {
		t.Errorf("? while filtering opened screen %v", m.(model).screen)
	}
	if got := m.(model).snaps.FilterInput.Value(); got != "?" {
		t.Errorf("filter = %q", got)
	}
}
