package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// lineNum holds a unified diff line's old and new line numbers (0 = not on that side).
type lineNum struct{ old, new int }

// LineNums numbers every body line of the unified diff.
func (f *FileDiff) LineNums() []lineNum {
	if f.nums != nil {
		return f.nums
	}
	body := f.Body()
	f.nums = make([]lineNum, len(body))
	ln, rn := 0, 0
	for i, l := range body {
		switch {
		case f.firstHunk < 0, strings.HasPrefix(l, `\`):
		case strings.HasPrefix(l, "@@"):
			if m := hunkRe.FindStringSubmatch(l); m != nil {
				fmt.Sscan(m[1], &ln)
				fmt.Sscan(m[2], &rn)
			}
		case lineKind(l) == '-':
			f.nums[i] = lineNum{old: ln}
			ln++
		case lineKind(l) == '+':
			f.nums[i] = lineNum{new: rn}
			rn++
		default:
			f.nums[i] = lineNum{ln, rn}
			ln++
			rn++
		}
	}
	return f.nums
}

// anchor is a line a comment can attach to, on GitHub's LEFT (old) or RIGHT (new) side.
type anchor struct {
	side string
	line int
}

// rowAnchors returns the file and the commentable lines of document row r; none for
// file and hunk headers.
func (m *model) rowAnchors(d *diffDoc, r int) (fi int, out []anchor) {
	if r < 0 || r >= d.total {
		return 0, nil
	}
	fi, row := d.locate(r)
	if row < 0 {
		return fi, nil
	}
	f := d.files[fi]
	if m.split {
		sr := f.Split().rows[row]
		if sr.header != "" {
			return fi, nil
		}
		if sr.left.kind != 0 {
			out = append(out, anchor{"LEFT", sr.left.num})
		}
		if sr.right.kind != 0 {
			out = append(out, anchor{"RIGHT", sr.right.num})
		}
		return fi, out
	}
	n := f.LineNums()[row]
	if n.old > 0 {
		out = append(out, anchor{"LEFT", n.old})
	}
	if n.new > 0 {
		out = append(out, anchor{"RIGHT", n.new})
	}
	return fi, out
}

func (m *model) commentable(d *diffDoc, r int) bool {
	_, a := m.rowAnchors(d, r)
	return len(a) > 0
}

// snapCursor moves the cursor to the nearest commentable row, preferring direction dir.
func (m *model) snapCursor(d *diffDoc, dir int) {
	m.cursor = max(min(m.cursor, d.total-1), 0)
	if dir == 0 {
		dir = 1
	}
	for _, step := range []int{dir, -dir} {
		for r := m.cursor; r >= 0 && r < d.total; r += step {
			if m.commentable(d, r) {
				m.cursor = r
				return
			}
		}
	}
}

// stepCursor moves to the next commentable row in direction dir, if there is one.
func (m *model) stepCursor(d *diffDoc, dir int) {
	for r := m.cursor + dir; r >= 0 && r < d.total; r += dir {
		if m.commentable(d, r) {
			m.cursor = r
			return
		}
	}
}

// hunkBounds is the run of commentable rows around r: one hunk, which is as far as
// a GitHub multi-line comment may reach.
func (m *model) hunkBounds(d *diffDoc, r int) (lo, hi int) {
	lo, hi = r, r
	for lo > 0 && m.commentable(d, lo-1) {
		lo--
	}
	for hi < d.total-1 && m.commentable(d, hi+1) {
		hi++
	}
	return lo, hi
}

// followCursor scrolls just enough to keep a couple of rows around the cursor visible.
func (m *model) followCursor(d *diffDoc) {
	_, _, diffW := m.columnWidths()
	width, rows := diffW-4, m.listRows()
	if width <= 0 || d.total == 0 {
		return
	}
	const margin = 2
	if top := max(m.cursor-margin, 0); m.diffY > top {
		m.diffY = top
		return
	}
	// Each row is at least one line, so rows further up than this can't be on screen.
	m.diffY = max(m.diffY, m.cursor-rows)
	bottom := min(m.cursor+margin, d.total-1)
	var heights []int
	used := 0
	for r := m.diffY; r <= bottom; r++ {
		h := len(m.diffRowLines(d, r, width))
		heights = append(heights, h)
		used += h
	}
	for i := 0; used > rows && m.diffY < m.cursor; i++ {
		used -= heights[i]
		m.diffY++
	}
}

// selection returns the selected document rows: the visual range, or the cursor row.
func (m *model) selection() (a, b int) {
	if !m.visual {
		return m.cursor, m.cursor
	}
	return min(m.visualFrom, m.cursor), max(m.visualFrom, m.cursor)
}

// lineTarget is where an inline comment goes, in GitHub's terms.
type lineTarget struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
}

func (t lineTarget) String() string {
	side := map[string]string{"LEFT": "old", "RIGHT": "new"}
	if t.StartLine == 0 {
		return fmt.Sprintf("%s:%d (%s)", t.Path, t.Line, side[t.Side])
	}
	if t.StartSide == t.Side {
		return fmt.Sprintf("%s:%d–%d (%s)", t.Path, t.StartLine, t.Line, side[t.Side])
	}
	return fmt.Sprintf("%s:%s %d – %s %d", t.Path, side[t.StartSide], t.StartLine, side[t.Side], t.Line)
}

// commentTarget turns the cursor or selection into a GitHub comment position. In
// side-by-side view it uses the active side (falling back to the other one); in
// unified view the lines decide: new side unless only removed lines are selected.
func (m *model) commentTarget(d *diffDoc) (lineTarget, error) {
	a, b := m.selection()
	fi, _ := m.rowAnchors(d, a)
	t := lineTarget{Path: d.files[fi].Path}
	span := func(side string) (first, last int, ok bool) {
		for r := a; r <= b; r++ {
			_, as := m.rowAnchors(d, r)
			for _, an := range as {
				if an.side == side {
					if !ok {
						first = an.line
					}
					last, ok = an.line, true
				}
			}
		}
		return
	}
	setSpan := func(side string) bool {
		first, last, ok := span(side)
		if ok {
			t.Line, t.Side = last, side
			if first != last {
				t.StartLine, t.StartSide = first, side
			}
		}
		return ok
	}
	if m.split {
		side, other := m.sideName(), "LEFT"
		if side == "LEFT" {
			other = "RIGHT"
		}
		if setSpan(side) || setSpan(other) {
			return t, nil
		}
		return t, errors.New("nothing to comment on here")
	}
	f := d.files[fi]
	hasDel, hasAdd := false, false
	for r := a; r <= b; r++ {
		if _, row := d.locate(r); row >= 0 {
			switch lineKind(f.Body()[row]) {
			case '-':
				hasDel = true
			case '+':
				hasAdd = true
			}
		}
	}
	switch {
	case !hasDel:
		if setSpan("RIGHT") {
			return t, nil
		}
	case !hasAdd:
		if setSpan("LEFT") {
			return t, nil
		}
	default:
		// A range from removed into added lines: it starts on the old side and ends
		// on the new one, as GitHub's unified view does it.
		_, start := m.rowAnchors(d, a)
		_, end := m.rowAnchors(d, b)
		t.StartSide, t.StartLine = start[0].side, start[0].line
		t.Side, t.Line = end[len(end)-1].side, end[len(end)-1].line
		if t.StartSide == "RIGHT" && t.Side == "LEFT" {
			return t, errors.New("a comment can't run from added lines into removed ones; select fewer lines")
		}
		return t, nil
	}
	return t, errors.New("nothing to comment on here")
}

// selectedText returns the selected lines' text on one side (without the +/- sign).
func (m *model) selectedText(d *diffDoc, side string) []string {
	a, b := m.selection()
	var out []string
	for r := a; r <= b; r++ {
		fi, row := d.locate(r)
		if row < 0 {
			continue
		}
		f := d.files[fi]
		if m.split {
			sr := f.Split().rows[row]
			c := sr.right
			if side == "LEFT" {
				c = sr.left
			}
			if sr.header == "" && c.kind != 0 {
				out = append(out, c.text)
			}
			continue
		}
		l, n := f.Body()[row], f.LineNums()[row]
		if (side == "RIGHT" && n.new > 0) || (side == "LEFT" && n.old > 0) {
			out = append(out, strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(l, "+"), "-"), " "))
		}
	}
	return out
}

func (m *model) sideName() string {
	if m.side == 'L' {
		return "LEFT"
	}
	return "RIGHT"
}

// draft is an inline comment kept locally until you submit a review.
type draft struct {
	lineTarget
	Body   string `json:"body"`
	Commit string `json:"commit,omitempty"`
}

// saveDrafts replaces a PR's drafts in the state file.
func saveDrafts(url string, ds []draft) *state {
	st, _ := loadState()
	if len(ds) == 0 {
		delete(st.Drafts, url)
	} else {
		st.Drafts[url] = ds
	}
	_ = st.save()
	return st
}

func clearDrafts(urls []string) *state {
	st, _ := loadState()
	for _, u := range urls {
		delete(st.Drafts, u)
	}
	_ = st.save()
	return st
}

// rowNotes are the one-line notes shown under a diff row for the review threads
// and draft comments that end on it.
func (m *model) rowNotes(d *diffDoc, r, width int) []string {
	fi, anchors := m.rowAnchors(d, r)
	if len(anchors) == 0 {
		return nil
	}
	pr, ok := m.selectedPR()
	if !ok {
		return nil
	}
	path := d.files[fi].Path
	at := func(side string, line int) bool {
		for _, an := range anchors {
			if an.side == side && an.line == line {
				return true
			}
		}
		return false
	}
	var out []string
	note := func(s string) { out = append(out, "   "+ansi.Truncate(s, max(width-3, 1), "…")) }
	if c := m.convs[pr.URL]; c != nil {
		for _, t := range c.Threads {
			if t.ID == "" || t.Outdated || t.Path != path || !at(t.Side, t.Line) {
				continue
			}
			first := t.Comments[0]
			s := fmt.Sprintf("💬 %s: %s", first.Author, firstLine(first.Body))
			if n := len(t.Comments) - 1; n > 0 {
				s += fmt.Sprintf("  (+%d)", n)
			}
			st := hunkStyle
			if t.Resolved {
				st, s = dimStyle, s+"  ✓ resolved"
			}
			note(st.Render(s))
		}
	}
	for _, dr := range m.st.Drafts[pr.URL] {
		if dr.Path == path && at(dr.Side, dr.Line) {
			note(newStyle.Render("✎ draft: " + firstLine(dr.Body)))
		}
	}
	return out
}

func firstLine(body string) string {
	body = strings.TrimSpace(htmlComment.ReplaceAllString(cleanText(body), ""))
	return strings.SplitN(body, "\n", 2)[0]
}

// threadAt returns the index of the first review thread ending on row r, or -1.
func (m *model) threadAt(d *diffDoc, r int) int {
	fi, anchors := m.rowAnchors(d, r)
	pr, _ := m.selectedPR()
	c := m.convs[pr.URL]
	if c == nil || len(anchors) == 0 {
		return -1
	}
	for ti, t := range c.Threads {
		if t.ID == "" || t.Outdated || t.Path != d.files[fi].Path {
			continue
		}
		for _, an := range anchors {
			if an.side == t.Side && an.line == t.Line {
				return ti
			}
		}
	}
	return -1
}

var (
	cursorNumStyle = lipgloss.NewStyle().Background(accent).Foreground(lipgloss.Color("0")).Bold(true)
	selNumStyle    = lipgloss.NewStyle().Background(lipgloss.Color("4")).Foreground(lipgloss.Color("0"))
)

// rowMark says how row r is highlighted: 'c' cursor, 's' selected, 0 not at all.
func (m *model) rowMark(r int) byte {
	if m.focus != focusDiff {
		return 0
	}
	if r == m.cursor {
		return 'c'
	}
	if a, b := m.selection(); m.visual && r >= a && r <= b {
		return 's'
	}
	return 0
}

func markStyle(mark byte) (lipgloss.Style, bool) {
	switch mark {
	case 'c':
		return cursorNumStyle, true
	case 's':
		return selNumStyle, true
	}
	return lipgloss.Style{}, false
}

// ghInput runs gh with JSON on stdin.
func ghInput(ctx context.Context, input any, args ...string) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if out, err := cmd.Output(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("gh %s: %s", args[1], msg)
	}
	return nil
}

// prAPIPath turns a PR URL into its REST path, e.g. repos/o/r/pulls/12.
func prAPIPath(pr PR) string {
	return fmt.Sprintf("repos/%s/pulls/%d", pr.Repository.NameWithOwner, pr.Number)
}

type reviewRequest struct {
	CommitID string       `json:"commit_id,omitempty"`
	Body     string       `json:"body,omitempty"`
	Event    string       `json:"event"`
	Comments []apiComment `json:"comments,omitempty"`
}

type apiComment struct {
	lineTarget
	Body string `json:"body"`
}

// newReviewRequest builds the one REST call that submits a review together with
// its draft inline comments.
func newReviewRequest(kind reviewKind, body string, drafts []draft) reviewRequest {
	req := reviewRequest{Body: body, Event: map[reviewKind]string{
		reviewApprove: "APPROVE", reviewComment: "COMMENT", reviewRequestChanges: "REQUEST_CHANGES",
	}[kind]}
	for _, d := range drafts {
		if req.CommitID == "" {
			req.CommitID = d.Commit
		}
		req.Comments = append(req.Comments, apiComment{d.lineTarget, d.Body})
	}
	return req
}

func submitReviewWithDrafts(ctx context.Context, pr PR, kind reviewKind, body string, drafts []draft) error {
	return ghInput(ctx, newReviewRequest(kind, body, drafts), "api", prAPIPath(pr)+"/reviews", "--method", "POST", "--input", "-")
}

// postLineComment posts a single inline comment right away.
func postLineComment(ctx context.Context, pr PR, d draft) error {
	req := struct {
		apiComment
		CommitID string `json:"commit_id,omitempty"`
	}{apiComment{d.lineTarget, d.Body}, d.Commit}
	return ghInput(ctx, req, "api", prAPIPath(pr)+"/comments", "--method", "POST", "--input", "-")
}

// fetchHead returns the PR's head commit, which inline comments are anchored to.
func fetchHead(ctx context.Context, url string) string {
	out, err := gh(ctx, "pr", "view", url, "--json", "headRefOid", "-q", ".headRefOid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

type lineCommentDoneMsg struct {
	pr  PR
	err error
}
