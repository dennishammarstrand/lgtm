package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// candidate is a user or team that can be requested as a reviewer.
type candidate struct {
	value string // what `gh pr edit --add-reviewer` takes: login, org/team or @copilot
	label string
	note  string
}

type reviewerPicker struct {
	prs        []PR
	input      textinput.Model
	query      string // the query the current results belong to
	results    []candidate
	loading    bool
	err        string
	cursor     int
	selected   map[string]bool
	order      []candidate
	submitting bool
}

type (
	pickerResultsMsg struct {
		key, query string
		cands      []candidate
		err        error
	}
	pickerSearchMsg  struct{ key, query string }
	reviewersDoneMsg struct {
		prs    []PR
		names  []string
		failed map[string]error // by PR URL
	}
)

const pickerVisible = 10

const suggestQuery = `query($owner: String!, $name: String!, $number: Int!) {
  viewer { login }
  repository(owner: $owner, name: $name) { pullRequest(number: $number) {
    author { login }
    suggestedReviewers { reviewer { login name } }
  } }
}`

const searchQuery = `query($owner: String!, $name: String!, $number: Int!, $q: String!) {
  viewer { login }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) { author { login } }
    assignableUsers(first: 15, query: $q) { nodes { login name } }
  }
  repositoryOwner(login: $owner) { ... on Organization { teams(first: 10, query: $q) { nodes { slug name } } } }
}`

