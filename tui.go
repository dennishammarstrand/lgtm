package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type focus int

const (
	focusPRs focus = iota
	focusComments
	focusFiles
	focusDiff
)

const panStep = 8

var (
	accent    = lipgloss.Color("5")
	dimColor  = lipgloss.Color("8")
	dimStyle  = lipgloss.NewStyle().Foreground(dimColor)
	boldStyle = lipgloss.NewStyle().Bold(true)
	selStyle  = lipgloss.NewStyle().Bold(true).Foreground(accent)
	addStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	delStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	hunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	okStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	newDot    = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("●")
)

type diffEntry struct {
	updatedAt time.Time
	files     []*FileDiff
	err       error
	loading   bool
	// refreshing keeps the current files on screen while a newer diff loads.
	refreshing bool
	tree       *treeNode
	head       string // head commit the diff was taken at; inline comments anchor to it
}

type reviewModal struct {
	kind       reviewKind
	prs        []PR
	replyTo    string // review thread ID for reviewReply ("" = PR conversation)
	replyLabel string
	// reviewInline: where the comment goes, the lines it is about, and the draft
	// it edits (-1 for a new one).
	target     lineTarget
	quote      []string
	editDraft  int
	commit     string
	ta         textarea.Model
	submitting bool
}

type model struct {
	interval time.Duration
	query    string
	showMine bool

	prs     []PR
	prCur   int
	fileCur int
	diffY   int
	diffX   int
	// Line cursor and visual selection in the diff pane.
	cursor     int
	visual     bool
	visualFrom int
	side       byte // 'R' new / 'L' old: which side comments go to in side-by-side
	split      bool
	wrap       bool
	diffs      map[string]*diffEntry
	focus      focus
	// collapsed holds collapsed directories, keyed by PR URL + "\x00" + path.
	collapsed map[string]bool
	st        *state
	modal     *reviewModal
	selected  map[string]bool // PR URLs selected for a bulk review
	convs     map[string]*conversation
	convCur   int // selected thread in the comments pane
	convY     int
	// While in a PR's comments, NEW badges use the read marker from before you
	// entered, so they don't vanish the moment you look.
	convSeenURL string
	convSeenAt  time.Time
	picker      *reviewerPicker
	help        bool
	pendG       bool
	width       int
	height      int

	fetching  bool
	fetchErr  error
	lastFetch time.Time
	lastCheck time.Time
	etag      string        // notifications ETag from the last cheap check
	pollEvery time.Duration // GitHub's X-Poll-Interval
	refetch   bool          // notifications changed while a fetch was in flight
	status    string
	statusErr bool

	latestVersion   string
	versionChecked  time.Time
	versionChecking bool
	confirmUpdate   bool     // waiting for y after U
	runAfterQuit    []string // update command main runs once the TUI has exited
}

type (
	tickMsg  struct{}
	notifMsg struct {
		res notifResult
		err error
	}
	loadDiffMsg struct{ url string }
	prsMsg      struct {
		prs, fresh []PR
		mine       []PR
		convs      map[string]*conversation // nil if they couldn't be fetched
		replies    []replyNote
		st         *state
		err        error
	}
	diffMsg struct {
		url       string
		updatedAt time.Time
		files     []*FileDiff
		head      string
		err       error
	}
	reviewDoneMsg struct {
		prs    []PR
		kind   reviewKind
		failed map[string]error // by PR URL
	}
)

func newModel(interval time.Duration, query string, showMine bool) model {
	st, _ := loadState()
	return model{side: 'R', interval: interval, query: query, showMine: showMine, diffs: map[string]*diffEntry{}, st: st,
		collapsed: map[string]bool{}, selected: map[string]bool{}, split: st.SplitDiff, wrap: !st.NoWrap, fetching: true,
		latestVersion: st.LatestVersion, versionChecked: st.VersionChecked}
}

func (m model) Init() tea.Cmd { return tea.Batch(m.fetchPRs(), m.checkNotifs(), m.checkVersion()) }

func (m model) tick() tea.Cmd {
	return tea.Tick(max(m.interval, m.pollEvery), func(time.Time) tea.Msg { return tickMsg{} })
}

func (m model) checkNotifs() tea.Cmd {
	etag := m.etag
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, err := checkNotifications(ctx, etag)
		return notifMsg{res: res, err: err}
	}
}

func (m model) fetchPRs() tea.Cmd {
	query, showMine := m.query, m.showMine
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		prs, err := fetchWatched(ctx, query)
		if err != nil {
			return prsMsg{err: err}
		}
		var mine []PR
		if showMine {
			if mine, err = fetchMine(ctx); err != nil {
				return prsMsg{err: err}
			}
		}
		fresh, st, _ := reconcile(prs)
		all := append(append([]PR{}, prs...), mine...)
		convs, cerr := fetchConversations(ctx, all)
		var replies []replyNote
		if cerr == nil {
			replies, st = reconcileReplies(all, convs)
		}
		return prsMsg{prs: prs, mine: mine, fresh: fresh, convs: convs, replies: replies, st: st}
	}
}

// ---- selection helpers ----

func (m *model) selectedPR() (PR, bool) {
	if m.prCur < 0 || m.prCur >= len(m.prs) {
		return PR{}, false
	}
	return m.prs[m.prCur], true
}

func (m *model) selectedEntry() *diffEntry {
	pr, ok := m.selectedPR()
	if !ok {
		return nil
	}
	return m.diffs[pr.URL]
}

// treeRows are the visible rows of the selected PR's changed-files tree.
func (m *model) treeRows() []treeRow {
	e := m.selectedEntry()
	if e == nil || len(e.files) == 0 {
		return nil
	}
	if e.tree == nil {
		e.tree = buildTree(e.files)
	}
	pr, _ := m.selectedPR()
	return e.tree.rows(func(path string) bool { return m.collapsed[pr.URL+"\x00"+path] })
}

func (m *model) selectedNode() *treeNode {
	rows := m.treeRows()
	if m.fileCur < 0 || m.fileCur >= len(rows) {
		return nil
	}
	return rows[m.fileCur].node
}

// selectedDoc is what the diff pane shows: the selected file, or every file under
// the selected directory.
func (m *model) selectedDoc() *diffDoc {
	n := m.selectedNode()
	switch {
	case n == nil:
		return nil
	case n.file != nil:
		return newDoc([]*FileDiff{n.file}, false, m.split)
	case len(n.files) > 0:
		return newDoc(n.files, true, m.split)
	}
	return nil
}

// ensureDiff loads the selected PR's diff unless an up-to-date copy is cached.
func (m *model) ensureDiff() tea.Cmd {
	pr, ok := m.selectedPR()
	if !ok {
		return nil
	}
	if e, ok := m.diffs[pr.URL]; ok && (e.loading || e.updatedAt.Equal(pr.UpdatedAt)) {
		return nil
	}
	m.diffs[pr.URL] = &diffEntry{loading: true, updatedAt: pr.UpdatedAt}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return loadDiff(ctx, pr)
	}
}

// loadDiff fetches a PR's diff and, alongside it, the head commit it belongs to.
func loadDiff(ctx context.Context, pr PR) diffMsg {
	head := make(chan string, 1)
	go func() { head <- fetchHead(ctx, pr.URL) }()
	files, err := fetchDiff(ctx, pr.URL)
	return diffMsg{url: pr.URL, updatedAt: pr.UpdatedAt, files: files, head: <-head, err: err}
}

