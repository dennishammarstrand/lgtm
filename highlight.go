package main

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Files larger than this are only tinted, not syntax highlighted (e.g. lockfiles).
const maxHighlightLines = 5000

var (
	syntaxTheme = styles.Get("catppuccin-mocha")
	addBg       = lipgloss.AdaptiveColor{Light: "#dafbe1", Dark: "#16341f"}
	delBg       = lipgloss.AdaptiveColor{Light: "#ffebe9", Dark: "#3d1719"}
	addEmphBg   = lipgloss.AdaptiveColor{Light: "#aceebb", Dark: "#2b6638"}
	delEmphBg   = lipgloss.AdaptiveColor{Light: "#ffcecb", Dark: "#7a2a2f"}
)

// Lines wrap onto at most this many screen lines; the rest is truncated.
const maxWrapLines = 10

type styleKey struct {
	kind byte
	tok  chroma.TokenType
	emph bool
}

var styleCache = map[styleKey]lipgloss.Style{}

func lineKind(l string) byte {
	if l != "" && (l[0] == '+' || l[0] == '-') {
		return l[0]
	}
	return ' '
}

// fillStyle is the background for a line of the given kind ('+', '-' or ' ').
func fillStyle(kind byte) lipgloss.Style {
	switch kind {
	case '+':
		return lipgloss.NewStyle().Background(addBg)
	case '-':
		return lipgloss.NewStyle().Background(delBg)
	}
	return lipgloss.NewStyle()
}

func tokenStyle(kind byte, t chroma.TokenType, emph bool) lipgloss.Style {
	key := styleKey{kind, t, emph}
	if st, ok := styleCache[key]; ok {
		return st
	}
	st := fillStyle(kind)
	if emph {
		switch kind {
		case '+':
			st = st.Background(addEmphBg)
		case '-':
			st = st.Background(delEmphBg)
		}
	}
	e := syntaxTheme.Get(t)
	if e.Colour.IsSet() {
		st = st.Foreground(lipgloss.Color(e.Colour.String()))
	}
	if e.Bold == chroma.Yes {
		st = st.Bold(true)
	}
	if e.Italic == chroma.Yes {
		st = st.Italic(true)
	}
	styleCache[key] = st
	return st
}