func fetchCandidates(ctx context.Context, pr PR, q string) ([]candidate, error) {
	owner, name, _ := strings.Cut(pr.Repository.NameWithOwner, "/")
	query := suggestQuery
	args := []string{"api", "graphql", "-f", "owner=" + owner, "-f", "name=" + name, "-F", fmt.Sprintf("number=%d", pr.Number)}
	if q != "" {
		query = searchQuery
		args = append(args, "-f", "q="+q)
	}
	out, err := gh(ctx, append(args, "-f", "query="+query)...)
	if err != nil {
		return nil, err
	}
	type user struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	var resp struct {
		Data struct {
			Viewer     user `json:"viewer"`
			Repository struct {
				PullRequest struct {
					Author             user `json:"author"`
					SuggestedReviewers []struct {
						Reviewer user `json:"reviewer"`
					} `json:"suggestedReviewers"`
				} `json:"pullRequest"`
				AssignableUsers struct {
					Nodes []user `json:"nodes"`
				} `json:"assignableUsers"`
			} `json:"repository"`
			RepositoryOwner struct {
				Teams struct {
					Nodes []struct {
						Slug string `json:"slug"`
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"teams"`
			} `json:"repositoryOwner"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}
	d := resp.Data
	users := d.Repository.AssignableUsers.Nodes
	for _, s := range d.Repository.PullRequest.SuggestedReviewers {
		users = append(users, s.Reviewer)
	}
	var cands []candidate
	for _, u := range users {
		// You can't review your own PR, and the author can't be a reviewer.
		if u.Login == "" || u.Login == d.Viewer.Login || u.Login == d.Repository.PullRequest.Author.Login {
			continue
		}
		c := candidate{value: u.Login, label: u.Login, note: u.Name}
		if strings.EqualFold(u.Login, "copilot") {
			c = candidate{value: "@copilot", label: "Copilot", note: "AI review"}
		}
		cands = append(cands, c)
	}
	for _, t := range d.RepositoryOwner.Teams.Nodes {
		cands = append(cands, candidate{value: owner + "/" + t.Slug, label: owner + "/" + t.Slug, note: "team · " + t.Name})
	}
	return cands, nil
}

// reviewerValue is what `gh pr edit --add-reviewer` takes for a reviewer name.
func reviewerValue(name string) string {
	if strings.EqualFold(name, "copilot") {
		return "@copilot"
	}
	return name
}

// key identifies the PRs a picker is for, to drop results meant for an older set.
func (p *reviewerPicker) key() string {
	var urls []string
	for _, pr := range p.prs {
		urls = append(urls, pr.URL)
	}
	return strings.Join(urls, ",")
}

func newReviewerPicker(prs []PR, width int) (*reviewerPicker, tea.Cmd) {
	in := textinput.New()
	in.Prompt = "› "
	in.Placeholder = "search people or teams…"
	in.Width = max(width-8, 10)
	focus := in.Focus()
	p := &reviewerPicker{prs: prs, input: in, loading: true, selected: map[string]bool{}}
	return p, tea.Batch(focus, p.search(""))
}

// search fetches candidates for every PR: each PR's suggestions when q is empty,
// otherwise people and teams matching q in each repo involved. Results are merged.
func (p *reviewerPicker) search(q string) tea.Cmd {
	var prs []PR
	seenRepo := map[string]bool{}
	for _, pr := range p.prs {
		// Suggestions are per PR, but a search only needs one query per repo.
		if q == "" || !seenRepo[pr.Repository.NameWithOwner] {
			seenRepo[pr.Repository.NameWithOwner] = true
			prs = append(prs, pr)
		}
	}
	key := p.key()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		results := make([][]candidate, len(prs))
		errs := make([]error, len(prs))
		var wg sync.WaitGroup
		for i, pr := range prs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = fetchCandidates(ctx, pr, q)
			}()
		}
		wg.Wait()
		var cands []candidate
		seen := map[string]bool{}
		var firstErr error
		for i := range prs {
			if errs[i] != nil && firstErr == nil {
				firstErr = errs[i]
			}
			for _, c := range results[i] {
				if !seen[c.value] {
					seen[c.value] = true
					cands = append(cands, c)
				}
			}
		}
		if len(cands) > 0 {
			firstErr = nil // partial results are still useful
		}
		return pickerResultsMsg{key: key, query: q, cands: cands, err: firstErr}
	}
}

// items is what the list shows: current reviewers first (so they can be
// re-requested) when not searching, then the results.
func (p *reviewerPicker) items() []candidate {
	var items []candidate
	seen := map[string]bool{}
	add := func(c candidate) {
		if !seen[c.value] {
			seen[c.value] = true
			items = append(items, c)
		}
	}
	// For several PRs, note on how many each current reviewer already is.
	state, count := map[string]string{}, map[string]int{}
	for _, pr := range p.prs {
		for _, r := range pr.Reviewers {
			v := reviewerValue(r.Name)
			if prev, ok := state[v]; !ok || r.State == "PENDING" || prev != "PENDING" {
				state[v] = r.State
			}
			count[v]++
			if p.query == "" {
				add(candidate{value: v, label: r.Name})
			}
		}
	}
	for _, c := range p.results {
		add(c)
	}
	notes := map[string]string{
		"PENDING": "⏳ requested", "APPROVED": "✓ approved · re-request",
		"CHANGES_REQUESTED": "✗ changes requested · re-request", "COMMENTED": "💬 commented · re-request",
	}
	for i := range items {
		if n, ok := notes[state[items[i].value]]; ok {
			if len(p.prs) > 1 {
				n += fmt.Sprintf(" %d/%d", count[items[i].value], len(p.prs))
			}
			items[i].note = n
		}
	}
	return items
}

func (m model) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	p := m.picker
	switch msg := msg.(type) {
	case pickerResultsMsg:
		if msg.key != p.key() || msg.query != strings.TrimSpace(p.input.Value()) {
			return m, nil // stale
		}
		p.loading, p.query, p.results, p.err = false, msg.query, msg.cands, ""
		if msg.err != nil {
			p.err = msg.err.Error()
		}
		p.cursor = max(min(p.cursor, len(p.items())-1), 0)
		return m, nil
	case pickerSearchMsg:
		if msg.key == p.key() && msg.query == strings.TrimSpace(p.input.Value()) && msg.query != p.query {
			p.loading = true
			return m, p.search(msg.query)
		}
		return m, nil
	case reviewersDoneMsg:
		var failed []PR
		for _, pr := range msg.prs {
			if msg.failed[pr.URL] != nil {
				failed = append(failed, pr)
			} else {
				delete(m.selected, pr.URL)
			}
		}
		if len(failed) > 0 {
			// Keep the picker open with only the failed PRs, so ctrl+s retries them.
			p.submitting, p.prs = false, failed
			status := fmt.Sprintf("%s failed: %v", failed[0].Ref(), msg.failed[failed[0].URL])
			if len(msg.prs) > 1 {
				status = fmt.Sprintf("Requested on %d of %d; %s", len(msg.prs)-len(failed), len(msg.prs), status)
			}
			m.setStatus(status, true)
			if len(failed) == len(msg.prs) {
				return m, nil
			}
		} else {
			m.picker = nil
			m.setStatus("✓ Requested review from "+strings.Join(msg.names, ", ")+" on "+prsLabel(msg.prs), false)
		}
		m.fetching = true
		return m, m.fetchPRs()
	case tea.KeyMsg:
		if p.submitting {
			return m, nil
		}
		items := p.items()
		switch msg.String() {
		case "esc":
			m.picker = nil
			return m, nil
		case "up", "ctrl+p", "ctrl+k":
			p.cursor = max(p.cursor-1, 0)
			return m, nil
		case "down", "ctrl+n", "ctrl+j":
			p.cursor = max(min(p.cursor+1, len(items)-1), 0)
			return m, nil
		case "tab", "enter":
			if p.cursor < len(items) {
				p.toggle(items[p.cursor])
			}
			return m, nil
		case "ctrl+s":
			if len(p.order) == 0 {
				m.setStatus("Select at least one reviewer with tab", true)
				return m, nil
			}
			p.submitting = true
			m.setStatus("", false)
			prs, order := p.prs, p.order
			return m, func() tea.Msg {
				var values, names []string
				for _, c := range order {
					values, names = append(values, c.value), append(names, c.label)
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				failed := map[string]error{}
				var mu sync.Mutex
				var wg sync.WaitGroup
				for _, pr := range prs {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if _, err := gh(ctx, "pr", "edit", pr.URL, "--add-reviewer", strings.Join(values, ",")); err != nil {
							mu.Lock()
							failed[pr.URL] = err
							mu.Unlock()
						}
					}()
				}
				wg.Wait()
				return reviewersDoneMsg{prs: prs, names: names, failed: failed}
			}
		}
		before := p.input.Value()
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		if q := strings.TrimSpace(p.input.Value()); p.input.Value() != before {
			p.cursor = 0
			key := p.key()
			// Debounce: only search once typing pauses.
			return m, tea.Batch(cmd, tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg {
				return pickerSearchMsg{key: key, query: q}
			}))
		}
		return m, cmd
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return m, cmd
}