// refreshDiff re-fetches the selected PR's diff, keeping the current one visible.
func (m *model) refreshDiff() tea.Cmd {
	pr, ok := m.selectedPR()
	if !ok {
		return nil
	}
	e, ok := m.diffs[pr.URL]
	if !ok || e.loading || e.err != nil {
		delete(m.diffs, pr.URL)
		return m.ensureDiff()
	}
	if e.refreshing {
		return nil
	}
	e.refreshing = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return loadDiff(ctx, pr)
	}
}

// ---- layout ----

func (m model) bodyHeight() int { return max(m.height-2, 5) }

// listRows is the number of content rows inside a column (box minus border and title).
func (m model) listRows() int { return m.bodyHeight() - 3 }

func (m model) columnWidths() (prW, fileW, diffW int) {
	if m.focus == focusPRs || m.focus == focusComments {
		return m.prListWidth(), 0, 0 // the rest is the comments pane
	}
	// Files and diff share one layout so moving between them doesn't reflow.
	fileW = max(m.width*25/100, min(30, m.width/2))
	return 0, fileW, m.width - fileW
}

func (m model) commentsWidth() int {
	if m.focus == focusPRs || m.focus == focusComments {
		return m.width - m.prListWidth()
	}
	return 0
}

// prListWidth gives the PR list 70% of the PRs | Comments screen.
func (m model) prListWidth() int { return m.width * 70 / 100 }

// panRange returns the longest line and the visible text width, for clamping pan.
func (m *model) panRange(d *diffDoc, diffW int) (longest, visW int) {
	visW = diffW - 5
	for i, f := range d.files {
		if m.split {
			sd := f.Split()
			_, tw := sd.layout(diffW - 4)
			if i == 0 || tw < visW {
				visW = tw
			}
			longest = max(longest, sd.longest)
			continue
		}
		for _, l := range f.Body() {
			longest = max(longest, len([]rune(l))-1)
		}
	}
	return longest, visW
}

// toggleSplit switches diff mode, keeping the same source lines at the top and
// under the cursor.
func (m *model) toggleSplit() {
	if d := m.selectedDoc(); d != nil && d.total > 0 {
		other := newDoc(d.files, d.headers, !m.split)
		mapRow := func(r int) int {
			fi, row := d.locate(r)
			if row >= 0 {
				rows := d.files[fi].Split().rows
				if m.split {
					if row < len(rows) {
						row = rows[row].src
					}
				} else {
					y := len(rows) - 1
					for i, sr := range rows {
						if sr.src >= row {
							y = i
							break
						}
					}
					row = max(y, 0)
				}
			}
			return other.docRow(fi, row)
		}
		m.diffY, m.cursor = mapRow(m.diffY), mapRow(min(m.cursor, d.total-1))
	}
	m.split = !m.split
	m.diffX, m.visual = 0, false
	saveDiffPrefs(m.split, m.wrap)
	if d := m.selectedDoc(); d != nil && d.total > 0 {
		m.snapCursor(d, 1)
	}
	m.clampDiff()
}

// ---- update ----

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	switch msg.(type) {
	case tea.KeyMsg, tea.FocusMsg, tea.WindowSizeMsg:
		// Some terminals show the cursor again (e.g. after switching tabs or panes),
		// which then flickers as the screen redraws; keep re-hiding it.
		cmd = tea.Batch(cmd, tea.HideCursor)
	}
	return next, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.modal != nil {
			m.modal.ta.SetWidth(m.modalWidth() - 4)
		}
		m.clampDiff()
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.checkNotifs(), m.checkVersion())

	case versionMsg:
		return m.applyVersion(msg), nil

	case notifMsg:
		if msg.err != nil {
			// The cheap check isn't available: fall back to a full refresh per tick.
			if m.fetching {
				return m, m.tick()
			}
			m.fetching = true
			return m, tea.Batch(m.fetchPRs(), m.tick())
		}
		first := m.etag == "" // the startup fetch already covers this
		m.etag, m.lastCheck = msg.res.etag, time.Now()
		if msg.res.poll > 0 {
			m.pollEvery = msg.res.poll
		}
		changed := msg.res.changed && !first
		if m.fetching {
			m.refetch = m.refetch || changed
			return m, m.tick()
		}
		if changed || time.Since(m.lastFetch) >= fullRefreshEvery {
			m.fetching = true
			return m, tea.Batch(m.fetchPRs(), m.tick())
		}
		return m, m.tick()

	case prsMsg:
		return m.applyPRs(msg)

	case loadDiffMsg:
		if pr, ok := m.selectedPR(); ok && pr.URL == msg.url {
			return m, m.ensureDiff()
		}
		return m, nil

	case diffMsg:
		selPath, isSelected := "", false
		if pr, ok := m.selectedPR(); ok && pr.URL == msg.url {
			isSelected = true
			if n := m.selectedNode(); n != nil {
				selPath = n.path
			}
		}
		m.diffs[msg.url] = &diffEntry{updatedAt: msg.updatedAt, files: msg.files, head: msg.head, err: msg.err}
		if isSelected {
			// After a refresh, stay on the same file or directory.
			rows := m.treeRows()
			m.fileCur = max(min(m.fileCur, len(rows)-1), 0)
			for i, r := range rows {
				if r.node.path == selPath {
					m.fileCur = i
					break
				}
			}
		}
		if m.focus == focusDiff && m.selectedDoc() == nil {
			m.focus = focusFiles
		}
		m.clampDiff()
		return m, nil

	case reviewDoneMsg:
		past := map[reviewKind]string{reviewApprove: "Approved", reviewComment: "Commented on", reviewRequestChanges: "Requested changes on", reviewReply: "Replied on"}
		var failed []PR
		for _, pr := range msg.prs {
			if msg.failed[pr.URL] != nil {
				failed = append(failed, pr)
			} else {
				delete(m.selected, pr.URL)
			}
		}
		done := len(msg.prs) - len(failed)
		if msg.kind != reviewReply {
			var submitted []string
			for _, pr := range msg.prs {
				if msg.failed[pr.URL] == nil {
					submitted = append(submitted, pr.URL)
				}
			}
			m.st = clearDrafts(submitted) // they went out with the review
		}
		if len(failed) > 0 {
			// Keep the dialog open with only the failed PRs, so ctrl+s retries them.
			if m.modal != nil {
				m.modal.submitting, m.modal.prs = false, failed
			}
			status := fmt.Sprintf("%s failed: %v", failed[0].Ref(), msg.failed[failed[0].URL])
			if len(msg.prs) > 1 {
				status = fmt.Sprintf("%s %d of %d; %s", past[msg.kind], done, len(msg.prs), status)
			}
			m.setStatus(status, true)
			if done == 0 {
				return m, nil
			}
		} else {
			m.modal = nil
			m.setStatus("✓ "+past[msg.kind]+" "+prsLabel(msg.prs), false)
		}
		m.fetching = true
		return m, m.fetchPRs()

	case lineCommentDoneMsg:
		if msg.err != nil {
			if m.modal != nil {
				m.modal.submitting = false
			}
			m.setStatus(msg.err.Error(), true)
			return m, nil
		}
		m.modal, m.visual = nil, false
		m.setStatus("✓ Posted comment on "+msg.pr.Ref(), false)
		m.fetching = true
		return m, m.fetchPRs()

	case pickerResultsMsg, pickerSearchMsg, reviewersDoneMsg:
		if m.picker != nil {
			return m.updatePicker(msg)
		}
		return m, nil

	case tea.KeyMsg:
		if m.modal != nil {
			return m.updateModal(msg)
		}
		if m.picker != nil {
			return m.updatePicker(msg)
		}
		return m.handleKey(msg)
	}

	if m.picker != nil {
		return m.updatePicker(msg)
	}
	if m.modal != nil {
		var cmd tea.Cmd
		m.modal.ta, cmd = m.modal.ta.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) setStatus(s string, isErr bool) { m.status, m.statusErr = cleanText(s), isErr }