// Highlighted returns each body line's content (without its +/-/space prefix) rendered
// with syntax colours and the added/removed background. Header lines stay empty.
func (f *FileDiff) Highlighted() []string {
	if f.hl != nil {
		return f.hl
	}
	body := f.Body()
	f.hl = make([]string, len(body))
	if f.firstHunk < 0 {
		return f.hl
	}
	f.emph = wordEmphasis(body)
	var lexer chroma.Lexer
	if len(body) <= maxHighlightLines {
		if lexer = lexers.Match(f.Path); lexer != nil {
			lexer = chroma.Coalesce(lexer)
		}
	}
	// Each hunk's old side (context + removed) and new side (context + added) is
	// tokenised as one text, so multi-line strings and comments colour correctly.
	var oldSide, newSide []int
	flush := func() {
		f.highlightSide(lexer, oldSide, '-')
		f.highlightSide(lexer, newSide, '+')
		oldSide, newSide = oldSide[:0], newSide[:0]
	}
	for i, l := range body {
		switch {
		case strings.HasPrefix(l, "@@"):
			flush()
		case strings.HasPrefix(l, `\`):
		case lineKind(l) == '-':
			oldSide = append(oldSide, i)
		case lineKind(l) == '+':
			newSide = append(newSide, i)
		default:
			oldSide = append(oldSide, i)
			newSide = append(newSide, i)
		}
	}
	flush()
	return f.hl
}

func (f *FileDiff) highlightSide(lexer chroma.Lexer, idx []int, side byte) {
	if len(idx) == 0 {
		return
	}
	body := f.Body()
	content := func(i int) string {
		if body[i] == "" {
			return ""
		}
		return body[i][1:]
	}
	// Context lines appear on both sides; the new side renders them.
	owns := func(i int) bool {
		k := lineKind(body[i])
		return k == side || (k == ' ' && side == '+')
	}
	plain := func() {
		for _, i := range idx {
			if owns(i) {
				var b strings.Builder
				writeStyled(&b, content(i), lineKind(body[i]), chroma.Text, f.emph[i], 0)
				f.hl[i] = b.String()
			}
		}
	}
	if lexer == nil {
		plain()
		return
	}
	lines := make([]string, len(idx))
	for k, i := range idx {
		lines[k] = content(i)
	}
	it, err := lexer.Tokenise(nil, strings.Join(lines, "\n")+"\n")
	if err != nil {
		plain()
		return
	}
	var b strings.Builder
	k, col := 0, 0
	for _, tok := range it.Tokens() {
		for p, part := range strings.Split(tok.Value, "\n") {
			if p > 0 {
				if k < len(idx) && owns(idx[k]) {
					f.hl[idx[k]] = b.String()
				}
				b.Reset()
				k, col = k+1, 0
			}
			if part != "" && k < len(idx) {
				i := idx[k]
				col = writeStyled(&b, part, lineKind(body[i]), tok.Type, f.emph[i], col)
			}
		}
	}
	if k < len(idx) && owns(idx[k]) {
		f.hl[idx[k]] = b.String()
	}
}

// viewport pans an ANSI-styled string x columns to the right and fits it to exactly
// w columns, padding with fill so the line background spans the full width.
func viewport(s string, x, w int, fill lipgloss.Style) string {
	if x > 0 {
		s = ansi.TruncateLeft(s, x, "")
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if n := w - ansi.StringWidth(s); n > 0 {
		s += fill.Render(strings.Repeat(" ", n))
	}
	return s
}

// writeStyled renders text that starts at rune column col of its line, switching to
// the emphasis background for runes marked as changed. It returns the next column.
func writeStyled(b *strings.Builder, text string, kind byte, t chroma.TokenType, emph []bool, col int) int {
	runes := []rune(text)
	if emph == nil {
		b.WriteString(tokenStyle(kind, t, false).Render(text))
		return col + len(runes)
	}
	at := func(i int) bool { return col+i < len(emph) && emph[col+i] }
	for start := 0; start < len(runes); {
		e, end := at(start), start+1
		for end < len(runes) && at(end) == e {
			end++
		}
		b.WriteString(tokenStyle(kind, t, e).Render(string(runes[start:end])))
		start = end
	}
	return col + len(runes)
}

// wrapText splits an ANSI-styled line into screen lines of exactly w columns, ending
// every wrapped line with a ↵ marker like delta/lazygit.
func wrapText(s string, w int, fill lipgloss.Style) []string {
	total := ansi.StringWidth(s)
	if w < 2 || total <= w {
		return []string{viewport(s, 0, w, fill)}
	}
	step := w - 1
	marker := fill.Foreground(dimColor).Render("↵")
	var out []string
	for start := 0; ; start += step {
		if total-start <= w || len(out) == maxWrapLines-1 {
			return append(out, viewport(ansi.TruncateLeft(s, start, ""), 0, w, fill))
		}
		out = append(out, viewport(ansi.Cut(s, start, start+step), 0, step, fill)+marker)
	}
}

// hunkHeaderLines renders a hunk header lazygit-style: the new side's start line and
// the enclosing function (syntax highlighted) in a small box, with a blank line above
// to separate it from the previous hunk.
func (f *FileDiff) hunkHeaderLines(header string, first bool, width int) []string {
	label, ok := f.hunkLabels[header]
	if !ok {
		label = hunkStyle.Render(header)
		if m := hunkRe.FindStringSubmatch(header); m != nil {
			start := m[2]
			if start == "0" {
				start = m[1] // a deleted file has no new lines; show where the old ones start
			}
			label = hunkStyle.Render(start + ":")
			if ctx := strings.TrimSpace(m[3]); ctx != "" {
				label += " " + highlightSnippet(f.Path, ctx)
			}
		}
		if f.hunkLabels == nil {
			f.hunkLabels = map[string]string{}
		}
		f.hunkLabels[header] = label
	}
	if width < 5 {
		return []string{ansi.Truncate(label, max(width, 0), "")}
	}
	label = ansi.Truncate(label, width-4, "…")
	bar := strings.Repeat("─", ansi.StringWidth(label)+2)
	lines := []string{
		hunkStyle.Render("┌" + bar + "┐"),
		hunkStyle.Render("│ ") + label + hunkStyle.Render(" │"),
		hunkStyle.Render("└" + bar + "┘"),
	}
	if !first {
		lines = append([]string{""}, lines...)
	}
	return lines
}

func highlightSnippet(path, text string) string {
	lexer := lexers.Match(path)
	if lexer == nil {
		return tokenStyle(' ', chroma.Text, false).Render(text)
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, text)
	if err != nil {
		return tokenStyle(' ', chroma.Text, false).Render(text)
	}
	var b strings.Builder
	for _, tok := range it.Tokens() {
		if v := strings.TrimRight(tok.Value, "\n"); v != "" {
			b.WriteString(tokenStyle(' ', tok.Type, false).Render(v))
		}
	}
	return b.String()
}
