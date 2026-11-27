package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// treeNode is a directory or file in the changed-files tree.
type treeNode struct {
	name     string // display name; directory chains with a single child are joined
	path     string
	file     *FileDiff // nil for directories
	children []*treeNode
	files    []*FileDiff // every file below this node, in tree order
	add, del int
}

type treeRow struct {
	node  *treeNode
	depth int
}

// buildTree groups files into a lazygit-style tree. The root covers every file:
// it is the common directory when there is one, otherwise "/".
func buildTree(files []*FileDiff) *treeNode {
	root := &treeNode{name: "/"}
	for _, f := range files {
		parts := strings.Split(f.Path, "/")
		n := root
		for i, p := range parts[:len(parts)-1] {
			var next *treeNode
			for _, c := range n.children {
				if c.file == nil && c.name == p {
					next = c
					break
				}
			}
			if next == nil {
				next = &treeNode{name: p, path: strings.Join(parts[:i+1], "/")}
				n.children = append(n.children, next)
			}
			n = next
		}
		n.children = append(n.children, &treeNode{name: parts[len(parts)-1], path: f.Path, file: f})
	}
	for _, c := range root.children {
		compress(c)
	}
	if len(root.children) == 1 && root.children[0].file == nil {
		root = root.children[0]
	}
	finish(root)
	return root
}

func compress(n *treeNode) {
	for n.file == nil && len(n.children) == 1 && n.children[0].file == nil {
		c := n.children[0]
		n.name, n.path, n.children = n.name+"/"+c.name, c.path, c.children
	}
	for _, c := range n.children {
		compress(c)
	}
}

// finish sorts directories before files and totals each directory's changes.
func finish(n *treeNode) {
	if n.file != nil {
		n.files, n.add, n.del = []*FileDiff{n.file}, n.file.Add, n.file.Del
		return
	}
	sort.SliceStable(n.children, func(i, j int) bool {
		a, b := n.children[i], n.children[j]
		if (a.file == nil) != (b.file == nil) {
			return a.file == nil
		}
		return strings.ToLower(a.name) < strings.ToLower(b.name)
	})
	n.files, n.add, n.del = nil, 0, 0
	for _, c := range n.children {
		finish(c)
		n.files = append(n.files, c.files...)
		n.add += c.add
		n.del += c.del
	}
}

// rows flattens the tree into visible rows, skipping collapsed directories' contents.
func (n *treeNode) rows(collapsed func(path string) bool) []treeRow {
	var out []treeRow
	var walk func(*treeNode, int)
	walk = func(n *treeNode, depth int) {
		out = append(out, treeRow{n, depth})
		if n.file == nil && !collapsed(n.path) {
			for _, c := range n.children {
				walk(c, depth+1)
			}
		}
	}
	walk(n, 0)
	return out
}

var statusStyles = map[byte]lipgloss.Style{
	'A': addStyle,
	'D': delStyle,
	'M': lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	'R': hunkStyle,
}

func (r treeRow) render(width int, selected, collapsed bool) string {
	n := r.node
	marker := "  "
	if selected {
		marker = selStyle.Render("› ")
	}
	icon := "▼ "
	if n.file != nil {
		icon = statusStyles[n.file.Status].Render(string(n.file.Status)) + " "
	} else if collapsed {
		icon = "▶ "
	}
	left := marker + strings.Repeat("  ", r.depth) + icon
	stats := addStyle.Render(fmt.Sprintf("+%d", n.add)) + " " + delStyle.Render(fmt.Sprintf("-%d", n.del))
	nameW := width - ansi.StringWidth(left) - ansi.StringWidth(stats) - 1
	if nameW < 6 {
		stats, nameW = "", width-ansi.StringWidth(left)
	}
	name := ansi.Truncate(n.name, max(nameW, 1), "…")
	if selected {
		name = selStyle.Render(name)
	}
	gap := max(width-ansi.StringWidth(left)-ansi.StringWidth(name)-ansi.StringWidth(stats), 1)
	return left + name + strings.Repeat(" ", gap) + stats
}

// diffDoc is what the diff pane scrolls through: one file, or every file under a
// directory one after another, each preceded by a file header row.
type diffDoc struct {
	files   []*FileDiff
	headers bool
	split   bool
	starts  []int // first row of each file (its header row when headers is set)
	total   int
}

func newDoc(files []*FileDiff, headers, split bool) *diffDoc {
	d := &diffDoc{files: files, headers: headers, split: split}
	for _, f := range files {
		d.starts = append(d.starts, d.total)
		if headers {
			d.total++
		}
		d.total += d.fileRows(f)
	}
	return d
}

func (d *diffDoc) fileRows(f *FileDiff) int {
	if d.split {
		return len(f.Split().rows)
	}
	return len(f.Body())
}

// locate maps a document row to a file and a row within it (-1 = the file header).
func (d *diffDoc) locate(r int) (fi, row int) {
	fi = max(sort.Search(len(d.starts), func(i int) bool { return d.starts[i] > r })-1, 0)
	row = r - d.starts[fi]
	if d.headers {
		row--
	}
	return fi, row
}

func (d *diffDoc) docRow(fi, row int) int {
	if d.headers {
		row++
	}
	return d.starts[fi] + row
}

// hunks returns the rows n/N jump between: file headers and hunk headers.
func (d *diffDoc) hunks() []int {
	var out []int
	for fi, f := range d.files {
		if d.headers {
			out = append(out, d.starts[fi])
		}
		hs := f.hunkStarts
		if d.split {
			hs = f.Split().hunkStarts
		}
		for _, h := range hs {
			out = append(out, d.docRow(fi, h))
		}
	}
	return out
}

func fileHeaderLines(f *FileDiff, first bool, width int) []string {
	label := f.Path
	switch f.Status {
	case 'A':
		label = "added: " + label
	case 'D':
		label = "deleted: " + label
	case 'R':
		label = "renamed: " + label
	}
	lines := []string{
		hunkStyle.Bold(true).Render(ansi.Truncate(label, width, "…")),
		hunkStyle.Render(strings.Repeat("─", max(width, 0))),
	}
	if !first {
		lines = append([]string{""}, lines...)
	}
	return lines
}