func (m model) applyPRs(msg prsMsg) (tea.Model, tea.Cmd) {
	m.fetching = false
	m.fetchErr = msg.err
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
		return m, nil
	}
	m.lastFetch = time.Now()
	var again tea.Cmd
	if m.refetch {
		m.refetch, m.fetching = false, true
		again = m.fetchPRs()
	}
	m.st = msg.st

	prevURL := ""
	if pr, ok := m.selectedPR(); ok {
		prevURL = pr.URL
	}
	m.prs = append(msg.prs, msg.mine...)
	if msg.convs != nil {
		m.convs = msg.convs
	}
	inList := map[string]bool{}
	for _, pr := range m.prs {
		inList[pr.URL] = true
	}
	for url := range m.selected {
		if !inList[url] {
			delete(m.selected, url)
		}
	}
	found := false
	for i, pr := range m.prs {
		if pr.URL == prevURL {
			m.prCur, found = i, true
			break
		}
	}
	if !found {
		// The selected PR disappeared (e.g. it was just reviewed): step back to the list.
		m.prCur = max(min(m.prCur, len(m.prs)-1), 0)
		m.focus = focusPRs
		m.fileCur, m.diffY, m.diffX = 0, 0, 0
	}

	current := map[string]bool{}
	for _, pr := range m.prs {
		current[pr.URL] = true
	}
	for url := range m.diffs {
		if !current[url] {
			delete(m.diffs, url)
		}
	}

	fresh, replies := msg.fresh, msg.replies
	notifyCmd := func() tea.Msg { notifyNew(fresh); notifyReplies(replies); return nil }
	return m, tea.Batch(notifyCmd, m.ensureDiff(), again)
}

func (m model) updateModal(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	md := m.modal
	switch k.String() {
	case "esc":
		if !md.submitting {
			m.modal = nil
		}
		return m, nil
	case "ctrl+s", "ctrl+o":
		if md.submitting {
			return m, nil
		}
		body := strings.TrimSpace(md.ta.Value())
		if md.kind == reviewInline {
			return m.submitLineComment(body, k.String() == "ctrl+o")
		}
		if k.String() == "ctrl+o" {
			break
		}
		drafts := map[string][]draft{}
		withDrafts := 0
		for _, pr := range md.prs {
			if ds := m.st.Drafts[pr.URL]; len(ds) > 0 && md.kind != reviewReply {
				drafts[pr.URL] = ds
				withDrafts++
			}
		}
		// Inline comments can carry a review on their own, as on GitHub.
		if body == "" && md.kind != reviewApprove && withDrafts < len(md.prs) {
			m.setStatus("A comment is required", true)
			return m, nil
		}
		md.submitting = true
		m.setStatus("", false)
		prs, kind, replyTo := md.prs, md.kind, md.replyTo
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			failed := map[string]error{}
			var mu sync.Mutex
			var wg sync.WaitGroup
			for _, pr := range prs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					submit := func() error { return submitReview(ctx, pr.URL, kind, body) }
					switch {
					case kind == reviewReply:
						submit = func() error { return postReply(ctx, pr.URL, replyTo, body) }
					case len(drafts[pr.URL]) > 0:
						submit = func() error { return submitReviewWithDrafts(ctx, pr, kind, body, drafts[pr.URL]) }
					}
					if err := submit(); err != nil {
						mu.Lock()
						failed[pr.URL] = err
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			return reviewDoneMsg{prs: prs, kind: kind, failed: failed}
		}
	}
	if md.submitting {
		return m, nil
	}
	var cmd tea.Cmd
	md.ta, cmd = md.ta.Update(k)
	return m, cmd
}

// submitLineComment saves the inline comment as a draft, or with now posts it as a
// single comment straight away.
func (m model) submitLineComment(body string, now bool) (tea.Model, tea.Cmd) {
	md := m.modal
	if body == "" {
		m.setStatus("A comment is required", true)
		return m, nil
	}
	pr := md.prs[0]
	d := draft{lineTarget: md.target, Body: body, Commit: md.commit}
	if now {
		md.submitting = true
		m.setStatus("", false)
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return lineCommentDoneMsg{pr: pr, err: postLineComment(ctx, pr, d)}
		}
	}
	ds := append([]draft(nil), m.st.Drafts[pr.URL]...)
	if md.editDraft >= 0 && md.editDraft < len(ds) {
		ds[md.editDraft] = d
	} else {
		ds = append(ds, d)
	}
	m.st = saveDrafts(pr.URL, ds)
	m.modal, m.visual = nil, false
	m.setStatus(fmt.Sprintf("✎ Draft saved (%d on %s) · a / x submit the review with it, c from the other panes", len(ds), pr.Ref()), false)
	return m, nil
}

func (m model) modalWidth() int { return max(min(80, m.width-4), 20) }

// prsLabel names one PR by its ref, or several by count.
func prsLabel(prs []PR) string {
	if len(prs) == 1 {
		return prs[0].Ref()
	}
	return fmt.Sprintf("%d PRs", len(prs))
}

// reviewTargets is what a/c/x act on: the selected PRs when there is a selection,
// otherwise the PR under the cursor. Approve and request changes skip your own PRs.
func (m *model) reviewTargets(kind reviewKind, cur PR) (targets []PR, skipped int) {
	candidates := []PR{cur}
	if len(m.selected) > 0 {
		candidates = nil
		for _, pr := range m.prs {
			if m.selected[pr.URL] {
				candidates = append(candidates, pr)
			}
		}
	}
	for _, pr := range candidates {
		if pr.Mine && kind != reviewComment {
			skipped++
			continue
		}
		targets = append(targets, pr)
	}
	return targets, skipped
}

// openReply opens the dialog for replying to a thread in the comments pane.
func (m *model) openReply(pr PR, t thread) tea.Cmd {
	cmd := m.openModal(reviewReply, []PR{pr})
	m.modal.replyTo = t.ID
	label := "the PR conversation"
	if t.ID != "" {
		label = fmt.Sprintf("%s:%d", t.Path, t.Line)
	}
	// Name the last person who wrote (not you, not a bot) as the one you answer.
	last := t.Comments[len(t.Comments)-1]
	for i := len(t.Comments) - 1; i >= 0; i-- {
		if cm := t.Comments[i]; cm.Author != m.convs[pr.URL].Viewer && !cm.Bot {
			last = cm
			break
		}
	}
	first := strings.SplitN(strings.TrimSpace(htmlComment.ReplaceAllString(last.Body, "")), "\n", 2)[0]
	m.modal.replyLabel = label + " · ↳ " + last.Author + ": " + first
	return cmd
}

