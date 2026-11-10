package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchAgent(t *testing.T) {
	dir := t.TempDir()
	home = func() (string, error) { return dir, nil }
	t.Cleanup(func() { home = os.UserHomeDir })
	exe := "/Applications/Lanflow & Co.app/Contents/MacOS/Lanflow"
	if Enabled(exe) {
		t.Fatal("enabled before")
	}
	if changed, err := Apply(exe, true); err != nil || !changed {
		t.Fatalf("apply: %v %v", changed, err)
	}
	if changed, _ := Apply(exe, true); changed {
		t.Fatal("rewrote an unchanged agent")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "Library", "LaunchAgents", Label+".plist"))
	for _, want := range []string{"Lanflow &amp; Co.app", "<string>--service</string>", "<string>--login</string>", "<key>RunAtLoad</key>"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist lacks %q:\n%s", want, b)
		}
	}
	if !Enabled(exe) || Enabled("/elsewhere/Lanflow") {
		t.Fatal("Enabled wrong")
	}
	// The app moved: the agent follows.
	if changed, _ := Apply("/elsewhere/Lanflow", true); !changed || !Enabled("/elsewhere/Lanflow") {
		t.Fatal("agent not rewritten for the new path")
	}
	if changed, err := Apply(exe, false); err != nil || !changed || Enabled("/elsewhere/Lanflow") {
		t.Fatalf("remove: %v %v", changed, err)
	}
	if changed, err := Apply(exe, false); err != nil || changed {
		t.Fatal("removing twice")
	}
}
