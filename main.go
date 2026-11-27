package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"github.com/alecthomas/chroma/v2/styles"
	tea "github.com/charmbracelet/bubbletea"
)

const defaultQuery = "--review-requested=@me"

// version is set by GoReleaser; `go install` builds fall back to the module version.
var version = "dev"

func versionString() string {
	if info, ok := debug.ReadBuildInfo(); ok && version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func main() {
	cfg, err := loadConfig(configPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "lgtm: config:", err)
		os.Exit(2)
	}
	showVersion := flag.Bool("version", false, "print the version and exit")
	notifications := flag.Bool("notify", cfg.Notifications, "show desktop notifications")
	interval := flag.Duration("interval", cfg.Interval, "how often to check GitHub notifications for changes (GitHub may ask for longer)")
	query := flag.String("query", cfg.Query, "extra `gh search prs` flags selecting the PRs to watch")
	mine := flag.Bool("mine", cfg.Mine, "also list your own open PRs")
	theme := flag.String("theme", cfg.Theme, "chroma syntax theme for diffs, e.g. dracula, github-dark, tokyonight-night, monokai")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n  lgtm [flags]        interactive TUI\n  lgtm [flags] check  poll once, notify about new review requests, exit (run it from launchd, systemd or Task Scheduler)\n\nFlags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nThe defaults can be changed in %s; flags override it.\n", configPath())
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("lgtm", versionString())
		return
	}
	notificationsOn = *notifications
	if err := checkTheme(*theme); err != nil {
		fmt.Fprintln(os.Stderr, "lgtm:", err)
		os.Exit(2)
	}
	syntaxTheme = styles.Get(*theme)

	if flag.Arg(0) == "check" {
		if err := check(*query); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}

	p := tea.NewProgram(newModel(*interval, *query, *mine), tea.WithAltScreen(), tea.WithReportFocus())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if fm, ok := final.(model); ok && fm.runAfterQuit != nil {
		if err := runUpdate(fm.runAfterQuit); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func check(query string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	st, _ := loadState()
	res, err := checkNotifications(ctx, st.NotifETag)
	if err == nil && !res.changed && time.Since(st.LastFull) < fullRefreshEvery {
		return nil // nothing new; this cost no rate limit
	}

	prs, err := fetchWatched(ctx, query)
	if err != nil {
		return err
	}
	fresh, st, err := reconcile(prs)
	if err != nil {
		return err
	}
	notifyNew(fresh)

	// Replies to your comments, on review requests and on your own PRs.
	all := prs
	if mine, err := fetchMine(ctx); err == nil {
		all = append(all, mine...)
	}
	if convs, err := fetchConversations(ctx, all); err == nil {
		var replies []replyNote
		replies, st = reconcileReplies(all, convs)
		notifyReplies(replies)
	}
	if res.etag != "" {
		st.NotifETag = res.etag
	}
	st.LastFull = time.Now()
	return st.save()
}
