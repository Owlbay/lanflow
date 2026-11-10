//go:build windows

package service

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOwnerMatchesWindows(t *testing.T) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	me := tu.User.Sid.String()
	d := t.TempDir()
	ok, err := ownerMatches(d, me)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skip("temp dir not owned by the current user (elevated test run?)")
	}
	if ok, _ := ownerMatches(d, "S-1-5-18"); ok {
		t.Fatal("SYSTEM reported as owner of a user temp dir")
	}
	// A junction / symlink is refused rather than followed.
	link := filepath.Join(d, "link")
	if err := os.Symlink(d, link); err == nil {
		if ok, err := ownerMatches(link, me); ok || err == nil {
			t.Fatal("followed a reparse point")
		}
	}
}

func TestCheckNotSystemDir(t *testing.T) {
	for _, bad := range []string{`C:\`, `C:\Windows\Temp\x`, `C:\Program Files\Lanflow`, `C:\ProgramData\Lanflow`, `C:\Users`} {
		if err := checkNotSystemDir(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if err := checkNotSystemDir(`C:\Users\someone\AppData\Roaming\Lanflow`); err != nil {
		t.Errorf("user dir refused: %v", err)
	}
}

func TestOpenLogFileWindowsRefusesLinks(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(d, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(d, "hard.log")
	if err := os.Link(target, hard); err != nil {
		t.Fatal(err)
	}
	if w, err := openLogFile(hard, ""); err == nil {
		_ = w.Close()
		t.Fatal("opened a hard-linked log")
	}
	sym := filepath.Join(d, "sym.log")
	if err := os.Symlink(target, sym); err == nil {
		if w, err := openLogFile(sym, ""); err == nil {
			_ = w.Close()
			t.Fatal("opened a symlinked log")
		}
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("target changed: %q", b)
	}
	ok := filepath.Join(d, "core.log")
	w, err := openLogFile(ok, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
}
