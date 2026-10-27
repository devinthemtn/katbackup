package tui

import (
	"sort"
	"strings"

	"github.com/devinthemtn/katbackup/internal/archive"
	"github.com/devinthemtn/katbackup/internal/restore"
)

type node struct {
	name     string
	path     string
	entry    *archive.Entry
	children []*node
	parent   *node
	depth    int
	expanded bool
	selected bool
	status   restore.Kind
}

func (n *node) isDir() bool {
	return len(n.children) > 0 || (n.entry != nil && n.entry.Type == archive.TypeDir)
}

// buildTree arranges index entries into a tree and annotates each node with
// its restore status (dirs take the "worst" status of their contents).
func buildTree(entries []archive.Entry, plan []restore.Action) *node {
	status := map[string]restore.Kind{}
	for _, a := range plan {
		status[a.Entry.Path] = a.Kind
	}
	root := &node{expanded: true, depth: -1}
	index := map[string]*node{"": root}
	var get func(p string) *node
	get = func(p string) *node {
		if n, ok := index[p]; ok {
			return n
		}
		parentPath, name := "", p
		if i := strings.LastIndex(p, "/"); i >= 0 {
			parentPath, name = p[:i], p[i+1:]
		}
		parent := get(parentPath)
		n := &node{name: name, path: p, parent: parent, depth: parent.depth + 1}
		parent.children = append(parent.children, n)
		index[p] = n
		return n
	}
	for i := range entries {
		e := &entries[i]
		n := get(e.Path)
		n.entry = e
		n.status = status[e.Path]
	}
	var finish func(n *node)
	finish = func(n *node) {
		sort.Slice(n.children, func(i, j int) bool {
			a, b := n.children[i], n.children[j]
			if a.isDir() != b.isDir() {
				return a.isDir()
			}
			return a.name < b.name
		})
		for _, c := range n.children {
			finish(c)
			n.status = worse(n.status, c.status)
		}
		if n.status == "" {
			n.status = restore.Unchanged
		}
	}
	finish(root)
	// Top-level folders like .config start open so there's something to see.
	for _, c := range root.children {
		if c.name == ".config" {
			c.expanded = true
		}
	}
	return root
}

func worse(a, b restore.Kind) restore.Kind {
	rank := map[restore.Kind]int{"": 0, restore.Unchanged: 1, restore.Create: 2, restore.Overwrite: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func (n *node) walk(fn func(*node)) {
	fn(n)
	for _, c := range n.children {
		c.walk(fn)
	}
}

// selection returns (selected, total) over nodes in the subtree that have entries.
func (n *node) selection() (int, int) {
	sel, total := 0, 0
	n.walk(func(x *node) {
		if x.entry != nil {
			total++
			if x.selected {
				sel++
			}
		}
	})
	return sel, total
}

func (n *node) setSelected(v bool) {
	n.walk(func(x *node) { x.selected = v })
}

// flatten returns the visible rows. With a filter, only matching nodes and
// their ancestors are shown, fully expanded.
func flatten(root *node, filter string) []*node {
	filter = strings.ToLower(filter)
	var out []*node
	var visit func(n *node) bool
	visit = func(n *node) bool {
		if filter == "" {
			out = append(out, n)
			if n.expanded {
				for _, c := range n.children {
					visit(c)
				}
			}
			return true
		}
		mark := len(out)
		out = append(out, n)
		match := strings.Contains(strings.ToLower(n.path), filter)
		any := false
		for _, c := range n.children {
			if visit(c) {
				any = true
			}
		}
		if !match && !any {
			out = out[:mark]
			return false
		}
		return true
	}
	for _, c := range root.children {
		visit(c)
	}
	return out
}

func selectedPaths(root *node) map[string]bool {
	out := map[string]bool{}
	root.walk(func(n *node) {
		if n.selected && n.entry != nil {
			out[n.path] = true
		}
	})
	return out
}