func (m *model) openModal(kind reviewKind, prs []PR) tea.Cmd {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetWidth(m.modalWidth() - 4)
	ta.SetHeight(8)
	if kind == reviewApprove {
		ta.Placeholder = "Optional message…"
	} else if kind == reviewReply {
		ta.Placeholder = "Write your reply…"
	} else if kind == reviewInline {
		ta.Placeholder = "Comment on these lines…"
	} else {
		ta.Placeholder = "Write your review comment…"
	}
	cmd := ta.Focus()
	m.modal = &reviewModal{kind: kind, prs: prs, ta: ta}
	return cmd
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	m.status = ""

	if m.help {
		if key == "?" || key == "q" || key == "esc" {
			m.help = false
		}
		return m, nil
	}

	if m.pendG {
		m.pendG = false
		if key == "g" {
			return m.jumpTop()
		}
	}
	if m.confirmUpdate {
		return m.answerUpdate(key)
	}

	switch key {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "U":
		return m.startUpdate()
	case "?":
		m.help = true
		return m, nil
	case "g":
		m.pendG = true
		return m, nil
	case "s":
		m.toggleSplit()
		return m, nil
	case "w":
		m.wrap = !m.wrap
		m.diffX = 0
		saveDiffPrefs(m.split, m.wrap)
		m.clampDiff()
		return m, nil
	case "r":
		// Refresh the PR list; from the files/diff columns also re-fetch this diff.
		m.fetching = true
		if m.focus == focusPRs || m.focus == focusComments {
			return m, m.fetchPRs()
		}
		return m, tea.Batch(m.fetchPRs(), m.refreshDiff())
	}

	if pr, ok := m.selectedPR(); ok {
		switch key {
		case "o":
			return m, func() tea.Msg { openURL(pr.URL); return nil }
		case "a", "x", "c":
			if key == "c" && m.focus == focusDiff {
				break // comment on the cursor lines, handled by keyDiff
			}
			kind := map[string]reviewKind{"a": reviewApprove, "x": reviewRequestChanges, "c": reviewComment}[key]
			targets, skipped := m.reviewTargets(kind, pr)
			if len(targets) == 0 {
				m.setStatus("GitHub doesn't let you approve or request changes on your own PR", true)
				return m, nil
			}
			if skipped > 0 {
				m.setStatus(fmt.Sprintf("Skipping %d of your own PRs", skipped), false)
			}
			return m, m.openModal(kind, targets)
		case "R":
			targets := []PR{pr}
			if len(m.selected) > 0 {
				targets = nil
				for _, sp := range m.prs {
					if m.selected[sp.URL] {
						targets = append(targets, sp)
					}
				}
			}
			p, cmd := newReviewerPicker(targets, m.modalWidth())
			m.picker = p
			return m, cmd
		}
	}

	switch m.focus {
	case focusPRs:
		return m.keyPRs(key)
	case focusComments:
		return m.keyComments(key)
	case focusFiles:
		return m.keyFiles(key)
	default:
		return m.keyDiff(key)
	}
}

// enterComments focuses the selected PR's comments and marks its replies read.
func (m *model) enterComments() {
	m.focus = focusComments
	pr, ok := m.selectedPR()
	if !ok {
		return
	}
	if m.convSeenURL != pr.URL {
		m.convSeenURL, m.convSeenAt = pr.URL, m.st.ReplySeen[pr.URL]
		// Start at the first thread with an unread reply.
		if c := m.convs[pr.URL]; c != nil && c.unseen(m.convSeenAt) > 0 {
			_, starts := c.render(m.commentsWidth()-4, 0, m.convSeenAt)
			for ti, t := range c.Threads {
				for i, r := range c.isReply(t) {
					if r && t.Comments[i].CreatedAt.After(m.convSeenAt) && m.convCur == 0 && m.convY == 0 {
						m.convCur, m.convY = ti, starts[ti]
					}
				}
			}
		}
	}
	if c := m.convs[pr.URL]; c != nil {
		if latest := c.latestReply(); latest.After(m.st.ReplySeen[pr.URL]) {
			m.st = markRepliesSeen(pr.URL, latest)
		}
	}
}

// seenFor is the read marker NEW badges compare against.
func (m *model) seenFor(url string) time.Time {
	if m.convSeenURL == url {
		return m.convSeenAt
	}
	return m.st.ReplySeen[url]
}

func (m model) keyComments(key string) (tea.Model, tea.Cmd) {
	pr, _ := m.selectedPR()
	c := m.convs[pr.URL]
	var starts []int
	total := 0
	if c != nil {
		lines, s := c.render(m.commentsWidth()-4, m.convCur, time.Time{})
		starts, total = s, len(lines)
	}
	rows := m.listRows()
	switch key {
	case "j", "down", "n":
		if m.convCur < len(starts)-1 {
			m.convCur++
			m.convY = starts[m.convCur]
		}
	case "k", "up", "N", "p":
		if m.convCur > 0 {
			m.convCur--
			m.convY = starts[m.convCur]
		}
	case "ctrl+d", "ctrl+f", " ":
		m.convY += rows / 2
	case "ctrl+u", "ctrl+b":
		m.convY -= rows / 2
	case "G":
		if len(starts) > 0 {
			m.convCur = len(starts) - 1
			m.convY = starts[m.convCur]
		}
	case "enter":
		if c != nil && m.convCur < len(c.Threads) {
			return m, m.openReply(pr, c.Threads[m.convCur])
		}
	case "l", "right":
		m.focus = focusFiles
		return m, m.ensureDiff()
	case "h", "left", "esc":
		m.focus = focusPRs
		m.convSeenURL = "" // back in the list, the replies count as read
		return m, nil
	}
	m.convY = max(min(m.convY, total-rows), 0)
	if strings.HasPrefix(key, "ctrl+") || key == " " {
		// After scrolling, the reply target is the thread at the top.
		for i, st := range starts {
			if st <= m.convY {
				m.convCur = i
			}
		}
	}
	return m, nil
}

func (m model) jumpTop() (tea.Model, tea.Cmd) {
	switch m.focus {
	case focusPRs:
		return m.movePR(-m.prCur)
	case focusComments:
		m.convCur, m.convY = 0, 0
	case focusFiles:
		m.moveFile(-m.fileCur)
	default:
		m.diffY = 0
	}
	return m, nil
}

func (m model) movePR(d int) (tea.Model, tea.Cmd) {
	next := max(min(m.prCur+d, len(m.prs)-1), 0)
	if next == m.prCur {
		return m, nil
	}
	m.prCur = next
	m.fileCur, m.diffY, m.diffX = 0, 0, 0
	m.convCur, m.convY, m.convSeenURL = 0, 0, ""
	url := m.prs[next].URL
	// Debounce so holding j doesn't spawn a `gh pr diff` per row.
	return m, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return loadDiffMsg{url: url} })
}

func (m *model) moveFile(d int) {
	rows := m.treeRows()
	next := max(min(m.fileCur+d, len(rows)-1), 0)
	if next != m.fileCur {
		m.fileCur = next
		m.diffY, m.diffX, m.cursor, m.visual = 0, 0, 0, false
	}
}

// moveToFile selects the next (d=1) or previous (d=-1) file row, skipping directories.
func (m *model) moveToFile(d int) {
	rows := m.treeRows()
	for i := m.fileCur + d; i >= 0 && i < len(rows); i += d {
		if rows[i].node.file != nil {
			m.fileCur = i
			m.diffY, m.diffX = 0, 0
			return
		}
	}
}

// toggleCollapse collapses or expands the selected directory.
func (m *model) toggleCollapse() {
	n := m.selectedNode()
	pr, ok := m.selectedPR()
	if n == nil || n.file != nil || !ok {
		return
	}
	key := pr.URL + "\x00" + n.path
	if m.collapsed[key] {
		delete(m.collapsed, key)
	} else {
		m.collapsed[key] = true
	}
}

// selectSameTitle selects every review request titled like the one under the cursor
// (the same change opened across repos), or deselects them if all already are.
func (m *model) selectSameTitle() {
	cur, ok := m.selectedPR()
	if !ok {
		return
	}
	var same []string
	all := true
	for _, pr := range m.prs {
		if pr.Mine == cur.Mine && strings.EqualFold(strings.TrimSpace(pr.Title), strings.TrimSpace(cur.Title)) {
			same = append(same, pr.URL)
			all = all && m.selected[pr.URL]
		}
	}
	for _, url := range same {
		if all {
			delete(m.selected, url)
		} else {
			m.selected[url] = true
		}
	}
}

