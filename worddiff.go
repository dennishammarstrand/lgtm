package main

import (
	"strings"
	"unicode"
)

// Lines sharing less than this fraction of their text are treated as rewritten
// rather than edited, and get no word emphasis (it would light up everything).
const minWordSimilarity = 0.4

// wordEmphasis pairs each removed line with the added line at the same position in
// its change block and marks, per rune, which parts of the two lines differ.
func wordEmphasis(body []string) map[int][]bool {
	emph := map[int][]bool{}
	for i := 0; i < len(body); {
		if k := lineKind(body[i]); k != '-' && k != '+' {
			i++
			continue
		}
		dels, adds, next := changeBlock(body, i)
		for k := 0; k < min(len(dels), len(adds)); k++ {
			a, b := body[dels[k]][1:], body[adds[k]][1:]
			if ea, eb, ok := wordDiff(a, b); ok {
				emph[dels[k]], emph[adds[k]] = ea, eb
			}
		}
		i = next
	}
	return emph
}

// wordDiff returns per-rune "changed" flags for a and b, based on the longest common
// subsequence of their word tokens. ok is false when the lines are too different.
func wordDiff(a, b string) (ea, eb []bool, ok bool) {
	ta, tb := wordTokens(a), wordTokens(b)
	if len(ta)*len(tb) > 250_000 {
		return nil, nil, false
	}
	// lcs[i][j] is the LCS length of ta[i:] and tb[j:].
	lcs := make([][]int, len(ta)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(tb)+1)
	}
	for i := len(ta) - 1; i >= 0; i-- {
		for j := len(tb) - 1; j >= 0; j-- {
			if ta[i] == tb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	ea, eb = make([]bool, len([]rune(a))), make([]bool, len([]rune(b)))
	posA, posB, common := 0, 0, 0
	mark := func(flags []bool, pos int, tok string) int {
		n := len([]rune(tok))
		for p := pos; p < pos+n; p++ {
			flags[p] = true
		}
		return pos + n
	}
	i, j := 0, 0
	for i < len(ta) || j < len(tb) {
		switch {
		case i < len(ta) && j < len(tb) && ta[i] == tb[j]:
			n := len([]rune(ta[i]))
			posA, posB, common = posA+n, posB+n, common+n
			i, j = i+1, j+1
		case j == len(tb) || (i < len(ta) && lcs[i+1][j] >= lcs[i][j+1]):
			posA = mark(ea, posA, ta[i])
			i++
		default:
			posB = mark(eb, posB, tb[j])
			j++
		}
	}
	total := len(ea) + len(eb)
	if total == 0 || float64(2*common)/float64(total) < minWordSimilarity {
		return nil, nil, false
	}
	return ea, eb, true
}

// wordTokens splits s into identifier/number runs, whitespace runs and single
// punctuation characters.
func wordTokens(s string) []string {
	class := func(r rune) int {
		switch {
		case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			return 1
		case unicode.IsSpace(r):
			return 2
		}
		return 0
	}
	var toks []string
	var cur strings.Builder
	prev := -1
	for _, r := range s {
		c := class(r)
		if cur.Len() > 0 && (c != prev || c == 0) {
			toks = append(toks, cur.String())
			cur.Reset()
		}
		cur.WriteRune(r)
		prev = c
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks
}
