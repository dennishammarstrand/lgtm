package main

import "os/exec"

// notify uses terminal-notifier when installed (clicking opens the PR), else
// osascript, which gets the text as arguments of a fixed script.
func notify(title, subtitle, msg, url string) {
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
