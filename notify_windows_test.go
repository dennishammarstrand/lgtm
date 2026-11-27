package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToastTextIsNeverRunAsPowerShell(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pwned")
	evil := `fix ]]> "@ $(New-Item -Path '` + marker + `') ` + "`" + `$(calc) <b>&amp;`
	out, err := powershell(toastScript+"'loaded'", toastXML(evil, evil, evil, "https://github.com/o/r/pull/1")).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "loaded") {
		t.Fatalf("the toast XML should load: %v %s", err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("text from GitHub was run as code")
	}
}
