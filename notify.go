package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// state is shared between the TUI and `lgtm check` so a PR is only notified once.
type state struct {
	Notified  map[string]time.Time `json:"notified"`
	Viewed    map[string]time.Time `json:"viewed"`
	SplitDiff bool                 `json:"splitDiff,omitempty"`
	NoWrap    bool                 `json:"noWrap,omitempty"`

	// Used by `lgtm check` to skip the full fetch when nothing changed.
	NotifETag string    `json:"notifETag,omitempty"`
	LastFull  time.Time `json:"lastFull,omitempty"`

	// Per PR: the newest reply to your comments you have read / been notified about.
	ReplySeen     map[string]time.Time `json:"replySeen,omitempty"`
	ReplyNotified map[string]time.Time `json:"replyNotified,omitempty"`

	// Inline comments not yet submitted, per PR URL.
	Drafts map[string][]draft `json:"drafts,omitempty"`

	// The latest lgtm release, checked at most once a day.
	LatestVersion  string    `json:"latestVersion,omitempty"`
	VersionChecked time.Time `json:"versionChecked,omitempty"`
}

func statePath() string {
	return filepath.Join(stateDir(), "lgtm", "state.json")
}

func loadState() (st *state, existed bool) {
	st = &state{Notified: map[string]time.Time{}, Viewed: map[string]time.Time{},
		ReplySeen: map[string]time.Time{}, ReplyNotified: map[string]time.Time{},
		Drafts: map[string][]draft{}}
	data, err := os.ReadFile(statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return st, false
	}
	if err == nil {
		_ = json.Unmarshal(data, st)
	}
	if st.Notified == nil {
		st.Notified = map[string]time.Time{}
	}
	if st.Viewed == nil {
		st.Viewed = map[string]time.Time{}
	}
	if st.ReplySeen == nil {
		st.ReplySeen = map[string]time.Time{}
	}
	if st.ReplyNotified == nil {
		st.ReplyNotified = map[string]time.Time{}
	}
	if st.Drafts == nil {
		st.Drafts = map[string][]draft{}
	}
	return st, true
}

// save writes the state privately (it holds review drafts and private PR URLs).
func (s *state) save() error {
	p := statePath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700) // older versions created it world-readable
	data, _ := json.MarshalIndent(s, "", "  ")
	tmp := p + ".tmp"
	_ = os.Remove(tmp) // WriteFile keeps the mode of an existing file
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// reconcile records the current PRs and returns those not notified before.
// On the very first run nothing is returned, to avoid a burst of notifications.
// PRs no longer requested are pruned, so a re-request notifies again.
func reconcile(prs []PR) ([]PR, *state, error) {
	st, existed := loadState()
	current := map[string]bool{}
	var fresh []PR
	now := time.Now()
	for _, pr := range prs {
		current[pr.URL] = true
		if _, ok := st.Notified[pr.URL]; !ok {
			st.Notified[pr.URL] = now
			if existed {
				fresh = append(fresh, pr)
			}
		}
	}
	for url := range st.Notified {
		if !current[url] {
			delete(st.Notified, url)
		}
	}
	for url := range st.Viewed {
		if !current[url] {
			delete(st.Viewed, url)
		}
	}
	return fresh, st, st.save()
}

func markViewed(url string) *state {
	st, _ := loadState()
	st.Viewed[url] = time.Now()
	_ = st.save()
	return st
}

func saveDiffPrefs(split, wrap bool) {
	st, existed := loadState()
	if !existed {
		return // the first poll must seed the state, or it would notify about every PR
	}
	st.SplitDiff, st.NoWrap = split, !wrap
	_ = st.save()
}

func notifyNew(prs []PR) {
	switch {
	case len(prs) == 0:
	case len(prs) > 3:
		notify("Review requests", "", fmt.Sprintf("%d new pull requests to review", len(prs)), "https://github.com/pulls/review-requested")
	default:
		for _, pr := range prs {
			notify("Review requested", pr.Ref()+" · "+pr.Author.Login, pr.Title, pr.URL)
		}
	}
}
