package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// updater says how this copy of lgtm was installed and how to update it.
type updater struct {
	how     string   // "Homebrew", "Scoop", "Go" or "" for a manual download
	command []string // run after quitting; nil when lgtm can't run it itself
	hint    string   // what to run yourself when command is nil
}

func (u updater) label() string { return strings.Join(append([]string{}, u.command...), " ") }

// detectUpdater works out the install method from where the binary lives.
func detectUpdater(exe string) updater {
	p := strings.ToLower(strings.ReplaceAll(exe, `\`, "/"))
	gobin := os.Getenv("GOBIN")
	if gobin == "" {
		gopath := os.Getenv("GOPATH")
		if gopath == "" {
			home, _ := os.UserHomeDir()
			gopath = filepath.Join(home, "go")
		}
		gobin = filepath.Join(strings.Split(gopath, string(os.PathListSeparator))[0], "bin")
	}
	switch {
	case strings.Contains(p, "/caskroom/lgtm/") || strings.Contains(p, "/cellar/lgtm/"):
		return updater{how: "Homebrew", command: []string{"brew", "upgrade", "lgtm"}}
	case strings.Contains(p, "/scoop/apps/lgtm/"):
		// Scoop won't replace an app that is running, so this has to happen after quitting.
		return updater{how: "Scoop", hint: "scoop update lgtm"}
	case strings.EqualFold(filepath.Clean(filepath.Dir(exe)), filepath.Clean(gobin)):
		return updater{how: "Go", command: []string{"go", "install", "github.com/" + releasesRepo + "@latest"}}
	}
	return updater{}
}

// currentUpdater is a variable so tests can replace it.
var currentUpdater = func() updater {
	exe, err := os.Executable()
	if err != nil {
		return updater{}
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return detectUpdater(exe)
}

// startUpdate is the U key: it asks before doing anything.
func (m model) startUpdate() (model, tea.Cmd) {
	if !updateAvailable(releaseVersion(), m.latestVersion) {
		m.setStatus("lgtm "+versionLabel()+" is the latest version", false)
		return m, nil
	}
	m.confirmUpdate = true
	return m, nil
}

// confirmUpdatePrompt is shown in the footer while waiting for y.
func (m model) confirmUpdatePrompt() string {
	u := currentUpdater()
	switch {
	case u.command != nil:
		return fmt.Sprintf("Update to %s with `%s`? lgtm restarts when it's done · y update · any other key cancels", m.latestVersion, u.label())
	case u.hint != "":
		return fmt.Sprintf("%s can't update lgtm while it runs: quit (q) and run `%s` · any key closes", u.how, u.hint)
	}
	return fmt.Sprintf("Open the %s release page to download it? y open · any other key cancels", m.latestVersion)
}

func (m model) answerUpdate(key string) (tea.Model, tea.Cmd) {
	m.confirmUpdate = false
	if key != "y" {
		return m, nil
	}
	u := currentUpdater()
	switch {
	case u.command != nil:
		m.runAfterQuit = u.command
		return m, tea.Quit
	case u.hint != "":
		return m, nil
	}
	url := "https://github.com/" + releasesRepo + "/releases/latest"
	return m, func() tea.Msg { openURL(url); return nil }
}

// runUpdate runs the update command in the terminal, then starts the new lgtm
// with the same arguments.
func runUpdate(command []string) error {
	fmt.Printf("Updating lgtm: %s\n", strings.Join(command, " "))
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("update failed: %w", err)
	}
	// Start lgtm the way it was started: Homebrew's link or Go's bin now point at
	// the new version, while the old binary itself may be gone.
	self, err := exec.LookPath(os.Args[0])
	if err != nil {
		fmt.Println("Updated. Start lgtm again to use the new version.")
		return nil
	}
	restart := exec.Command(self, os.Args[1:]...)
	restart.Stdin, restart.Stdout, restart.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := restart.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		return err
	}
	return nil
}
