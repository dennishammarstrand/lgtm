package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// openURL opens an https URL in the default browser.
func openURL(url string) {
	if !strings.HasPrefix(url, "https://") {
		return // only GitHub links; never hand a file path or other scheme to the OS
	}
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
