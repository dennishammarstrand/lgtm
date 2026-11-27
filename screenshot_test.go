package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestScreenshots renders the README screenshots from made-up data:
//
//	LGTM_SCREENSHOTS=docs go test -run TestScreenshots
//
// and `make screenshots` turns them into the PNGs in docs/.
func TestScreenshots(t *testing.T) {
	dir := os.Getenv("LGTM_SCREENSHOTS")
	if dir == "" {
		t.Skip("set LGTM_SCREENSHOTS to a directory to render them")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer func(v string) { version = v }(version)
	version = "0.3.0"

	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	pr := func(repo string, n int, title, author string, updated time.Duration) PR {
		p := PR{Number: n, Title: title, UpdatedAt: ago(updated), CreatedAt: ago(updated + 48*time.Hour)}
		p.Repository.NameWithOwner, p.Author.Login = repo, author
		p.URL = "https://github.com/" + repo + "/pull/" + strconv.Itoa(n)
		return p
	}
	hero := pr("acme/storefront", 482, "Add retry with backoff to the payment client", "maria", 25*time.Minute)
	prs := []PR{
		hero,
		pr("acme/storefront-api", 1290, "Bump Go to 1.27 and update dependencies", "sara", 3*time.Hour),
		pr("acme/design-system", 77, "Button: support a loading state", "jonas", 26*time.Hour),
	}
	commented := pr("acme/infra", 301, "Move staging to the new cluster", "li", 5*time.Hour)
	commented.MyReview = "COMMENTED"
	prs = append(prs, commented)
	mine1 := pr("acme/storefront", 479, "Cache product images at the edge", "you", 2*time.Hour)
	mine1.Mine, mine1.Reviewers = true, []reviewer{{"maria", "APPROVED"}, {"jonas", "APPROVED"}}
	mine2 := pr("acme/checkout", 158, "Show delivery estimates in the cart", "you", 20*time.Hour)
	mine2.Mine, mine2.Reviewers = true, []reviewer{{"li", "CHANGES_REQUESTED"}, {"Copilot", "PENDING"}}
	prs = append(prs, mine1, mine2)

	m := newModel(time.Minute, "", true)
	m.fetching, m.split = false, true
	m.prs, m.lastFetch = prs, now
	m.st.Viewed[commented.URL] = now // the other two stay marked new
	m.st.ReplySeen[commented.URL] = ago(6 * time.Hour)
	m.convs = map[string]*conversation{
		hero.URL: {Viewer: "you", Threads: []thread{
			{ID: "t1", Path: "payments/client.go", Line: 41, Side: "RIGHT", DiffHunk: demoHunk, Comments: []comment{
				{Author: "you", CreatedAt: ago(50 * time.Minute), Body: "Should we cap the total wait too? With 5 attempts this can block a checkout for ~30s."},
				{Author: "maria", CreatedAt: ago(25 * time.Minute), Body: "Good point, I'll add a context deadline so the whole retry loop is bounded."},
			}},
			{Comments: []comment{
				{Author: "maria", CreatedAt: ago(3 * time.Hour), Body: "Payments sometimes fail with a 503 during deploys. This retries idempotent calls with exponential backoff and jitter.\n\nCloses #470."},
				{Author: "jonas", CreatedAt: ago(2 * time.Hour), Body: "Nice, this will make the deploy alerts a lot quieter 🎉"},
			}},
		}},
		commented.URL: {Viewer: "you", Threads: []thread{{ID: "t2", Path: "deploy/staging.yaml", Line: 12, Comments: []comment{
			{Author: "you", CreatedAt: ago(7 * time.Hour), Body: "Do we need the old node pool after this?"},
			{Author: "li", CreatedAt: ago(5 * time.Hour), Body: "No, I'll remove it in a follow-up."},
		}}}},
	}
	m.st.Drafts[hero.URL] = []draft{{lineTarget: lineTarget{Path: "payments/client.go", Line: 47, Side: "RIGHT"}, Body: "nit: log the attempt number"}}
	if err := m.st.save(); err != nil { // opening a PR reloads the state from disk
		t.Fatal(err)
	}

	nm, _ := m.Update(tea.WindowSizeMsg{Width: 150, Height: 38})
	m = nm.(model)
	nm, _ = m.Update(diffMsg{url: hero.URL, updatedAt: hero.UpdatedAt, files: parseDiff(demoDiff), head: "abc123"})
	m = nm.(model)
	key := func(k string) {
		nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		m = nm.(model)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(truecolorBackgrounds(m.View())), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	key("l") // comments of the first PR
	write("prs.ansi")
	key("l") // files
	key("j")
	key("j")
	key("l") // the diff of payments/client.go
	key("n") // the retry loop
	for range 10 {
		key("j")
	}
	write("diff.ansi")
}

// freeze ignores the basic background colours (SGR 40-47), e.g. the line cursor,
// so spell them out in its palette.
var freezePalette = []string{"40;42;46", "215;78;111", "49;187;113", "211;229;97", "128;86;255", "237;97;215", "4;215;215", "197;200;198"}

var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

func truecolorBackgrounds(frame string) string {
	return sgr.ReplaceAllStringFunc(frame, func(seq string) string {
		params := strings.Split(sgr.FindStringSubmatch(seq)[1], ";")
		for i := 0; i < len(params); i++ {
			switch p := params[i]; {
			case p == "38" || p == "48": // extended colour: skip its arguments
				if i+1 < len(params) && params[i+1] == "2" {
					i += 4
				} else {
					i += 2
				}
			case len(p) == 2 && p[0] == '4' && p[1] >= '0' && p[1] <= '7':
				params[i] = "48;2;" + freezePalette[p[1]-'0']
			}
		}
		return "\x1b[" + strings.Join(params, ";") + "m"
	})
}

const demoHunk = `@@ -36,6 +36,14 @@ func (c *Client) Charge(ctx context.Context, req ChargeRequest) (*Charge, error) {
+	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
+		resp, err = c.do(ctx, req)`

const demoDiff = `diff --git a/payments/client.go b/payments/client.go
index 3b18e51..9f2c4aa 100644
--- a/payments/client.go
+++ b/payments/client.go
@@ -1,9 +1,11 @@
 package payments

 import (
 	"context"
+	"errors"
+	"math/rand/v2"
 	"net/http"
 	"time"
 )

 type Client struct {
@@ -34,14 +36,33 @@ func New(baseURL string) *Client {
 // Charge charges a card. Charges are idempotent per request ID.
 func (c *Client) Charge(ctx context.Context, req ChargeRequest) (*Charge, error) {
-	resp, err := c.do(ctx, req)
-	if err != nil {
-		return nil, err
+	var resp *Charge
+	var err error
+	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
+		resp, err = c.do(ctx, req)
+		if err == nil || !retryable(err) {
+			return resp, err
+		}
+		wait := backoff(attempt)
+		select {
+		case <-time.After(wait):
+		case <-ctx.Done():
+			return nil, ctx.Err()
+		}
 	}
-	return resp, nil
+	return nil, err
+}
+
+// backoff doubles the wait for each attempt, with jitter so clients don't retry in sync.
+func backoff(attempt int) time.Duration {
+	base := 100 * time.Millisecond << (attempt - 1)
+	return base/2 + rand.N(base/2)
 }
diff --git a/payments/client_test.go b/payments/client_test.go
index 1c2d3e4..5f6a7b8 100644
--- a/payments/client_test.go
+++ b/payments/client_test.go
@@ -20,3 +20,15 @@ func TestCharge(t *testing.T) {
 	}
 }
+
+func TestChargeRetriesOn503(t *testing.T) {
+	srv := flaky(t, 2, http.StatusServiceUnavailable)
+	c := New(srv.URL)
+	if _, err := c.Charge(context.Background(), ChargeRequest{ID: "r1"}); err != nil {
+		t.Fatalf("should succeed on the third attempt: %v", err)
+	}
+}
diff --git a/README.md b/README.md
index 0a1b2c3..4d5e6f7 100644
--- a/README.md
+++ b/README.md
@@ -12,3 +12,3 @@ Configure the client with the payment provider's base URL.
 
-Calls that fail with a 5xx are not retried.
+Idempotent calls that fail with a 5xx are retried up to 5 times with exponential backoff.
`