func (m model) keyPRs(key string) (tea.Model, tea.Cmd) {
	half := max(m.listRows()/4, 1) // PR rows are two lines tall
	switch key {
	case "j", "down":
		return m.movePR(1)
	case "k", "up":
		return m.movePR(-1)
	case "ctrl+d":
		return m.movePR(half)
	case "ctrl+u":
		return m.movePR(-half)
	case "G":
		return m.movePR(len(m.prs))
	case " ":
		if pr, ok := m.selectedPR(); ok {
			if m.selected[pr.URL] {
				delete(m.selected, pr.URL)
			} else {
				m.selected[pr.URL] = true
			}
		}
		return m.movePR(1)
	case "*":
		m.selectSameTitle()
		return m, nil
	case "esc":
		clear(m.selected)
		return m, nil
	case "l", "right", "enter":
		pr, ok := m.selectedPR()
		if !ok {
			return m, nil
		}
		m.st = markViewed(pr.URL)
		m.enterComments()
		return m, m.ensureDiff() // prefetch the diff for the next step
	}
	return m, nil
}

func (m model) keyFiles(key string) (tea.Model, tea.Cmd) {
	half := max(m.listRows()/2, 1)
	switch key {
	case "j", "down":
		m.moveFile(1)
	case "k", "up":
		m.moveFile(-1)
	case "ctrl+d":
		m.moveFile(half)
	case "ctrl+u":
		m.moveFile(-half)
	case "G":
		m.moveFile(len(m.treeRows()))
	case " ":
		m.toggleCollapse()
	case "J", "K":
		// Scroll the diff next to the tree without leaving it.
		if key == "J" {
			m.diffY++
		} else {
			m.diffY--
		}
		m.clampDiff()
	case "l", "right", "enter":
		m.enterDiff()
	case "h", "left", "esc":
		m.enterComments()
	}
	return m, nil
}

func (m model) keyDiff(key string) (tea.Model, tea.Cmd) {
	d := m.selectedDoc()
	if d == nil || d.total == 0 {
		if key == "h" || key == "left" || key == "esc" {
			m.focus = focusFiles
		}
		return m, nil
	}
	page := max(m.listRows(), 1)
	hunks := d.hunks()
	// The first commentable row of the hunk starting at row h.
	hunkTop := func(h int) int {
		for r := h; r < d.total; r++ {
			if m.commentable(d, r) {
				return r
			}
		}
		return d.total - 1
	}
	switch key {
	case "j", "down":
		m.stepCursor(d, 1)
	case "k", "up":
		m.stepCursor(d, -1)
	case "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", " ":
		delta := map[string]int{"ctrl+d": page / 2, "ctrl+u": -page / 2, "ctrl+f": page, " ": page, "ctrl+b": -page}[key]
		m.cursor += delta
		m.snapCursor(d, delta)
	case "G":
		m.cursor = d.total - 1
		m.snapCursor(d, -1)
	case "n":
		for _, h := range hunks {
			if t := hunkTop(h); t > m.cursor {
				m.cursor = t
				break
			}
		}
	case "N", "p":
		for i := len(hunks) - 1; i >= 0; i-- {
			if t := hunkTop(hunks[i]); t < m.cursor {
				m.cursor = t
				break
			}
		}
	case "J", "K":
		if key == "J" {
			m.moveToFile(1)
		} else {
			m.moveToFile(-1)
		}
		m.visual = false
		if d = m.selectedDoc(); d == nil {
			return m, nil
		}
		m.snapCursor(d, 1)
	case "V":
		m.visual = !m.visual
		m.visualFrom = m.cursor
	case "tab":
		if m.split {
			m.side = map[byte]byte{'L': 'R', 'R': 'L'}[m.side]
		}
	case "c", "i", "S":
		return m, m.openLineComment(d, key == "S")
	case "D":
		m.deleteDraftsAt(d)
	case "enter":
		if ti := m.threadAt(d, m.cursor); ti >= 0 {
			m.enterComments()
			pr, _ := m.selectedPR()
			c := m.convs[pr.URL]
			_, starts := c.render(m.commentsWidth()-4, ti, m.seenFor(pr.URL))
			m.convCur, m.convY = ti, starts[ti]
			return m, nil
		}
	case "l", "right":
		if !m.wrap {
			m.diffX += panStep
		}
	case "h", "left":
		// Smart h: pan back first, then leave the diff once fully scrolled left.
		if m.diffX > 0 {
			m.diffX = max(m.diffX-panStep, 0)
		} else {
			m.focus, m.visual = focusFiles, false
		}
	case "0":
		m.diffX = 0
	case "esc":
		if m.visual {
			m.visual = false
		} else {
			m.focus = focusFiles
		}
	}
	if m.visual {
		// A multi-line comment can't leave the hunk it starts in.
		lo, hi := m.hunkBounds(d, m.visualFrom)
		m.cursor = max(min(m.cursor, hi), lo)
	}
	m.clampDiff()
	m.followCursor(d)
	m.clampDiff()
	return m, nil
}

// enterDiff focuses the diff with the cursor on the first line on screen.
func (m *model) enterDiff() {
	d := m.selectedDoc()
	if d == nil {
		return
	}
	m.focus, m.visual = focusDiff, false
	m.cursor = m.diffY
	m.snapCursor(d, 1)
	m.followCursor(d)
	m.clampDiff()
}

// openLineComment opens the inline comment dialog for the cursor line or selection;
// suggest pre-fills a GitHub suggestion with the selected new-side lines. On a line
// that already has a draft it edits that draft.
func (m *model) openLineComment(d *diffDoc, suggest bool) tea.Cmd {
	pr, ok := m.selectedPR()
	if !ok {
		return nil
	}
	t, err := m.commentTarget(d)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	body := ""
	if suggest {
		if t.Side != "RIGHT" || (t.StartSide != "" && t.StartSide != "RIGHT") {
			m.setStatus("Suggestions replace new code: put the cursor on added or unchanged lines (tab picks the new side)", true)
			return nil
		}
		body = "```suggestion\n" + strings.Join(m.selectedText(d, "RIGHT"), "\n") + "\n```\n"
	}
	edit := -1
	for i, dr := range m.st.Drafts[pr.URL] {
		if dr.lineTarget == t {
			edit = i
			if !suggest {
				body = dr.Body
			}
		}
	}
	cmd := m.openModal(reviewInline, []PR{pr})
	md := m.modal
	md.target, md.editDraft = t, edit
	md.quote = m.selectedText(d, t.Side)
	if e := m.selectedEntry(); e != nil {
		md.commit = e.head
	}
	if body != "" {
		md.ta.SetValue(body)
	}
	return cmd
}

// deleteDraftsAt removes the draft comments that end on the cursor line.
func (m *model) deleteDraftsAt(d *diffDoc) {
	pr, ok := m.selectedPR()
	fi, anchors := m.rowAnchors(d, m.cursor)
	if !ok || len(anchors) == 0 {
		return
	}
	var keep []draft
	removed := 0
	for _, dr := range m.st.Drafts[pr.URL] {
		hit := false
		for _, an := range anchors {
			hit = hit || (dr.Path == d.files[fi].Path && dr.Side == an.side && dr.Line == an.line)
		}
		if hit {
			removed++
		} else {
			keep = append(keep, dr)
		}
	}
	if removed == 0 {
		m.setStatus("No draft comment on this line", false)
		return
	}
	m.st = saveDrafts(pr.URL, keep)
	m.setStatus(fmt.Sprintf("Deleted %d draft comment%s", removed, plural(removed)), false)
}

