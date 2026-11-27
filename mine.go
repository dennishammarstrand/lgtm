package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// reviewer is someone asked to review one of your PRs, with their latest state:
// APPROVED, CHANGES_REQUESTED, COMMENTED, or PENDING when still requested.
type reviewer struct {
	Name  string
	State string
}

const minePRQuery = `query {
  search(query: "is:pr is:open author:@me archived:false", type: ISSUE, first: 50) {
    nodes { ... on PullRequest {
      number title url isDraft createdAt updatedAt
      author { login } repository { nameWithOwner }
      reviewRequests(first: 30) { nodes { requestedReviewer {
        ... on User { login }
        ... on Team { slug organization { login } }
        ... on Bot { login }
      } } }
      latestReviews(first: 30) { nodes { state author { login } } }
    } }
  }
}`

// fetchMine returns your own open PRs with their reviewers.
func fetchMine(ctx context.Context) ([]PR, error) {
	out, err := gh(ctx, "api", "graphql", "-f", "query="+minePRQuery)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Search struct {
				Nodes []struct {
					PR
					ReviewRequests struct {
						Nodes []struct {
							RequestedReviewer struct {
								Login        string `json:"login"`
								Slug         string `json:"slug"`
								Organization struct {
									Login string `json:"login"`
								} `json:"organization"`
							} `json:"requestedReviewer"`
						} `json:"nodes"`
					} `json:"reviewRequests"`
					LatestReviews struct {
						Nodes []struct {
							State  string `json:"state"`
							Author struct {
								Login string `json:"login"`
							} `json:"author"`
						} `json:"nodes"`
					} `json:"latestReviews"`
				} `json:"nodes"`
			} `json:"search"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}
	var prs []PR
	for _, n := range resp.Data.Search.Nodes {
		if n.URL == "" {
			continue
		}
		pr := n.PR
		pr.Mine = true
		pending := map[string]bool{}
		for _, rr := range n.ReviewRequests.Nodes {
			name := rr.RequestedReviewer.Login
			if r := rr.RequestedReviewer; r.Slug != "" {
				name = r.Organization.Login + "/" + r.Slug
			}
			if strings.Contains(strings.ToLower(name), "copilot") {
				name = "Copilot" // a requested Copilot review shows up as its bot login
			}
			if name != "" {
				pending[name] = true
				pr.Reviewers = append(pr.Reviewers, reviewer{name, "PENDING"})
			}
		}
		for _, r := range n.LatestReviews.Nodes {
			// A re-requested reviewer shows as pending rather than with their old review.
			if l := r.Author.Login; l != "" && !pending[l] && !isBot(l) {
				pr.Reviewers = append(pr.Reviewers, reviewer{l, r.State})
			}
		}
		prs = append(prs, pr)
	}
	sort.SliceStable(prs, func(i, j int) bool { return prs[i].UpdatedAt.After(prs[j].UpdatedAt) })
	return prs, nil
}

func isBot(login string) bool {
	return strings.HasSuffix(login, "[bot]") || login == "copilot-pull-request-reviewer"
}

// reviewerSummary renders e.g. "✓ erik · ⏳ anna, org/team" for the PR list,
// dimmed except for the approval check mark, which stands out in green.
func reviewerSummary(rs []reviewer) string {
	if len(rs) == 0 {
		return dimStyle.Render("no reviewers")
	}
	groups := []struct{ state, icon string }{
		{"APPROVED", "✓"}, {"CHANGES_REQUESTED", "✗"}, {"COMMENTED", "💬"}, {"PENDING", "⏳"},
	}
	var parts []string
	for _, g := range groups {
		var names []string
		for _, r := range rs {
			if r.State == g.state {
				names = append(names, r.Name)
			}
		}
		if len(names) > 0 {
			icon := dimStyle.Render(g.icon)
			if g.state == "APPROVED" {
				icon = okStyle.Bold(true).Render(g.icon)
			}
			parts = append(parts, icon+dimStyle.Render(" "+strings.Join(names, ", ")))
		}
	}
	return strings.Join(parts, dimStyle.Render(" · "))
}
