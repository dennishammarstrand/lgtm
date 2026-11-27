package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

const sampleDiff = `diff --git a/src/a.go b/src/a.go
index 111..222 100644
--- a/src/a.go
+++ b/src/a.go
@@ -1,3 +1,4 @@
 package a
-var x = 1
+var x = 2
+var y = 3
@@ -10,2 +11,2 @@ func f() {
-	return
+	return nil
diff --git a/old.txt b/old.txt
deleted file mode 100644
index 333..000
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
diff --git a/img.png b/img.png
new file mode 100644
Binary files /dev/null and b/img.png differ
`

func TestParseDiff(t *testing.T) {
	files := parseDiff(sampleDiff)
	if len(files) != 3 {
		t.Fatalf("want 3 files, got %d", len(files))
	}

	a := files[0]
	if a.Path != "src/a.go" || a.Add != 3 || a.Del != 2 {
		t.Errorf("a.go: got path=%q +%d -%d", a.Path, a.Add, a.Del)
	}
	body := a.Body()
	if body[0] != "@@ -1,3 +1,4 @@" {
		t.Errorf("body should start at first hunk, got %q", body[0])
	}
	if len(a.hunkStarts) != 2 || a.hunkStarts[0] != 0 || body[a.hunkStarts[1]][:2] != "@@" {
		t.Errorf("unexpected hunk starts %v", a.hunkStarts)
	}

	if files[1].Path != "old.txt" || files[1].Del != 1 {
		t.Errorf("deleted file: got path=%q -%d", files[1].Path, files[1].Del)
	}

	bin := files[2]
	if bin.Path != "img.png" || len(bin.Body()) != len(bin.Lines) {
		t.Errorf("binary file without hunks should show its header, got %q %v", bin.Path, bin.Body())
	}
}

func TestBuildSplitPairsChangesWithLineNumbers(t *testing.T) {
	a := parseDiff(sampleDiff)[0]
	sd := a.Split()

	type side struct {
		num  int
		text string
	}
	want := []struct {
		header      bool
		left, right side
	}{
		{header: true},
		{left: side{1, "package a"}, right: side{1, "package a"}},
		{left: side{2, "var x = 1"}, right: side{2, "var x = 2"}},
		{right: side{3, "var y = 3"}},
		{header: true},
		{left: side{10, "    return"}, right: side{11, "    return nil"}},
	}
	if len(sd.rows) != len(want) {
		t.Fatalf("want %d rows, got %d", len(want), len(sd.rows))
	}
	for i, w := range want {
		r := sd.rows[i]
		if (r.header != "") != w.header ||
			r.left.num != w.left.num || r.left.text != w.left.text ||
			r.right.num != w.right.num || r.right.text != w.right.text {
			t.Errorf("row %d: got %+v, want %+v", i, r, w)
		}
	}
	if len(sd.hunkStarts) != 2 || sd.hunkStarts[1] != 4 {
		t.Errorf("hunk starts: got %v", sd.hunkStarts)
	}
	// src maps each row back to the unified line so toggling keeps the scroll position.
	if body := a.Body(); body[sd.rows[3].src] != "+var y = 3" {
		t.Errorf("row 3 src points at %q", body[sd.rows[3].src])
	}
}

func TestHighlightedKeepsEachLineAligned(t *testing.T) {
	f := parseDiff(`diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,4 +1,5 @@
 /* a comment
-   spanning */
+   spanning
+   lines */
 var s = ` + "`multi" + `
-old` + "`" + `
+new` + "`" + `
`)[0]
	body, hl := f.Body(), f.Highlighted()
	for i, l := range body {
		want := ""
		if !strings.HasPrefix(l, "@@") && l != "" {
			want = l[1:]
		}
		if got := ansi.Strip(hl[i]); got != want {
			t.Errorf("line %d: got %q, want %q", i, got, want)
		}
	}
}

