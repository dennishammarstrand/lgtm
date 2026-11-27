package main

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// cleanText makes text from GitHub safe to print: it drops byte-order marks and
// carriage returns (Windows line endings) and shows other control characters,
// including escape codes inside a file, as �. Printed raw, a carriage return sends
// the cursor back to the start of the line and the rest overwrites the screen.
func cleanText(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\ufeff' }) {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n', r == '\t':
			return r
		case r == '\r', r == '\ufeff':
			return -1
		case r < 0x20, r == 0x7f:
			return '\ufffd'
		}
		return r
	}, s)
}

// joinsGraphemes reports runes that can merge with others into one displayed
// character: joiners, variation selectors, skin tones, keycaps, flag parts, tags
// and combining marks.
func joinsGraphemes(r rune) bool {
	return r == '\u200d' || r == '\ufe0f' || r == '\ufe0e' || r == '\u20e3' ||
		(r >= 0x1F3FB && r <= 0x1F3FF) || (r >= 0x1F1E6 && r <= 0x1F1FF) ||
		(r >= 0xE0020 && r <= 0xE007F) || unicode.Is(unicode.Mn, r)
}

// displaySafe makes multi-code-point characters (👍🏽, 👨‍👩‍👧, ❤️, 1️⃣) take exactly the
// columns the layout counted for them, also in terminals without grapheme
// clustering (xterm.js in Orca and VS Code, among others). Those draw each code
// point on its own, so 👍🏽 takes 4 columns instead of 2: the line wraps and the
// screen scrolls on every redraw. A sequence the terminal would draw at a different
// width is shown as its first character, padded to the counted width.
//
// Only the display changes; text you type is posted as is.
func displaySafe(s string) string {
	if !strings.ContainsFunc(s, joinsGraphemes) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	state := -1
	for s != "" {
		var cluster string
		cluster, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		runes := []rune(cluster)
		if len(runes) == 1 {
			b.WriteString(cluster)
			continue
		}
		want, drawn := ansi.StringWidth(cluster), 0
		for _, r := range runes {
			drawn += runewidth.RuneWidth(r)
		}
		if drawn == want {
			b.WriteString(cluster) // e.g. a letter with a combining accent
			continue
		}
		b.WriteRune(runes[0])
		b.WriteString(strings.Repeat(" ", max(want-runewidth.RuneWidth(runes[0]), 0)))
	}
	return b.String()
}
