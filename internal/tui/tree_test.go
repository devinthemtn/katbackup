package tui

import (
	"testing"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/restore"
)

func TestTreeSelectionAndStatus(t *testing.T) {
	entries := []archive.Entry{
		{Path: ".zshrc", Type: archive.TypeFile},
		{Path: ".config/nvim", Type: archive.TypeDir},
		{Path: ".config/nvim/init.lua", Type: archive.TypeFile},
		{Path: ".config/nvim/lua/a.lua", Type: archive.TypeFile},
	}
	plan := []restore.Action{
		{Entry: entries[0], Kind: restore.Unchanged},
		{Entry: entries[1], Kind: restore.Unchanged},
		{Entry: entries[2], Kind: restore.Overwrite},
		{Entry: entries[3], Kind: restore.Create},
	}
	root := buildTree(entries, plan)
	rows := flatten(root, "")
	// .config (expanded) → nvim (collapsed), then .zshrc
	if len(rows) != 3 || rows[0].path != ".config" || rows[1].path != ".config/nvim" || rows[2].path != ".zshrc" {
		t.Fatalf("rows: %v", names(rows))
	}
	if rows[1].status != restore.Overwrite {
		t.Errorf("dir status = %s, want overwrite", rows[1].status)
	}
	rows[1].setSelected(true)
	if got := selectedPaths(root); len(got) != 3 || got[".zshrc"] {
		t.Errorf("selected: %v", got)
	}
	if f := flatten(root, "a.lua"); len(f) != 4 {
		t.Errorf("filtered rows: %v", names(f))
	}
}

func names(ns []*node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.path
	}
	return out
}