func (m *model) clampDiff() {
	d := m.selectedDoc()
	if d == nil {
		m.diffY, m.diffX = 0, 0
		return
	}
	_, _, diffW := m.columnWidths()
	n := d.total
	longest, visW := m.panRange(d, diffW)
	rows := m.listRows()
	maxY := n - rows
	if diffW > 4 {
		// Lowest top row that still fills the screen, counting multi-line rows
		// (wrapped lines and boxed hunk headers).
		used, y := 0, n
		for y > 0 {
			h := len(m.diffRowLines(d, y-1, diffW-4))
			if used+h > rows {
				break
			}
			used += h
			y--
		}
		maxY = y
	}
	m.diffY = max(min(m.diffY, maxY), 0)
	if m.wrap {
		m.diffX = 0
	} else {
		m.diffX = max(min(m.diffX, longest-visW), 0)
	}
}

// ---- view ----

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	if m.width < 50 || m.height < 10 {
		return "Terminal too small for lgtm"
	}
	var body string
	switch {
	case m.help:
		body = lipgloss.Place(m.width, m.bodyHeight(), lipgloss.Center, lipgloss.Center, m.viewHelp())
	case m.modal != nil:
		body = lipgloss.Place(m.width, m.bodyHeight(), lipgloss.Center, lipgloss.Center, m.viewModal())
	case m.picker != nil:
		body = lipgloss.Place(m.width, m.bodyHeight(), lipgloss.Center, lipgloss.Center, m.viewPicker())
	default:
		body = m.viewColumns()
	}
	return displaySafe(lipgloss.JoinVertical(lipgloss.Left, m.viewHeader(), body, m.viewFooter()))
}

