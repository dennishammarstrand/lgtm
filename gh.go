package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type PR struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	IsDraft   bool      `json:"isDraft"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`

	// MyReview is set for PRs kept after the review request went away: your latest
	// review state (COMMENTED or CHANGES_REQUESTED).
	MyReview string `json:"-"`

	// Mine marks your own PRs; Reviewers holds their reviewers' latest states.
	Mine      bool       `json:"-"`
	Reviewers []reviewer `json:"-"`
}

func (p PR) Ref() string { return fmt.Sprintf("%s#%d", p.Repository.NameWithOwner, p.Number) }

type FileDiff struct {
	Path       string
	Status     byte // A added, D deleted, R renamed, M modified
	Lines      []string
	Add, Del   int
	firstHunk  int
	hunkStarts []int
	split      *splitDiff
	hl         []string
	emph       map[int][]bool
	nums       []lineNum
	hunkLabels map[string]string
}

// Body returns the diff lines to display, skipping the git header when hunks exist.
func (f *FileDiff) Body() []string {
	if f.firstHunk >= 0 {
		return f.Lines[f.firstHunk:]
	}
	return f.Lines
}

func gh(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args[:min(2, len(args))], " "), msg)
	}
	return out, nil
}

func fetchReviewRequests(ctx context.Context, query string) ([]PR, error) {
	args := []string{"search", "prs", "--state=open", "--limit", "100",
		"--json", "number,title,url,isDraft,createdAt,updatedAt,author,repository"}
	args = append(args, strings.Fields(query)...)
	out, err := gh(ctx, args...)
	if err != nil {
		return nil, err
	}
	var prs []PR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}
	sort.SliceStable(prs, func(i, j int) bool { return prs[i].UpdatedAt.After(prs[j].UpdatedAt) })
	return prs, nil
}

// fetchWatched returns the PRs requesting your review, plus PRs that did earlier and
// that you have reviewed without approving. GitHub drops the request as soon as any
// review (even a comment) is submitted; those PRs stay until approved, merged or closed.
func fetchWatched(ctx context.Context, query string) ([]PR, error) {
	prs, err := fetchReviewRequests(ctx, query)
	if err != nil {
		return nil, err
	}
	st, _ := loadState()
	requested := map[string]bool{}
	for _, pr := range prs {
		requested[pr.URL] = true
	}
	var missing []string
	for url := range st.Notified {
		if !requested[url] {
			missing = append(missing, url)
		}
	}
	sort.Strings(missing)
	kept, err := fetchStillPending(ctx, missing)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].UpdatedAt.After(kept[j].UpdatedAt) })
	return append(prs, kept...), nil
}

type trackedPR struct {
	PR
	State              string `json:"state"`
	ViewerLatestReview *struct {
		State string `json:"state"`
	} `json:"viewerLatestReview"`
	LatestOpinionatedReviews struct {
		Nodes []struct {
			State  string `json:"state"`
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"nodes"`
	} `json:"latestOpinionatedReviews"`
}

const trackedFields = `number title url isDraft createdAt updatedAt state
	author { login } repository { nameWithOwner } viewerLatestReview { state }
	latestOpinionatedReviews(first: 100) { nodes { state author { login } } }`

// fetchStillPending keeps the given PRs that are open, reviewed by you, and not
// approved by you. PRs you never reviewed (the request was removed) are dropped.
func fetchStillPending(ctx context.Context, urls []string) ([]PR, error) {
	var kept []PR
	for start := 0; start < len(urls); start += 50 {
		chunk := urls[start:min(start+50, len(urls))]
		var params, fields []string
		args := []string{"api", "graphql"}
		for i, u := range chunk {
			params = append(params, fmt.Sprintf("$u%d: URI!", i))
			fields = append(fields, fmt.Sprintf("p%d: resource(url: $u%d) { ...pr }", i, i))
			args = append(args, "-f", fmt.Sprintf("u%d=%s", i, u))
		}
		q := fmt.Sprintf("query(%s) { viewer { login } %s } fragment pr on PullRequest { %s }",
			strings.Join(params, ", "), strings.Join(fields, " "), trackedFields)
		out, err := gh(ctx, append(args, "-f", "query="+q)...)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			return nil, fmt.Errorf("parse gh output: %w", err)
		}
		var viewer struct {
			Login string `json:"login"`
		}
		_ = json.Unmarshal(resp.Data["viewer"], &viewer)
		for i := range chunk {
			var t *trackedPR
			if err := json.Unmarshal(resp.Data[fmt.Sprintf("p%d", i)], &t); err != nil || t == nil {
				continue
			}
			if t.State != "OPEN" || t.ViewerLatestReview == nil {
				continue
			}
			approved := false
			for _, r := range t.LatestOpinionatedReviews.Nodes {
				if r.Author.Login == viewer.Login && r.State == "APPROVED" {
					approved = true
				}
			}
			if !approved {
				t.PR.MyReview = t.ViewerLatestReview.State
				kept = append(kept, t.PR)
			}
		}
	}
	return kept, nil
}

func fetchDiff(ctx context.Context, url string) ([]*FileDiff, error) {
	out, err := gh(ctx, "pr", "diff", url, "--color=never")
	if err != nil {
		return nil, err
	}
	return parseDiff(string(out)), nil
}

func parseDiff(s string) []*FileDiff {
	var files []*FileDiff
	var cur *FileDiff
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		line = cleanText(strings.ReplaceAll(strings.TrimSuffix(line, "\r"), "\t", "    "))
		if strings.HasPrefix(line, "diff --git ") {
			cur = &FileDiff{firstHunk: -1, Status: 'M'}
			if i := strings.LastIndex(line, " b/"); i >= 0 {
				cur.Path = line[i+3:]
			}
			files = append(files, cur)
		}
		if cur == nil {
			continue
		}
		switch {
		// File headers only come before the first hunk; inside one, "--- x" is a
		// removed "-- x" line (an SQL comment, say) and "+++ x" an added "++ x".
		case cur.firstHunk < 0 && strings.HasPrefix(line, "+++ b/"):
			cur.Path = line[6:]
		case cur.firstHunk < 0 && strings.HasPrefix(line, "new file mode"):
			cur.Status = 'A'
		case cur.firstHunk < 0 && strings.HasPrefix(line, "deleted file mode"):
			cur.Status = 'D'
		case cur.firstHunk < 0 && strings.HasPrefix(line, "rename from"):
			cur.Status = 'R'
		case cur.firstHunk < 0 && (strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ")):
		case strings.HasPrefix(line, "@@"):
			if cur.firstHunk < 0 {
				cur.firstHunk = len(cur.Lines)
			}
			cur.hunkStarts = append(cur.hunkStarts, len(cur.Lines)-cur.firstHunk)
		case strings.HasPrefix(line, "+"):
			cur.Add++
		case strings.HasPrefix(line, "-"):
			cur.Del++
		}
		cur.Lines = append(cur.Lines, line)
	}
	return files
}

type reviewKind int

const (
	reviewApprove reviewKind = iota
	reviewComment
	reviewRequestChanges
	reviewReply  // answer a comment thread; not a review
	reviewInline // comment on diff lines; kept as a draft or posted right away
)

func (k reviewKind) String() string {
	return [...]string{"Approve", "Comment on", "Request changes on", "Reply on", "Comment on"}[k]
}

func submitReview(ctx context.Context, url string, kind reviewKind, body string) error {
	flag := [...]string{"--approve", "--comment", "--request-changes"}[kind]
	args := []string{"pr", "review", url, flag}
	if body != "" {
		args = append(args, "--body", body)
	}
	_, err := gh(ctx, args...)
	return err
}
