package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type comment struct {
	Author    string
	Body      string
	CreatedAt time.Time
	URL       string
	Tag       string // review verdict for review messages, e.g. "approved"
	Bot       bool
}

// thread is an inline review thread, or (ID "") the PR's general conversation:
// its comments plus review messages.
type thread struct {
	ID       string
	Path     string
	Line     int
	Side     string // LEFT (old) or RIGHT (new)
	Resolved bool
	Outdated bool
	DiffHunk string
	Comments []comment
}

type conversation struct {
	Viewer  string
	Threads []thread
}

// Lines shown per comment before it is cut off (long bot reviews, mostly).
const maxCommentLines = 15

// isReply reports which comments in t answer you: comments by someone else (not a
// bot) after your first comment in the thread.
func (c *conversation) isReply(t thread) []bool {
	out := make([]bool, len(t.Comments))
	mine := false
	for i, cm := range t.Comments {
		if cm.Author == c.Viewer {
			mine = true
			continue
		}
		out[i] = mine && !cm.Bot && !isBot(cm.Author)
	}
	return out
}

// replies returns every reply to you, across threads.
func (c *conversation) replies() []comment {
	var out []comment
	for _, t := range c.Threads {
		for i, r := range c.isReply(t) {
			if r {
				out = append(out, t.Comments[i])
			}
		}
	}
	return out
}

func (c *conversation) unseen(since time.Time) int {
	n := 0
	for _, r := range c.replies() {
		if r.CreatedAt.After(since) {
			n++
		}
	}
	return n
}

func (c *conversation) latestReply() time.Time {
	var t time.Time
	for _, r := range c.replies() {
		if r.CreatedAt.After(t) {
			t = r.CreatedAt
		}
	}
	return t
}

const convFields = `url
  reviewThreads(first: 50) { nodes { id isResolved isOutdated path line originalLine diffSide
    comments(first: 30) { nodes { author { __typename login } body createdAt url diffHunk } } } }
  comments(last: 50) { nodes { author { __typename login } body createdAt url } }
  reviews(last: 30) { nodes { author { __typename login } body createdAt url state } }`

type ghComment struct {
	Author *struct {
		Typename string `json:"__typename"`
		Login    string `json:"login"`
	} `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url"`
	DiffHunk  string    `json:"diffHunk"`
	State     string    `json:"state"`
}

func (g ghComment) toComment() comment {
	who := "ghost"
	if g.Author != nil && g.Author.Login != "" {
		who = g.Author.Login
	}
	tag := map[string]string{"APPROVED": "approved", "CHANGES_REQUESTED": "requested changes", "DISMISSED": "dismissed"}[g.State]
	bot := g.Author != nil && g.Author.Typename == "Bot"
	return comment{Author: who, Body: g.Body, CreatedAt: g.CreatedAt, URL: g.URL, Tag: tag, Bot: bot}
}