func (m model) viewHeader() string {
	toReview, reviewed, unread := 0, 0, 0
	for _, pr := range m.prs {
		if pr.Mine {
			continue
		}
		if pr.MyReview == "" {
			toReview++
		} else {
			reviewed++
		}
		if _, ok := m.st.Viewed[pr.URL]; !ok {
			unread++
		}
	}
	left := selStyle.Render(" lgtm ") + fmt.Sprintf(" %d review request%s", toReview, plural(toReview))
	pr, inPR := m.selectedPR()
	inPR = inPR && (m.focus == focusFiles || m.focus == focusDiff)
	if inPR {
		// The PR list is hidden here, so name the PR being reviewed instead.
		left = selStyle.Render(" lgtm ") + " " + boldStyle.Render(cleanText(pr.Title))
	}
	if reviewed > 0 && !inPR {
		left += fmt.Sprintf(" · %d reviewed, not approved", reviewed)
	}
	if unread > 0 && !inPR {
		left += fmt.Sprintf(" · %s %d new", newDot, unread)
	}
	newReplies := 0
	for _, pr := range m.prs {
		newReplies += m.newReplies(pr.URL)
	}
	if newReplies > 0 {
		left += " · " + newStyle.Render(fmt.Sprintf("💬 %d new repl%s", newReplies, map[bool]string{true: "y", false: "ies"}[newReplies == 1]))
	}
	if n := len(m.selected); n > 0 {
		left += " · " + okStyle.Render(fmt.Sprintf("✓ %d selected", n))
	}
	var right string
	switch {
	case m.fetching:
		right = dimStyle.Render("↻ refreshing… ")
	case m.fetchErr != nil:
		right = errStyle.Render("⚠ refresh failed ")
	case !m.lastFetch.IsZero():
		// A "nothing changed" check also means the list is current.
		updated := m.lastFetch
		if m.lastCheck.After(updated) {
			updated = m.lastCheck
		}
		right = dimStyle.Render("updated " + updated.Format("15:04") + " ")
	}
	if updateAvailable(releaseVersion(), m.latestVersion) {
		notice := newStyle.Render("↑ "+m.latestVersion+" available") + dimStyle.Render(" · U update") + " "
		if right != "" {
			notice += dimStyle.Render("· ")
		}
		right = notice + right
	}
	if inPR {
		meta := " " + dimStyle.Render(pr.Ref()+" · "+pr.Author.Login)
		// Keep the right side; shorten the title when the line is too long.
		room := m.width - lipgloss.Width(right) - lipgloss.Width(meta) - 1
		if lipgloss.Width(left) > room {
			left = ansi.Truncate(left, max(room, 0), "…")
		}
		left += meta
	}
	gap := max(m.width-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return ansi.Truncate(left+strings.Repeat(" ", gap)+right, m.width, "")
}

func (m model) viewFooter() string {
	if m.confirmUpdate {
		return m.footerLine(newStyle.Render(m.confirmUpdatePrompt()))
	}
	if m.status != "" {
		st := okStyle
		if m.statusErr {
			st = errStyle
		}
		return m.footerLine(st.Render(m.status))
	}
	if n := len(m.selected); n > 0 {
		hints := fmt.Sprintf("%d selected: a approve all · c comment all · x request changes on all · R reviewers for all · space toggle · * same title · esc clear · ? help", n)
		return m.footerLine(okStyle.Render(hints))
	}
	var hints string
	switch m.focus {
	case focusPRs:
		hints = "j/k move · l comments · space select · * select same title"
	case focusComments:
		hints = "j/k thread · enter reply · ctrl+d/u scroll · l files · h back"
	case focusFiles:
		hints = "j/k move · J/K scroll diff · space fold · l diff · h back"
	case focusDiff:
		if m.visual {
			hints = "VISUAL · j/k extend · c comment · S suggest · esc cancel"
			return m.footerLine(selStyle.Render(hints))
		}
		hints = "j/k line · V select · c comment · S suggest · D del draft · enter thread · tab side · n/N hunk · J/K file · s split · w wrap · h back · a approve · x changes"
		return m.footerLine(dimStyle.Render(hints + " · ? help"))
	}
	hints += " · a approve · c comment · x changes · R reviewers · o web · r refresh · ? help · q quit"
	return m.footerLine(dimStyle.Render(hints))
}

// footerLine shows the hints or status on the left and the version at the
// bottom right; the left side is shortened to make room.
func (m model) footerLine(text string) string {
	ver := dimStyle.Render(versionLabel()) + " "
	left := ansi.Truncate(" "+text, max(m.width-lipgloss.Width(ver)-1, 0), "…")
	gap := max(m.width-lipgloss.Width(left)-lipgloss.Width(ver), 1)
	return ansi.Truncate(left+strings.Repeat(" ", gap)+ver, m.width, "")
}

func (m model) viewColumns() string {
	prW, fileW, diffW := m.columnWidths()
	h := m.bodyHeight()
	var cols []string
	if prW > 0 {
		cols = append(cols, m.viewPRs(prW, h))
	}
	if cw := m.commentsWidth(); cw > 0 {
		cols = append(cols, m.viewComments(cw, h))
	}
	if fileW > 0 {
		cols = append(cols, m.viewFiles(fileW, h))
	}
	if diffW > 0 {
		cols = append(cols, m.viewDiff(diffW, h))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols...)
}

// scroll describes which part of a list is visible, for the scrollbar.
type scroll struct{ top, visible, total int }

// thumb returns the scrollbar thumb's first row and size on a track of n rows;
// size 0 means everything fits and no scrollbar is drawn.
func (sc scroll) thumb(n int) (start, size int) {
	if sc.total <= sc.visible || sc.total <= 0 || n <= 0 {
		return 0, 0
	}
	size = min(max(n*sc.visible/sc.total, 1), n)
	if maxTop := sc.total - sc.visible; maxTop > 0 {
		start = ((n-size)*sc.top + maxTop/2) / maxTop
	}
	return min(start, n-size), size
}

func box(title string, lines []string, w, h int, focused bool, sc scroll) string {
	innerW, innerH := max(w-4, 1), max(h-2, 1)
	border, titleStyle := dimColor, boldStyle
	if focused {
		border, titleStyle = accent, selStyle
	}
	out := []string{ansi.Truncate(titleStyle.Render(title), innerW, "…")}
	for _, l := range lines {
		if len(out) == innerH {
			break
		}
		out = append(out, ansi.Truncate(l, innerW, "…"))
	}
	for len(out) < innerH {
		out = append(out, "")
	}
	rendered := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderRight(false).BorderForeground(border).
		Padding(0, 1).Width(w - 2).Height(innerH).
		Render(strings.Join(out, "\n"))

	// Draw the right border ourselves so it can carry the scrollbar thumb.
	edge := lipgloss.NewStyle().Foreground(border)
	thumbStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	if focused {
		thumbStyle = selStyle
	}
	// The track skips the title row so the thumb lines up with the content.
	start, size := sc.thumb(innerH - 1)
	rows := strings.Split(rendered, "\n")
	for i := range rows {
		switch {
		case i == 0:
			rows[i] += edge.Render("╮")
		case i == len(rows)-1:
			rows[i] += edge.Render("╯")
		case i-2 >= start && i-2 < start+size:
			rows[i] += thumbStyle.Render("┃")
		default:
			rows[i] += edge.Render("│")
		}
	}
	return strings.Join(rows, "\n")
}

// window returns the visible slice bounds, keeping the cursor centred.
func window(cur, n, visible int) (int, int) {
	if visible <= 0 {
		return 0, 0
	}
	start := max(min(cur-visible/2, n-visible), 0)
	return start, min(n, start+visible)
}

func (m model) viewPRs(w, h int) string {
	if len(m.prs) == 0 && m.fetching {
		return box("Pull requests", []string{dimStyle.Render("Loading…")}, w, h, m.focus == focusPRs, scroll{})
	}
	title := "Pull requests"
	if m.fetching {
		title += dimStyle.Render("  ↻ refreshing…")
	}
	nReq, nMine := 0, 0
	for _, pr := range m.prs {
		if pr.Mine {
			nMine++
		} else {
			nReq++
		}
	}
	heading := func(title string, n int) string {
		return boldStyle.Render(title) + dimStyle.Render(fmt.Sprintf(" (%d)", n))
	}
	var lines []string
	// Each PR takes two rows; leave room for the section headings.
	start, end := window(m.prCur, len(m.prs), max((m.listRows()-4)/2, 1))
	if start == 0 {
		lines = append(lines, heading("Review requests", nReq))
		if nReq == 0 {
			lines = append(lines, dimStyle.Render("  No pending review requests 🎉"))
		}
	}
	for i := start; i < end; i++ {
		pr := m.prs[i]
		if pr.Mine && (i == 0 || !m.prs[i-1].Mine) {
			lines = append(lines, "", heading("My PRs", nMine))
		}
		dot := " "
		if _, ok := m.st.Viewed[pr.URL]; !ok && !pr.Mine {
			dot = newDot
		}
		if m.selected[pr.URL] {
			dot = okStyle.Render("✓")
		}
		marker, title := "  ", cleanText(pr.Title)
		if i == m.prCur {
			marker, title = selStyle.Render("› "), selStyle.Render(title)
		}
		meta := pr.Ref()
		if !pr.Mine {
			meta += " · " + pr.Author.Login
		}
		meta += " · " + age(pr.UpdatedAt)
		if pr.IsDraft {
			meta += " · draft"
		}
		switch pr.MyReview {
		case "COMMENTED":
			meta += " · 💬 commented"
		case "CHANGES_REQUESTED":
			meta += " · ✗ changes requested"
		case "":
		default:
			meta += " · reviewed"
		}
		metaLine := "    " + dimStyle.Render(meta)
		if pr.Mine {
			metaLine += dimStyle.Render(" · ") + reviewerSummary(pr.Reviewers)
		}
		if n := len(m.st.Drafts[pr.URL]); n > 0 {
			metaLine += " " + newStyle.Render(fmt.Sprintf("✎ %d draft%s", n, plural(n)))
		}
		if n := m.newReplies(pr.URL); n > 0 {
			metaLine += " " + newStyle.Render(fmt.Sprintf("💬 %d new repl%s", n, map[bool]string{true: "y", false: "ies"}[n == 1]))
		}
		lines = append(lines, marker+dot+" "+title, metaLine)
	}
	if m.showMine && nMine == 0 && end == len(m.prs) {
		lines = append(lines, "", heading("My PRs", 0), dimStyle.Render("  No open PRs"))
	}
	return box(title, lines, w, h, m.focus == focusPRs, scroll{start, end - start, len(m.prs)})
}

func (m model) viewComments(w, h int) string {
	focused := m.focus == focusComments
	pr, ok := m.selectedPR()
	if !ok {
		return box("Comments", nil, w, h, focused, scroll{})
	}
	c := m.convs[pr.URL]
	if c == nil || len(c.Threads) == 0 {
		msg := "No comments yet · c to comment"
		if c == nil && m.fetching {
			msg = "Loading…"
		}
		return box("Comments", []string{dimStyle.Render(msg)}, w, h, focused, scroll{})
	}
	seen := m.seenFor(pr.URL)
	cur := -1 // the selection bar only shows while the pane is focused
	if focused {
		cur = m.convCur
	}
	all, _ := c.render(w-4, cur, seen)
	rows := m.listRows()
	y := max(min(m.convY, len(all)-rows), 0)
	lines := all[y:min(y+rows, len(all))]
	title := fmt.Sprintf("Comments (%d thread%s)", len(c.Threads), plural(len(c.Threads)))
	if n := c.unseen(seen); n > 0 {
		title += " " + newStyle.Render(fmt.Sprintf("%d new", n))
	}
	return box(title, lines, w, h, focused, scroll{y, len(lines), len(all)})
}

// newReplies counts unread replies to your comments on a PR.
func (m *model) newReplies(url string) int {
	if c := m.convs[url]; c != nil {
		return c.unseen(m.seenFor(url))
	}
	return 0
}

func (m model) viewFiles(w, h int) string {
	title := "Files"
	var lines []string
	var sc scroll
	e := m.selectedEntry()
	switch {
	case len(m.prs) == 0:
	case e == nil || e.loading:
		lines = []string{dimStyle.Render("Loading diff…")}
	case e.err != nil:
		lines = []string{errStyle.Render(e.err.Error())}
	default:
		title = fmt.Sprintf("Files (%d)", len(e.files))
		if pr, ok := m.selectedPR(); ok && m.focus != focusPRs {
			// The PR column is hidden here, so say which PR these files belong to.
			title = fmt.Sprintf("#%d · %s", pr.Number, title)
		}
		if e.refreshing {
			title += dimStyle.Render("  ↻ refreshing…")
		}
		pr, _ := m.selectedPR()
		rows := m.treeRows()
		start, end := window(m.fileCur, len(rows), m.listRows())
		sc = scroll{start, end - start, len(rows)}
		for i := start; i < end; i++ {
			collapsed := m.collapsed[pr.URL+"\x00"+rows[i].node.path]
			lines = append(lines, rows[i].render(w-4, i == m.fileCur, collapsed))
		}
	}
	return box(title, lines, w, h, m.focus == focusFiles, sc)
}

func (m model) viewDiff(w, h int) string {
	d := m.selectedDoc()
	if d == nil {
		return box("Diff", nil, w, h, m.focus == focusDiff, scroll{})
	}
	rows := m.listRows()
	n := d.total
	var lines []string
	last := m.diffY
	for i := m.diffY; i < n && len(lines) < rows; i++ {
		lines = append(lines, m.diffRowLines(d, i, w-4)...)
		last = i + 1
	}
	title := d.files[0].Path
	if d.headers {
		title = m.selectedNode().path
		if title == "" {
			title = "All changes"
		}
		title += fmt.Sprintf(" (%d files)", len(d.files))
	}
	pos := dimStyle.Render(fmt.Sprintf("  %d/%d", last, n))
	if m.diffX > 0 {
		pos += dimStyle.Render(fmt.Sprintf(" →%d", m.diffX))
	}
	return box(truncLeft(title, w-20)+pos, lines, w, h, m.focus == focusDiff, scroll{m.diffY, last - m.diffY, n})
}

// diffRowLines renders row r of the diff document in the current mode as one or
// more screen lines of the given width.
func (m *model) diffRowLines(d *diffDoc, r, width int) []string {
	fi, i := d.locate(r)
	f := d.files[fi]
	if i < 0 {
		return fileHeaderLines(f, r == 0, width)
	}
	var lines []string
	if m.split {
		lines = f.Split().rowLines(f, i, m.diffX, width, m.wrap, m.rowMark(r), m.side)
	} else {
		lines = unifiedRowLines(f, i, m.diffX, width, m.wrap, m.rowMark(r))
	}
	return append(lines, m.rowNotes(d, r, width)...)
}

func unifiedRowLines(f *FileDiff, i, x, width int, wrap bool, mark byte) []string {
	raw := f.Body()[i]
	switch {
	case f.firstHunk < 0, strings.HasPrefix(raw, `\`):
		return []string{dimStyle.Render(ansi.Truncate(ansi.TruncateLeft(raw, x, ""), width, "…"))}
	case strings.HasPrefix(raw, "@@"):
		return f.hunkHeaderLines(raw, i == 0, width)
	}
	// The +/- sign stays put; only the code pans or wraps.
	kind := lineKind(raw)
	fill := fillStyle(kind)
	sign := fill.Render(string(kind))
	switch kind {
	case '+':
		sign = addStyle.Background(addBg).Render("+")
	case '-':
		sign = delStyle.Background(delBg).Render("-")
	}
	if st, ok := markStyle(mark); ok {
		sign = st.Render(string(kind))
	}
	hl := f.Highlighted()
	if !wrap {
		return []string{sign + viewport(hl[i], x, width-1, fill)}
	}
	segs := wrapText(hl[i], width-1, fill)
	for k := range segs {
		if k == 0 {
			segs[k] = sign + segs[k]
		} else {
			segs[k] = fill.Render(" ") + segs[k]
		}
	}
	return segs
}

func (m model) viewModal() string {
	md := m.modal
	hint := "ctrl+s submit · esc cancel"
	if md.kind == reviewInline {
		hint = "ctrl+s add to review (draft) · ctrl+o post now as a single comment · esc cancel"
	}
	if md.submitting {
		hint = "Submitting…"
	}
	inner := m.modalWidth() - 4
	head := []string{selStyle.Render(md.kind.String() + " " + prsLabel(md.prs))}
	drafts := 0
	for _, pr := range md.prs {
		drafts += len(m.st.Drafts[pr.URL])
	}
	if md.kind == reviewInline {
		head[0] = selStyle.Render("Comment on " + truncLeft(md.target.String(), inner-11))
		const shown = 6
		for i, l := range md.quote {
			if i == shown {
				head = append(head, dimStyle.Render(fmt.Sprintf("  … %d more lines", len(md.quote)-shown)))
				break
			}
			head = append(head, dimStyle.Render(ansi.Truncate("  │ "+l, inner, "…")))
		}
	} else if md.kind != reviewReply && drafts > 0 {
		head[0] += newStyle.Render(fmt.Sprintf("  with %d inline comment%s", drafts, plural(drafts)))
	}
	switch {
	case md.kind == reviewInline: // the quoted lines are the context
	case md.kind == reviewReply:
		head = append(head, dimStyle.Render(ansi.Truncate(md.replyLabel, inner, "…")))
	case len(md.prs) == 1:
		head = append(head, dimStyle.Render(ansi.Truncate(md.prs[0].Title, inner, "…")))
	default:
		const shown = 8
		for i, pr := range md.prs {
			if i == shown {
				head = append(head, dimStyle.Render(fmt.Sprintf("  … and %d more", len(md.prs)-shown)))
				break
			}
			head = append(head, ansi.Truncate("  • "+pr.Ref()+dimStyle.Render("  "+pr.Title), inner, "…"))
		}
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		strings.Join(head, "\n"),
		"",
		md.ta.View(),
		"",
		dimStyle.Render(hint),
	)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).
		Padding(0, 1).Width(m.modalWidth() - 2).Render(content)
}

func (m model) viewHelp() string {
	rows := [][2]string{
		{"Navigation", ""},
		{"j / k", "move down / up (line cursor in diff)"},
		{"l / enter", "go deeper: PRs → comments → files → diff (in diff: pan right)"},
		{"h / esc", "go back (in diff: pan left first, then back)"},
		{"gg / G", "top / bottom"},
		{"ctrl+d / ctrl+u", "half page down / up"},
		{"space", "fold / unfold directory (files)"},
		{"J / K", "next / previous file (in diff); scroll the diff (in files)"},
		{"n / N", "next / previous hunk (in diff)"},
		{"0", "reset horizontal pan"},
		{"s", "toggle side-by-side / unified diff"},
		{"w", "toggle line wrap (h/l pan when off)"},
		{"", ""},
		{"Review", ""},
		{"a", "approve (optional message)"},
		{"c", "comment"},
		{"x", "request changes"},
		{"R", "request reviewers (people, teams, Copilot)"},
		{"enter", "reply to the selected thread (comments)"},
		{"space / *", "select PR / all PRs with the same title"},
		{"esc", "clear selection (a/c/x then act on all selected)"},
		{"", ""},
		{"Diff", ""},
		{"j / k", "move the line cursor"},
		{"V", "select lines (within one hunk); esc cancels"},
		{"c / i", "comment on the line(s): ctrl+s adds to review, ctrl+o posts now"},
		{"S", "suggest a change (GitHub suggestion block)"},
		{"D", "delete the draft comment on this line"},
		{"enter", "open the thread on this line in the comments pane"},
		{"tab", "comment on the old / new side (side-by-side)"},
		{"a / x", "submit the review, with all draft comments"},
		{"o", "open PR in browser"},
		{"", ""},
		{"r", "refresh now"},
		{"U", "update lgtm, when a new version is available"},
		{"? / q", "close help / quit"},
	}
	var b strings.Builder
	for _, r := range rows {
		switch {
		case r[1] == "" && r[0] != "":
			b.WriteString(selStyle.Render(r[0]) + "\n")
		default:
			b.WriteString(fmt.Sprintf("  %-17s %s\n", r[0], dimStyle.Render(r[1])))
		}
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).
		Padding(0, 2).Render(strings.TrimRight(b.String(), "\n"))
}

// ---- utils ----

func truncLeft(s string, w int) string {
	r := []rune(s)
	if w <= 1 || len(r) <= w {
		return s
	}
	return "…" + string(r[len(r)-w+1:])
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
