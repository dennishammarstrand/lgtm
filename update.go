package main

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/mod/semver"
)

const (
	releasesRepo      = "dennishammarstrand/lgtm"
	versionCheckEvery = 24 * time.Hour
)

type versionMsg struct {
	latest string
	err    error
}

// releaseVersion is the running version as "v1.2.3", or "" for development,
// snapshot and pseudo-version builds, which never look for updates.
func releaseVersion() string {
	v := versionString()
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) || semver.Prerelease(v) != "" {
		return ""
	}
	return v
}

func updateAvailable(current, latest string) bool {
	return current != "" && semver.IsValid(latest) && semver.Compare(latest, current) > 0
}

// checkVersion asks GitHub for the latest release, at most once a day.
func (m *model) checkVersion() tea.Cmd {
	if releaseVersion() == "" || m.versionChecking || time.Since(m.versionChecked) < versionCheckEvery {
		return nil
	}
	m.versionChecking = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := gh(ctx, "api", "repos/"+releasesRepo+"/releases/latest", "--jq", ".tag_name")
		return versionMsg{latest: strings.TrimSpace(string(out)), err: err}
	}
}

func (m model) applyVersion(msg versionMsg) model {
	m.versionChecking, m.versionChecked = false, time.Now() // failures also wait a day
	if msg.err != nil {
		return m
	}
	m.latestVersion = msg.latest
	if st, existed := loadState(); existed { // the first poll must seed the state
		st.LatestVersion, st.VersionChecked = msg.latest, m.versionChecked
		_ = st.save()
		m.st = st
	}
	return m
}

// versionLabel is the version shown in the footer: the release, or "dev" for
// local builds (`lgtm -version` has the full pseudo-version).
func versionLabel() string {
	if v := releaseVersion(); v != "" {
		return v
	}
	return "dev"
}
