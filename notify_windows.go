package main

import (
	"encoding/base64"
	"encoding/xml"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf16"
)

// toastScript shows the toast whose XML is in $env:LGTM_TOAST_XML. The script
// is fixed: the text from GitHub only ever arrives as data in that variable, so
// PowerShell never parses it as code.
const toastScript = `$ErrorActionPreference = 'Stop'
[void][Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]
[void][Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom, ContentType = WindowsRuntime]
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml($env:LGTM_TOAST_XML)
`

// PowerShell's own app id, so the toast shows without registering lgtm.
const toastShow = `$app = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($app).Show([Windows.UI.Notifications.ToastNotification]::new($xml))
`

// toastXML builds the toast with every value XML-escaped; clicking it opens url.
func toastXML(title, subtitle, msg, url string) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	launch := ""
	if strings.HasPrefix(url, "https://") {
		launch = ` activationType="protocol" launch="` + esc(url) + `"`
	}
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	return `<toast` + launch + `><visual><binding template="ToastGeneric">` +
		`<text>` + esc(title) + `</text><text>` + esc(subtitle) + `</text><text>` + esc(msg) + `</text>` +
		`</binding></visual></toast>`
}

// powershell runs a fixed script (as -EncodedCommand, so no quoting is involved)
// with the toast XML in the environment, without flashing a console window.
func powershell(script, toast string) *exec.Cmd {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Restricted",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd.Env = append(os.Environ(), "LGTM_TOAST_XML="+toast)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd
}

func notify(title, subtitle, msg, url string) {
	_ = powershell(toastScript+toastShow, toastXML(title, subtitle, msg, url)).Run()
}