func (p *reviewerPicker) toggle(c candidate) {
	if p.selected[c.value] {
		delete(p.selected, c.value)
		for i, o := range p.order {
			if o.value == c.value {
				p.order = append(p.order[:i], p.order[i+1:]...)
				break
			}
		}
		return
	}
	p.selected[c.value] = true
	p.order = append(p.order, c)
}

func (m model) viewPicker() string {
	p := m.picker
	w := m.modalWidth()
	inner := w - 4
	lines := []string{selStyle.Render("Request reviewers for " + prsLabel(p.prs))}
	if len(p.prs) == 1 {
		lines = append(lines, dimStyle.Render(ansi.Truncate(p.prs[0].Title, inner, "…")))
	} else {
		const shown = 5
		for i, pr := range p.prs {
			if i == shown {
				lines = append(lines, dimStyle.Render(fmt.Sprintf("  … and %d more", len(p.prs)-shown)))
				break
			}
			lines = append(lines, ansi.Truncate("  • "+pr.Ref()+dimStyle.Render("  "+pr.Title), inner, "…"))
		}
	}
	lines = append(lines, "", p.input.View(), "")
	items := p.items()
	switch {
	case p.err != "":
		lines = append(lines, errStyle.Render(ansi.Truncate(p.err, inner, "…")))
	case len(items) == 0 && p.loading:
		lines = append(lines, dimStyle.Render("Loading…"))
	case len(items) == 0:
		lines = append(lines, dimStyle.Render("No matches"))
	}
	start, end := window(p.cursor, len(items), pickerVisible)
	for i := start; i < end; i++ {
		c := items[i]
		box := "[ ] "
		if p.selected[c.value] {
			box = okStyle.Render("[x] ")
		}
		marker, label := "  ", c.label
		if i == p.cursor {
			marker, label = selStyle.Render("› "), selStyle.Render(label)
		}
		line := marker + box + label
		if c.note != "" {
			line += "  " + dimStyle.Render(c.note)
		}
		lines = append(lines, ansi.Truncate(line, inner, "…"))
	}
	for i := end - start; i < pickerVisible; i++ {
		lines = append(lines, "")
	}
	if p.loading && len(items) > 0 {
		lines[len(lines)-1] = dimStyle.Render("Searching…")
	}
	var names []string
	for _, c := range p.order {
		names = append(names, c.label)
	}
	selected := dimStyle.Render("Selected: none")
	if len(names) > 0 {
		selected = "Selected: " + okStyle.Render(strings.Join(names, ", "))
	}
	hint := "tab select · ↑/↓ or ctrl+j/k move · ctrl+s request · esc cancel"
	if p.submitting {
		hint = "Requesting…"
	}
	lines = append(lines, "", ansi.Truncate(selected, inner, "…"), dimStyle.Render(ansi.Truncate(hint, inner, "…")))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).
		Padding(0, 1).Width(w - 2).Render(strings.Join(lines, "\n"))
}