func TestWordDiffMarksOnlyChangedWords(t *testing.T) {
	ea, eb, ok := wordDiff("var timeout = 30;", "var timeout = 60;")
	if !ok {
		t.Fatal("similar lines should get word emphasis")
	}
	marked := func(s string, flags []bool) string {
		var out []rune
		for i, r := range []rune(s) {
			if flags[i] {
				out = append(out, r)
			}
		}
		return string(out)
	}
	if got := marked("var timeout = 30;", ea); got != "30" {
		t.Errorf("old side: marked %q, want %q", got, "30")
	}
	if got := marked("var timeout = 60;", eb); got != "60" {
		t.Errorf("new side: marked %q, want %q", got, "60")
	}

	if _, _, ok := wordDiff("return result;", "LogTimings(elapsed, total);"); ok {
		t.Error("rewritten lines should not get word emphasis")
	}
}

func TestWrapTextFillsWidthWithMarkers(t *testing.T) {
	segs := wrapText(strings.Repeat("abcdefghij", 3), 12, fillStyle(' '))
	want := []string{"abcdefghija↵", "bcdefghijab↵", "cdefghij    "}
	if len(segs) != len(want) {
		t.Fatalf("got %d lines %q", len(segs), segs)
	}
	for i := range want {
		if got := ansi.Strip(segs[i]); got != want[i] {
			t.Errorf("line %d: got %q, want %q", i, got, want[i])
		}
	}

	long := wrapText(strings.Repeat("x", 1000), 10, fillStyle(' '))
	if len(long) != maxWrapLines || !strings.HasSuffix(ansi.Strip(long[len(long)-1]), "…") {
		t.Errorf("very long lines should be capped at %d lines ending in …, got %d", maxWrapLines, len(long))
	}
}

func TestScrollThumb(t *testing.T) {
	if _, size := (scroll{0, 10, 10}).thumb(20); size != 0 {
		t.Errorf("no scrollbar when everything fits, got size %d", size)
	}
	if start, size := (scroll{0, 25, 100}).thumb(20); start != 0 || size != 5 {
		t.Errorf("top: got start=%d size=%d, want 0,5", start, size)
	}
	if start, size := (scroll{75, 25, 100}).thumb(20); start != 15 || size != 5 {
		t.Errorf("bottom: got start=%d size=%d, want 15,5", start, size)
	}
	if _, size := (scroll{0, 1, 10000}).thumb(20); size != 1 {
		t.Errorf("thumb should be at least one row, got %d", size)
	}
}

func TestHunkHeaderBox(t *testing.T) {
	f := &FileDiff{Path: "Service.cs"}
	lines := f.hunkHeaderLines("@@ -359,49 +421,39 @@ private int GetNextDataId(int amount)", false, 80)
	label := "│ 421: private int GetNextDataId(int amount) │"
	bar := strings.Repeat("─", len([]rune(label))-2)
	want := []string{"", "┌" + bar + "┐", label, "└" + bar + "┘"}
	for i := range want {
		if got := ansi.Strip(lines[i]); got != want[i] {
			t.Errorf("line %d: got %q, want %q", i, got, want[i])
		}
	}
	if first := f.hunkHeaderLines("@@ -1,3 +1,4 @@", true, 80); len(first) != 3 || ansi.Strip(first[1]) != "│ 1: │" {
		t.Errorf("first hunk should have no spacer and just the line number, got %q", first)
	}
}

func TestPickerListsCurrentReviewersFirstForReRequest(t *testing.T) {
	p := &reviewerPicker{
		prs: []PR{{Reviewers: []reviewer{{"anna", "PENDING"}, {"tobias", "CHANGES_REQUESTED"}}}},
		results: []candidate{
			{value: "tobias", label: "tobias", note: "Tobias E"},
			{value: "@copilot", label: "Copilot", note: "AI review"},
		},
	}
	var got []string
	for _, c := range p.items() {
		got = append(got, c.label+" | "+c.note)
	}
	want := []string{"anna | ⏳ requested", "tobias | ✗ changes requested · re-request", "Copilot | AI review"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q, want %q", got, want)
	}

	multi := &reviewerPicker{prs: []PR{
		{Reviewers: []reviewer{{"anna", "APPROVED"}, {"Copilot", "PENDING"}}},
		{Reviewers: []reviewer{{"anna", "PENDING"}}},
	}}
	items := multi.items()
	if len(items) != 2 || items[0].note != "⏳ requested 2/2" || items[1].value != "@copilot" || items[1].note != "⏳ requested 1/2" {
		t.Errorf("across PRs, notes should count PRs and Copilot should map to @copilot: %+v", items)
	}
}

