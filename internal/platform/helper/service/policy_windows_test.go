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