// fetchConversations loads the comment threads of the given PRs, batched into as
// few GraphQL calls as possible.
func fetchConversations(ctx context.Context, prs []PR) (map[string]*conversation, error) {
	out := map[string]*conversation{}
	const batch = 10
	for start := 0; start < len(prs); start += batch {
		chunk := prs[start:min(start+batch, len(prs))]
		var params, fields []string
		args := []string{"api", "graphql"}
		for i, pr := range chunk {
			params = append(params, fmt.Sprintf("$u%d: URI!", i))
			fields = append(fields, fmt.Sprintf("p%d: resource(url: $u%d) { ...conv }", i, i))
			args = append(args, "-f", fmt.Sprintf("u%d=%s", i, pr.URL))
		}
		q := fmt.Sprintf("query(%s) { viewer { login } %s } fragment conv on PullRequest { %s }",
			strings.Join(params, ", "), strings.Join(fields, " "), convFields)
		raw, err := gh(ctx, append(args, "-f", "query="+q)...)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("parse gh output: %w", err)
		}
		var viewer struct {
			Login string `json:"login"`
		}
		_ = json.Unmarshal(resp.Data["viewer"], &viewer)
		for i := range chunk {
			var p *struct {
				URL           string `json:"url"`
				ReviewThreads struct {
					Nodes []struct {
						ID           string `json:"id"`
						IsResolved   bool   `json:"isResolved"`
						IsOutdated   bool   `json:"isOutdated"`
						Path         string `json:"path"`
						Line         *int   `json:"line"`
						OriginalLine *int   `json:"originalLine"`
						DiffSide     string `json:"diffSide"`
						Comments     struct {
							Nodes []ghComment `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
				Comments struct {
					Nodes []ghComment `json:"nodes"`
				} `json:"comments"`
				Reviews struct {
					Nodes []ghComment `json:"nodes"`
				} `json:"reviews"`
			}
			if err := json.Unmarshal(resp.Data[fmt.Sprintf("p%d", i)], &p); err != nil || p == nil {
				continue
			}
			c := &conversation{Viewer: viewer.Login}
			for _, t := range p.ReviewThreads.Nodes {
				th := thread{ID: t.ID, Path: t.Path, Side: t.DiffSide, Resolved: t.IsResolved, Outdated: t.IsOutdated}
				if t.Line != nil {
					th.Line = *t.Line
				} else if t.OriginalLine != nil {
					th.Line = *t.OriginalLine
				}
				for j, cm := range t.Comments.Nodes {
					if j == 0 {
						th.DiffHunk = cm.DiffHunk
					}
					th.Comments = append(th.Comments, cm.toComment())
				}
				if len(th.Comments) > 0 {
					c.Threads = append(c.Threads, th)
				}
			}
			// The PR conversation skips bots (CI links, issue-tracker notices, AI
			// review summaries); their inline review comments above are kept.
			general := thread{}
			for _, g := range append(p.Comments.Nodes, p.Reviews.Nodes...) {
				if cm := g.toComment(); !cm.Bot && strings.TrimSpace(cm.Body) != "" {
					general.Comments = append(general.Comments, cm)
				}
			}
			sort.SliceStable(general.Comments, func(a, b int) bool {
				return general.Comments[a].CreatedAt.Before(general.Comments[b].CreatedAt)
			})
			if len(general.Comments) > 0 {
				c.Threads = append(c.Threads, general)
			}
			sortThreads(c)
			out[chunk[i].URL] = c
		}
	}
	return out, nil
}

// sortThreads puts the thread with the most recent comment first.
func sortThreads(c *conversation) {
	last := func(t thread) time.Time { return t.Comments[len(t.Comments)-1].CreatedAt }
	sort.SliceStable(c.Threads, func(i, j int) bool {
		return last(c.Threads[i]).After(last(c.Threads[j]))
	})
}

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	blankRuns   = regexp.MustCompile(`\n{3,}`)
	newStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
)

// wrapBody prepares a markdown comment body for the terminal: drops HTML comments,
// wraps to width and caps very long comments.
func wrapBody(body string, width int) []string {
	body = cleanText(body)
	body = strings.ReplaceAll(body, "\t", "    ")
	body = htmlComment.ReplaceAllString(body, "")
	body = strings.TrimSpace(blankRuns.ReplaceAllString(body, "\n\n"))
	if body == "" {
		return []string{dimStyle.Render("(no text)")}
	}
	wrapped := strings.Split(lipgloss.NewStyle().Width(max(width, 10)).Render(body), "\n")
	for i := range wrapped {
		wrapped[i] = strings.TrimRight(wrapped[i], " ")
	}
	if len(wrapped) > maxCommentLines {
		more := len(wrapped) - maxCommentLines
		wrapped = append(wrapped[:maxCommentLines], dimStyle.Render(fmt.Sprintf("… %d more lines (o opens the PR in the browser)", more)))
	}
	return wrapped
}

// render lays out every thread; starts holds each thread's first line. The
// selected thread gets a bar down its left side.
func (c *conversation) render(width, cur int, seen time.Time) (lines []string, starts []int) {
	for ti, t := range c.Threads {
		starts = append(starts, len(lines))
		var tl []string
		title, tags := "💬 Conversation", ""
		if t.ID != "" {
			title = t.Path
			if t.Line > 0 {
				title += fmt.Sprintf(":%d", t.Line)
			}
		}
		if t.Resolved {
			tags += dimStyle.Render(" · ✓ resolved")
		}
		if t.Outdated {
			tags += dimStyle.Render(" · outdated")
		}
		tl = append(tl, hunkStyle.Bold(true).Render(ansi.Truncate(title, width-2, "…"))+tags)
		if t.DiffHunk != "" {
			hunk := strings.Split(strings.TrimRight(t.DiffHunk, "\n"), "\n")
			for _, l := range hunk[max(len(hunk)-3, 0):] {
				l = ansi.Truncate(strings.ReplaceAll(l, "\t", "    "), width-4, "…")
				switch lineKind(l) {
				case '+':
					l = addStyle.Render(l)
				case '-':
					l = delStyle.Render(l)
				default:
					l = dimStyle.Render(l)
				}
				tl = append(tl, "  "+l)
			}
		}
		replies := c.isReply(t)
		for i, cm := range t.Comments {
			who := cm.Author
			if who == c.Viewer {
				who = "you"
			}
			head := boldStyle.Render(who) + dimStyle.Render(" · "+age(cm.CreatedAt))
			if cm.Tag != "" {
				head += dimStyle.Render(" · " + cm.Tag)
			}
			if replies[i] && cm.CreatedAt.After(seen) {
				head += " " + newStyle.Render("NEW")
			}
			tl = append(tl, "", "  "+head)
			for _, l := range wrapBody(cm.Body, width-6) {
				tl = append(tl, "    "+l)
			}
		}
		marker := "  "
		if ti == cur {
			marker = selStyle.Render("▌ ")
		}
		for _, l := range tl {
			lines = append(lines, marker+l)
		}
		lines = append(lines, "")
	}
	return lines, starts
}

// replyNote is a new reply to one of your comments, for a notification.
type replyNote struct {
	pr      PR
	comment comment
}

// reconcileReplies records the latest reply per PR and returns replies newer than
// the last notified one. The first time a PR is seen, its existing replies are
// recorded silently (and count as read).
func reconcileReplies(prs []PR, convs map[string]*conversation) ([]replyNote, *state) {
	st, _ := loadState()
	var fresh []replyNote
	current := map[string]bool{}
	for _, pr := range prs {
		c, ok := convs[pr.URL]
		if !ok {
			continue
		}
		current[pr.URL] = true
		latest := c.latestReply()
		notified, seenBefore := st.ReplyNotified[pr.URL]
		if !seenBefore {
			st.ReplyNotified[pr.URL], st.ReplySeen[pr.URL] = latest, latest
			continue
		}
		for _, r := range c.replies() {
			if r.CreatedAt.After(notified) {
				fresh = append(fresh, replyNote{pr, r})
			}
		}
		if latest.After(notified) {
			st.ReplyNotified[pr.URL] = latest
		}
	}
	for url := range st.ReplyNotified {
		if !current[url] {
			delete(st.ReplyNotified, url)
			delete(st.ReplySeen, url)
		}
	}
	_ = st.save()
	return fresh, st
}

func markRepliesSeen(url string, t time.Time) *state {
	st, _ := loadState()
	st.ReplySeen[url] = t
	_ = st.save()
	return st
}

func notifyReplies(notes []replyNote) {
	switch {
	case len(notes) == 0:
	case len(notes) > 3:
		notify("New replies", "", fmt.Sprintf("%d new replies to your comments", len(notes)), notes[0].comment.URL)
	default:
		for _, n := range notes {
			body := strings.Join(wrapBody(n.comment.Body, 200), " ")
			notify("Reply from "+n.comment.Author, n.pr.Ref(), ansi.Truncate(ansi.Strip(body), 180, "…"), n.comment.URL)
		}
	}
}

const replyMutation = `mutation($id: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $id, body: $body}) { comment { id } }
}`

// postReply answers an inline review thread, or comments on the PR for the general
// conversation (threadID "").
func postReply(ctx context.Context, prURL, threadID, body string) error {
	if threadID == "" {
		_, err := gh(ctx, "pr", "comment", prURL, "--body", body)
		return err
	}
	_, err := gh(ctx, "api", "graphql", "-f", "query="+replyMutation, "-f", "id="+threadID, "-f", "body="+body)
	return err
}