func TestCheckNotificationsReadsStatusDespiteGhExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	bin := t.TempDir()
	fake := `#!/bin/sh
case "$*" in
  *If-None-Match*) printf 'HTTP/2.0 304 Not Modified\r\nEtag: W/"old"\r\nX-Poll-Interval: 90\r\n\r\n'; echo "gh: HTTP 304" >&2; exit 1 ;;
  *) printf 'HTTP/2.0 200 OK\r\nEtag: W/"new"\r\nX-Poll-Interval: 60\r\n\r\n[]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	res, err := checkNotifications(t.Context(), "")
	if err != nil || !res.changed || res.etag != `W/"new"` || res.poll != time.Minute {
		t.Fatalf("first check: got %+v err=%v", res, err)
	}
	res, err = checkNotifications(t.Context(), `W/"old"`)
	if err != nil || res.changed || res.etag != `W/"old"` || res.poll != 90*time.Second {
		t.Fatalf("304 should mean unchanged, not an error: got %+v err=%v", res, err)
	}
}

func TestNotifCheckOnlyRefreshesWhenSomethingChanged(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := newModel(time.Minute, "", false)
	m.fetching, m.lastFetch = false, time.Now() // the startup fetch has finished
	send := func(res notifResult) {
		nm, _ := m.Update(notifMsg{res: res})
		m = nm.(model)
	}

	send(notifResult{changed: true, etag: "a"})
	if m.fetching {
		t.Error("the first check only records the ETag; startup already fetched")
	}
	send(notifResult{changed: false, etag: "a"})
	if m.fetching {
		t.Error("unchanged notifications must not trigger a full refresh")
	}
	send(notifResult{changed: true, etag: "b"})
	if !m.fetching || m.etag != "b" {
		t.Errorf("new notifications should trigger a full refresh (fetching=%v etag=%q)", m.fetching, m.etag)
	}

	m.fetching, m.lastFetch = false, time.Now().Add(-fullRefreshEvery)
	send(notifResult{changed: false, etag: "b"})
	if !m.fetching {
		t.Error("a full refresh is still due after fullRefreshEvery")
	}
}

func TestBuildTreeCompressesAndOrdersLikeLazygit(t *testing.T) {
	files := []*FileDiff{
		{Path: "packages/app/src/dev.ts", Add: 1, Status: 'M'},
		{Path: "packages/app/src/components/shared/ErrorMessage.vue", Add: 2, Status: 'A'},
		{Path: "packages/app/src/components/sections/Create/_CreateNode.vue", Del: 3, Status: 'M'},
	}
	root := buildTree(files)
	var got []string
	for _, r := range root.rows(func(string) bool { return false }) {
		got = append(got, strings.Repeat("  ", r.depth)+r.node.name)
	}
	want := []string{
		"packages/app/src",
		"  components",
		"    sections/Create",
		"      _CreateNode.vue",
		"    shared",
		"      ErrorMessage.vue",
		"  dev.ts",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("tree:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if root.add != 3 || root.del != 3 || len(root.files) != 3 || root.files[2].Path != "packages/app/src/dev.ts" {
		t.Errorf("root should total all files in tree order, got +%d -%d %d files", root.add, root.del, len(root.files))
	}

	collapsed := root.rows(func(p string) bool { return p == "packages/app/src/components" })
	if len(collapsed) != 3 {
		t.Errorf("collapsing components should hide its 4 rows, got %d rows", len(collapsed))
	}
	if r := buildTree([]*FileDiff{{Path: "a.go"}, {Path: "b/c.go"}}); r.name != "/" || len(r.files) != 2 {
		t.Errorf("files without a common directory get a / root, got %q", r.name)
	}
}

func TestDiffDocSpansFilesWithHeaders(t *testing.T) {
	files := parseDiff(sampleDiff)[:2] // a.go: 8 body lines, 2 hunks; old.txt: 2 lines, 1 hunk
	d := newDoc(files, true, false)
	if d.total != 1+8+1+2 {
		t.Fatalf("total rows: got %d", d.total)
	}
	if fi, row := d.locate(9); fi != 1 || row != -1 {
		t.Errorf("row 9 should be old.txt's header, got file %d row %d", fi, row)
	}
	if fi, row := d.locate(11); fi != 1 || row != 1 {
		t.Errorf("row 11 should be old.txt row 1, got file %d row %d", fi, row)
	}
	want := []int{0, 1, 6, 9, 10} // header, hunk, hunk, header, hunk
	if got := d.hunks(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("n/N stops: got %v, want %v", got, want)
	}
}

func TestBulkReviewSelectionAndPartialFailure(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pr := func(url, title string, mine bool) PR { return PR{URL: url, Title: title, Mine: mine} }
	m := newModel(time.Minute, "", true)
	m.prs = []PR{
		pr("a", "PROJ-12: Bump dependency", false),
		pr("b", "PROJ-12: Bump dependency", false),
		pr("c", "PROJ-34: Something else", false),
		pr("d", "PROJ-12: Bump dependency", true),
	}

	m.selectSameTitle()
	if len(m.selected) != 2 || !m.selected["a"] || !m.selected["b"] {
		t.Fatalf("* should select the review requests with the same title, got %v", m.selected)
	}
	m.selected["d"] = true
	targets, skipped := m.reviewTargets(reviewApprove, m.prs[2])
	if len(targets) != 2 || skipped != 1 {
		t.Fatalf("approve should target the selection minus your own PR, got %d targets, %d skipped", len(targets), skipped)
	}
	m.openModal(reviewApprove, targets)

	nm, _ := m.Update(reviewDoneMsg{prs: targets, kind: reviewApprove, failed: map[string]error{"b": errors.New("boom")}})
	m = nm.(model)
	if m.selected["a"] || !m.selected["b"] {
		t.Errorf("succeeded PRs leave the selection, failed ones stay: %v", m.selected)
	}
	if m.modal == nil || len(m.modal.prs) != 1 || m.modal.prs[0].URL != "b" {
		t.Errorf("the dialog should stay open with just the failed PR for a retry")
	}

	m.selectSameTitle() // cursor is on "a"; "a" is unselected, so this selects a and b again
	m.selectSameTitle() // all selected now, so this deselects them
	if m.selected["a"] || m.selected["b"] {
		t.Errorf("* again should deselect, got %v", m.selected)
	}
}

func TestRepliesAndReplyNotifications(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	at := func(min int) time.Time { return time.Date(2026, 11, 20, 12, min, 0, 0, time.UTC) }
	c := &conversation{Viewer: "me", Threads: []thread{
		{ID: "t1", Comments: []comment{
			{Author: "oden", CreatedAt: at(0)}, // before my comment: not a reply
			{Author: "me", CreatedAt: at(1)},
			{Author: "copilot-pull-request-reviewer", CreatedAt: at(2)}, // bots don't count
			{Author: "oden", CreatedAt: at(3), Body: "Yes, we use it"},
		}},
		{ID: "t2", Comments: []comment{{Author: "erik", CreatedAt: at(4)}}}, // I'm not in it
	}}
	if r := c.replies(); len(r) != 1 || r[0].Body != "Yes, we use it" {
		t.Fatalf("replies: got %+v", r)
	}
	if c.unseen(at(2)) != 1 || c.unseen(at(3)) != 0 {
		t.Error("unseen should count replies after the read marker")
	}

	pr := PR{URL: "u"}
	convs := map[string]*conversation{"u": c}
	if fresh, _ := reconcileReplies([]PR{pr}, convs); len(fresh) != 0 {
		t.Fatalf("existing replies are recorded silently the first time, got %d", len(fresh))
	}
	c.Threads[0].Comments = append(c.Threads[0].Comments, comment{Author: "oden", CreatedAt: at(5), Body: "Also…"})
	fresh, st := reconcileReplies([]PR{pr}, convs)
	if len(fresh) != 1 || fresh[0].comment.Body != "Also…" {
		t.Fatalf("a new reply should be notified once, got %+v", fresh)
	}
	if st.ReplySeen["u"] != at(3) {
		t.Errorf("notifying must not mark the reply read")
	}
	if fresh, _ := reconcileReplies([]PR{pr}, convs); len(fresh) != 0 {
		t.Errorf("already-notified replies must not notify again")
	}

	quiet := &conversation{Viewer: "me", Threads: []thread{{Comments: []comment{{Author: "me", CreatedAt: at(1)}}}}}
	reconcileReplies([]PR{{URL: "q"}}, map[string]*conversation{"q": quiet})
	quiet.Threads[0].Comments = append(quiet.Threads[0].Comments, comment{Author: "anna", CreatedAt: at(9)})
	if fresh, _ := reconcileReplies([]PR{{URL: "q"}}, map[string]*conversation{"q": quiet}); len(fresh) != 1 {
		t.Errorf("the first reply on a PR without replies yet should notify, got %d", len(fresh))
	}
}

func TestThreadsSortByMostRecentComment(t *testing.T) {
	at := func(min int) time.Time { return time.Date(2026, 11, 20, 12, min, 0, 0, time.UTC) }
	c := &conversation{Viewer: "me", Threads: []thread{
		{ID: "mine", Comments: []comment{{Author: "bot", CreatedAt: at(0)}, {Author: "me", CreatedAt: at(1)}}},
		{ID: "theirs", Comments: []comment{{Author: "anders", CreatedAt: at(5)}}},
	}}
	sortThreads(c)
	if c.Threads[0].ID != "theirs" {
		t.Errorf("the newest thread should come first, even if you're not in it; got %q", c.Threads[0].ID)
	}
}

func TestEmojiSequencesDontOverflowTerminalsWithoutGraphemeClustering(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// How xterm.js-like terminals measure: every code point on its own.
	termWidth := func(line string) int {
		n := 0
		for _, r := range ansi.Strip(line) {
			n += runewidth.RuneWidth(r)
		}
		return n
	}
	check := func(label string, m model) {
		t.Helper()
		for i, l := range strings.Split(m.View(), "\n") {
			if w := termWidth(l); w != m.width {
				t.Errorf("%s: line %d is %d columns wide on screen, want %d: %q", label, i, w, m.width, ansi.Strip(l))
			}
		}
	}

	pr := PR{URL: "u", Number: 556, Title: "migrate 🧑‍💻 from sem"}
	m := newModel(time.Minute, "", false)
	m.fetching = false
	m.prs = []PR{pr}
	m.convs = map[string]*conversation{"u": {Viewer: "me", Threads: []thread{{Comments: []comment{
		{Author: "me", Body: "andänvder vi denna?"},
		{Author: "oden", Body: "Yes 👍🏽 the whole 👨‍👩‍👧 uses it ❤️"},
	}}}}}
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = nm.(model)
	check("comments pane", m)

	m.openReply(pr, m.convs["u"].Threads[0])
	for _, r := range "ah okey 👍🏽" {
		nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = nm.(model)
	}
	check("reply, cursor at the end", m)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = nm.(model)
	check("reply, cursor inside the emoji", m)
	if got := m.modal.ta.Value(); got != "ah okey 👍🏽" {
		t.Errorf("the reply must be posted as typed, got %q", got)
	}
}

func TestShiftJKInFilesScrollsTheDiff(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pr := PR{URL: "u"}
	m := newModel(time.Minute, "", false)
	m.prs = []PR{pr}
	m.diffs["u"] = &diffEntry{files: parseDiff(sampleDiff)}
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	m = nm.(model)
	m.focus = focusFiles
	key := func(k string) {
		nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		m = nm.(model)
	}
	key("J")
	key("J")
	if m.diffY != 2 || m.fileCur != 0 || m.focus != focusFiles {
		t.Fatalf("J should scroll the diff and stay in the tree: diffY=%d fileCur=%d focus=%d", m.diffY, m.fileCur, m.focus)
	}
	key("K")
	if m.diffY != 1 {
		t.Errorf("K should scroll back up, diffY=%d", m.diffY)
	}
}

func TestHeaderNamesThePRWhileInItsDiff(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pr := PR{URL: "u", Number: 558, Title: "PROJ-56: Lägg till stöd för fullskärmsläge i lådan"}
	pr.Repository.NameWithOwner = "acme/web"
	pr.Author.Login = "oden"
	m := newModel(time.Minute, "", false)
	m.prs = []PR{pr}
	m.width = 90
	if h := m.viewHeader(); strings.Contains(h, "PROJ-56") {
		t.Errorf("the PR list header shouldn't name the PR: %q", h)
	}
	m.focus = focusDiff
	h := ansi.Strip(m.viewHeader())
	if !strings.Contains(h, "PROJ-56: Lägg") || !strings.Contains(h, "…") || !strings.Contains(h, "acme/web#558 · oden") {
		t.Errorf("the diff header should show the (shortened) title, ref and author: %q", h)
	}
}

func TestUpdateNoticeOnlyForNewerReleases(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	defer func(v string) { version = v }(version)
	for _, c := range []struct {
		running, latest string
		want            bool
	}{
		{"0.1.0", "v0.2.0", true},
		{"v0.2.0", "v0.2.0", false},
		{"0.3.0", "v0.2.0", false},
		{"0.0.0-SNAPSHOT-e00e453", "v0.2.0", false}, // snapshot builds never nag
		{"dev", "v0.2.0", false},
		{"0.1.0", "", false},
	} {
		version = c.running
		if got := updateAvailable(releaseVersion(), c.latest); got != c.want {
			t.Errorf("running %s, latest %q: got %v", c.running, c.latest, got)
		}
	}

	version = "0.1.0"
	m := newModel(time.Minute, "", false)
	m.width = 120
	if m.checkVersion() == nil {
		t.Fatal("a release build that never checked should check")
	}
	if m.checkVersion() != nil {
		t.Error("only one check at a time")
	}
	m = m.applyVersion(versionMsg{latest: "v0.2.0"})
	if h := ansi.Strip(m.viewHeader()); !strings.Contains(h, "↑ v0.2.0 available") {
		t.Errorf("header should announce the update: %q", h)
	}
	if m.checkVersion() != nil {
		t.Error("the next check should wait a day")
	}
	if f := ansi.Strip(m.viewFooter()); !strings.HasSuffix(f, " v0.1.0 ") || ansi.StringWidth(f) != m.width {
		t.Errorf("the footer should end with the version at the right edge: %q", f)
	}
}

func lineCommentModel(t *testing.T, split bool) model {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pr := PR{URL: "u", Number: 7}
	pr.Repository.NameWithOwner = "o/r"
	m := newModel(time.Minute, "", false)
	m.fetching, m.split = false, split
	m.prs = []PR{pr}
	m.diffs["u"] = &diffEntry{files: parseDiff(sampleDiff)[:1], head: "abc123"} // src/a.go
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = nm.(model)
	m.focus, m.fileCur = focusFiles, 1 // the file row, below its "src" directory
	return m
}

func TestLineCursorAndCommentTargets(t *testing.T) {
	// a.go unified rows: 0 @@, 1 " package a", 2 "-var x = 1", 3 "+var x = 2",
	// 4 "+var y = 3", 5 @@, 6 "-return", 7 "+return nil"
	m := lineCommentModel(t, false)
	key := func(k string) {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		if k == "esc" {
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		}
		nm, _ := m.Update(msg)
		m = nm.(model)
	}
	target := func() string {
		d := m.selectedDoc()
		tg, err := m.commentTarget(d)
		if err != nil {
			return "error: " + err.Error()
		}
		return tg.String()
	}

	key("l")
	if m.focus != focusDiff || m.cursor != 1 {
		t.Fatalf("entering the diff should put the cursor on the first line, not the hunk header: cursor=%d", m.cursor)
	}
	if got := target(); got != "src/a.go:1 (new)" {
		t.Errorf("context line: %s", got)
	}
	key("j")
	if got := target(); got != "src/a.go:2 (old)" {
		t.Errorf("removed line: %s", got)
	}
	key("V")
	key("j")
	key("j")
	if got := target(); got != "src/a.go:old 2 – new 3" {
		t.Errorf("removed-to-added range: %s", got)
	}
	key("j") // would cross into the next hunk
	if a, b := m.selection(); a != 2 || b != 4 {
		t.Errorf("selection must stay in its hunk, got rows %d–%d", a, b)
	}
	key("esc")
	key("j")
	if m.cursor != 6 {
		t.Errorf("j should skip the hunk header, cursor=%d", m.cursor)
	}

	s := lineCommentModel(t, true)
	s.focus = focusDiff
	d := s.selectedDoc()
	// split rows: 0 @@, 1 (1|1), 2 (-2|+2), 3 (·|+3), 4 @@, 5 (-10|+11)
	s.cursor = 3
	s.side = 'L'
	if tg, _ := s.commentTarget(d); tg.String() != "src/a.go:3 (new)" {
		t.Errorf("an empty side should fall back to the other: %s", tg)
	}
	s.cursor, s.visual, s.visualFrom = 3, true, 1
	s.side = 'R'
	if got := s.selectedText(d, "RIGHT"); strings.Join(got, "|") != "package a|var x = 2|var y = 3" {
		t.Errorf("suggestion text: %q", got)
	}
}

func TestDraftCommentFlowAndReviewRequest(t *testing.T) {
	m := lineCommentModel(t, false)
	send := func(msg tea.Msg) {
		nm, _ := m.Update(msg)
		m = nm.(model)
	}
	runes := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	send(runes("l"))
	send(runes("j"))
	send(runes("j")) // "+var x = 2", new line 2
	send(runes("c"))
	if m.modal == nil || m.modal.kind != reviewInline {
		t.Fatal("c in the diff should open the inline comment dialog")
	}
	for _, r := range "use a const" {
		send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	send(tea.KeyMsg{Type: tea.KeyCtrlS})
	ds := m.st.Drafts["u"]
	if m.modal != nil || len(ds) != 1 || ds[0].Line != 2 || ds[0].Side != "RIGHT" || ds[0].Commit != "abc123" {
		t.Fatalf("ctrl+s should save a draft on new line 2 at the diff's commit, got %+v", ds)
	}
	if st, _ := loadState(); len(st.Drafts["u"]) != 1 {
		t.Error("drafts must survive a restart")
	}
	d := m.selectedDoc()
	if notes := m.rowNotes(d, m.cursor, 100); len(notes) != 1 || !strings.Contains(ansi.Strip(notes[0]), "✎ draft: use a const") {
		t.Errorf("the draft should show under its line, got %q", notes)
	}

	send(runes("c")) // same line again: edit the draft
	if m.modal.editDraft != 0 || m.modal.ta.Value() != "use a const" {
		t.Error("c on a line with a draft should edit it")
	}
	send(tea.KeyMsg{Type: tea.KeyEsc})

	req := newReviewRequest(reviewApprove, "", m.st.Drafts["u"])
	got, _ := json.Marshal(req)
	want := `{"commit_id":"abc123","event":"APPROVE","comments":[{"path":"src/a.go","line":2,"side":"RIGHT","body":"use a const"}]}`
	if string(got) != want {
		t.Errorf("review request:\n got %s\nwant %s", got, want)
	}

	send(runes("D"))
	if len(m.st.Drafts["u"]) != 0 {
		t.Error("D should delete the draft on the cursor line")
	}
}

func TestReviewThreadsShowUnderTheirLineAndOpenOnEnter(t *testing.T) {
	m := lineCommentModel(t, false)
	m.convs = map[string]*conversation{"u": {Viewer: "me", Threads: []thread{
		{Comments: []comment{{Author: "anna", Body: "general"}}},
		{ID: "T1", Path: "src/a.go", Side: "RIGHT", Line: 2, Comments: []comment{
			{Author: "oden", Body: "Why 2?\nmore text"}, {Author: "me", Body: "because"},
		}},
	}}}
	m.focus = focusDiff
	d := m.selectedDoc()
	notes := m.rowNotes(d, 3, 100) // "+var x = 2" is new line 2
	if len(notes) != 1 || ansi.Strip(notes[0]) != "   💬 oden: Why 2?  (+1)" {
		t.Fatalf("thread note: %q", notes)
	}
	if len(m.rowNotes(d, 2, 100)) != 0 {
		t.Error("a RIGHT-side thread must not show on the removed (old) line 2")
	}
	m.cursor = 3
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(model)
	if m.focus != focusComments || m.convCur != 1 {
		t.Errorf("enter should open that thread in the comments pane: focus=%d thread=%d", m.focus, m.convCur)
	}
}

func TestHeaderLookalikesInsideHunksAreContent(t *testing.T) {
	f := parseDiff(`diff --git a/q.sql b/q.sql
