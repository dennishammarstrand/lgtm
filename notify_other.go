//go:build !darwin && !windows

package main

import (
	"strings"

	"github.com/gen2brain/beeep"
)

// Notification servers read the body as markup (bold, links); show it as text.
var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// desktopNotify uses the desktop notification service (D-Bus, or notify-send).
func desktopNotify(title, subtitle, msg, url string) {
	beeep.AppName = "lgtm"
	_ = beeep.Notify(title, markupEscaper.Replace(subtitle+"\n"+msg), "")
}
