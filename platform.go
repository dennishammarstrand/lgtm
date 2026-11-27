package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/gen2brain/beeep"
)

// stateDir is where lgtm keeps its state: $XDG_STATE_HOME, else %LOCALAPPDATA% on
// Windows and ~/.local/state elsewhere.
func stateDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return dir
	}
	if runtime.GOOS == "windows" {
		if dir, err := os.UserCacheDir(); err == nil {
			return dir
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}

// openURL opens url in the default browser.
func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}

// notify shows a desktop notification. On macOS it uses terminal-notifier when
// installed (clicking opens the PR), else osascript; elsewhere beeep (D-Bus or
// notify-send on Linux, a toast on Windows).
func notify(title, subtitle, msg, url string) {
	if runtime.GOOS != "darwin" {
		beeep.AppName = "lgtm"
		_ = beeep.Notify(title, subtitle+"\n"+msg, "")
		return
	}
	if p, err := exec.LookPath("terminal-notifier"); err == nil {
		_ = exec.Command(p, "-title", title, "-subtitle", subtitle, "-message", msg,
			"-open", url, "-group", url, "-sound", "default").Run()
		return
	}
	_ = exec.Command("osascript",
		"-e", "on run argv",
		"-e", `display notification (item 3 of argv) with title (item 1 of argv) subtitle (item 2 of argv) sound name "default"`,
		"-e", "end run",
		title, subtitle, msg).Run()
}