--- a/q.sql
+++ b/q.sql
@@ -1,2 +1,2 @@
--- old comment
+++ b/not-a-path
 select 1;
`)[0]
	if f.Path != "q.sql" || f.Add != 1 || f.Del != 1 {
		t.Errorf("got path=%q +%d -%d, want q.sql +1 -1", f.Path, f.Add, f.Del)
	}

	deleted := &FileDiff{Path: "Gone.cs"}
	if got := ansi.Strip(deleted.hunkHeaderLines("@@ -1,11 +0,0 @@", true, 40)[1]); got != "│ 1: │" {
		t.Errorf("a deleted file's hunk should show its old start line, got %q", got)
	}
}

func TestWindowsLineEndingsAndControlCharsDontReachTheTerminal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	raw := "diff --git a/viewNode.less b/viewNode.less\n--- a/viewNode.less\n+++ b/viewNode.less\n" +
		"@@ -1,3 +0,0 @@\n-\ufeffdiv#btnAddPeriod {\r\n-    cursor: pointer;\r\n-    color: \x1b[31mred;\r\n"
	f := parseDiff(raw)[0]
	for _, l := range f.Body() {
		if strings.ContainsAny(l, "\r\x1b\ufeff") {
			t.Errorf("control characters left in %q", l)
		}
	}
	if got := f.Body()[3]; got != "-    color: \ufffd[31mred;" {
		t.Errorf("an escape code in a file should show as �, got %q", got)
	}

	pr := PR{URL: "u"}
	m := newModel(time.Minute, "", false)
	m.fetching = false
	m.prs = []PR{pr}
	m.diffs["u"] = &diffEntry{files: []*FileDiff{f}}
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 16})
	m = nm.(model)
	m.focus, m.fileCur = focusDiff, 1
	for _, split := range []bool{false, true} {
		m.split = split
		v := m.View()
		if strings.Contains(v, "\r") {
			t.Fatalf("split=%v: a carriage return reached the screen", split)
		}
		for i, l := range strings.Split(v, "\n") {
			if w := ansi.StringWidth(l); w != 90 {
				t.Errorf("split=%v line %d: width %d", split, i, w)
			}
		}
	}
}

func TestReconcileNotifiesOnlyNewPRsAfterFirstRun(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pr := func(url string) PR { return PR{URL: url} }

	fresh, _, err := reconcile([]PR{pr("a"), pr("b")})
	if err != nil || len(fresh) != 0 {
		t.Fatalf("first run should seed silently, got %v err=%v", fresh, err)
	}

	fresh, _, _ = reconcile([]PR{pr("a"), pr("b"), pr("c")})
	if len(fresh) != 1 || fresh[0].URL != "c" {
		t.Fatalf("want only c to be new, got %v", fresh)
	}

	fresh, _, _ = reconcile([]PR{pr("a"), pr("b"), pr("c")})
	if len(fresh) != 0 {
		t.Fatalf("already-notified PRs must not notify again, got %v", fresh)
	}

	// b drops out (reviewed) and is re-requested later: notify again.
	reconcile([]PR{pr("a"), pr("c")})
	fresh, _, _ = reconcile([]PR{pr("a"), pr("b"), pr("c")})
	if len(fresh) != 1 || fresh[0].URL != "b" {
		t.Fatalf("re-requested PR should notify again, got %v", fresh)
	}
}
