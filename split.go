package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// cell is one side of a side-by-side row; kind 0 means the side is empty.
type cell struct {
	num  int
	text string
	kind byte
	idx  int // index into FileDiff.Body()
}

type splitRow struct {
	header      string // hunk header or file meta line, spanning both sides
	left, right cell
	src         int // index into FileDiff.Body() of the line this row starts at
}

type splitDiff struct {
	rows       []splitRow
	hunkStarts []int
	longest    int
	maxNum     int
}

var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@ ?(.*)$`)

func (f *FileDiff) Split() *splitDiff {
	if f.split == nil {
		f.split = buildSplit(f)
	}
	return f.split
}

func buildSplit(f *FileDiff) *splitDiff {
	body := f.Body()
	sd := &splitDiff{}
	if f.firstHunk < 0 {
		for i, l := range body {
			sd.rows = append(sd.rows, splitRow{header: l, src: i})
		}
		return sd
	}
	push := func(r splitRow) {
		for _, c := range []cell{r.left, r.right} {
			sd.longest = max(sd.longest, len([]rune(c.text)))
			sd.maxNum = max(sd.maxNum, c.num)
		}
		sd.rows = append(sd.rows, r)
	}
	ln, rn := 0, 0
	for i := 0; i < len(body); {
		l := body[i]
		switch {
		case strings.HasPrefix(l, "@@"):
			if m := hunkRe.FindStringSubmatch(l); m != nil {
				ln, _ = strconv.Atoi(m[1])
				rn, _ = strconv.Atoi(m[2])
			}
			sd.hunkStarts = append(sd.hunkStarts, len(sd.rows))
			sd.rows = append(sd.rows, splitRow{header: l, src: i})
			i++
		case strings.HasPrefix(l, "-"), strings.HasPrefix(l, "+"):
			dels, adds, j := changeBlock(body, i)
			for k := 0; k < max(len(dels), len(adds)); k++ {
				r := splitRow{src: -1}
				if k < len(dels) {
					r.left = cell{ln, body[dels[k]][1:], '-', dels[k]}
					r.src = dels[k]
					ln++
				}
				if k < len(adds) {
					r.right = cell{rn, body[adds[k]][1:], '+', adds[k]}
					if r.src < 0 {
						r.src = adds[k]
					}
					rn++
				}
				push(r)
			}
			i = j
		case strings.HasPrefix(l, `\`):
			i++
		default:
			text := ""
			if l != "" {
				text = l[1:]
			}
			push(splitRow{left: cell{ln, text, ' ', i}, right: cell{rn, text, ' ', i}, src: i})
			ln++
			rn++
			i++
		}
	}
	return sd
}

// layout returns the line-number gutter width and text width of each half.
func (sd *splitDiff) layout(width int) (numW, textW int) {
	numW = max(len(strconv.Itoa(sd.maxNum)), 3)
	half := (width - 1) / 2
	return numW, max(half-numW-1, 1)
}

// changeBlock reads the run of removals followed by additions starting at body[i],
// returning their indices and the index after the block. Removals and additions are
// paired by position so a replaced line sits next to (and is word-diffed against) its
// replacement.
func changeBlock(body []string, i int) (dels, adds []int, next int) {
	j := i
	for ; j < len(body) && (lineKind(body[j]) == '-' || strings.HasPrefix(body[j], `\`)); j++ {
		if lineKind(body[j]) == '-' {
			dels = append(dels, j)
		}
	}
	for ; j < len(body) && (lineKind(body[j]) == '+' || strings.HasPrefix(body[j], `\`)); j++ {
		if lineKind(body[j]) == '+' {
			adds = append(adds, j)
		}
	}
	return dels, adds, j
}

// rowLines renders row i as one or more screen lines (more than one when wrapping).
// mark highlights the line number on side ('L'/'R', or the other one when that
// side is empty): 'c' for the cursor, 's' for a selected row.
func (sd *splitDiff) rowLines(f *FileDiff, i, x, width int, wrap bool, mark, side byte) []string {
	r := sd.rows[i]
	if r.header != "" {
		if strings.HasPrefix(r.header, "@@") {
			return f.hunkHeaderLines(r.header, i == 0, width)
		}
		return []string{dimStyle.Render(ansi.Truncate(r.header, width, "…"))}
	}
	hl := f.Highlighted()
	numW, textW := sd.layout(width)
	markL, markR := byte(0), byte(0)
	if mark != 0 {
		if (side == 'L' && r.left.kind != 0) || r.right.kind == 0 {
			markL = mark
		} else {
			markR = mark
		}
	}
	left := renderCell(hl, r.left, x, numW, textW, wrap, markL)
	right := renderCell(hl, r.right, x, numW, textW, wrap, markR)
	blank := strings.Repeat(" ", numW+1+textW)
	sep := dimStyle.Render("│")
	out := make([]string, max(len(left), len(right)))
	for k := range out {
		l, rr := blank, blank
		if k < len(left) {
			l = left[k]
		}
		if k < len(right) {
			rr = right[k]
		}
		out[k] = l + sep + rr
	}
	return out
}

func renderCell(hl []string, c cell, x, numW, textW int, wrap bool, mark byte) []string {
	if c.kind == 0 {
		return nil
	}
	numStyle := dimStyle
	switch c.kind {
	case '-':
		numStyle = delStyle
	case '+':
		numStyle = addStyle
	}
	if st, ok := markStyle(mark); ok {
		numStyle = st
	}
	fill := fillStyle(c.kind)
	gutter := numStyle.Render(fmt.Sprintf("%*d", numW, c.num)) + fill.Render(" ")
	if !wrap {
		return []string{gutter + viewport(hl[c.idx], x, textW, fill)}
	}
	segs := wrapText(hl[c.idx], textW, fill)
	cont := strings.Repeat(" ", numW) + fill.Render(" ")
	for k := range segs {
		if k == 0 {
			segs[k] = gutter + segs[k]
		} else {
			segs[k] = cont + segs[k]
		}
	}
	return segs
}
