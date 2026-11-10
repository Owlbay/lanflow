package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppBundleOf(t *testing.T) {
	cases := map[string]string{
		"/Applications/Safari.app/Contents/MacOS/Safari": "/Applications/Safari.app",
		"/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Versions/1/Helpers/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper": "/Applications/Google Chrome.app",
		"/Applications/Safari.app":                              "/Applications/Safari.app",
		"/Applications/Xcode.APP/Contents/MacOS/Xcode":          "/Applications/Xcode.APP",
		"  /System/Applications/Mail.app/Contents/MacOS/Mail\n": "/System/Applications/Mail.app",
		`C:\Program Files\Foo.app\foo.exe`:                      `C:\Program Files\Foo.app`,
		"/usr/bin/curl":                                         "",
		"/Users/x/.app/bin/thing":                               "", // ".app" alone names no bundle
		"/Users/x/apple/bin":                                    "",
		"":                                                      "",
	}
	for in, want := range cases {
		if got := appBundleOf(in); got != want {
			t.Errorf("appBundleOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProcIconKey(t *testing.T) {
	a := procIconKey("/Applications/Safari.app", 1000)
	if a != procIconKey("/Applications/Safari.app", 1000) {
		t.Fatal("key not stable")
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(a) {
		t.Fatalf("key %q is not 32 hex digits", a)
	}
	if a == procIconKey("/Applications/Safari.app", 1001) {
		t.Error("key ignores the modification time")
	}
	if a == procIconKey("/Applications/Mail.app", 1000) {
		t.Error("key ignores the path")
	}
}

// fakeApp makes a Foo.app bundle with an executable inside and returns
// the executable's path.
func fakeApp(t *testing.T, dir, name string) string {
	t.Helper()
	exe := filepath.Join(dir, name+".app", "Contents", "MacOS", name)
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func waitProcIcon(t *testing.T, topics <-chan Topic) {
	t.Helper()
	select {
	case <-topics:
	case <-time.After(3 * time.Second):
		t.Fatal("TopicProcIcons did not fire")
	}
}

// settled waits until no load of subject runs any more.
func settled(t *testing.T, h *harness, subject string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.s.mu.Lock()
		c := h.s.procIconsLocked()
		h.s.mu.Unlock()
		c.mu.Lock()
		pending := c.pending[subject]
		c.mu.Unlock()
		if !pending {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("load of %s never finished", subject)
}

func TestProcessIcon(t *testing.T) {
	h := newHarness(t, nil)
	apps := t.TempDir()
	foo := fakeApp(t, apps, "Foo")
	bar := fakeApp(t, apps, "Bar")
	png := []byte("\x89PNG not really")
	var calls atomic.Int32
	h.s.d.fileIcon = func(path string, px int) ([]byte, error) {
		calls.Add(1)
		if px != procIconPx {
			t.Errorf("asked for %d px, want %d", px, procIconPx)
		}
		switch path {
		case iconSubject(foo):
			return png, nil
		case iconSubject(bar):
			return nil, errors.New("no icon")
		}
		t.Errorf("asked for the icon of %q", path)
		return nil, errors.New("unexpected")
	}
	topics := make(chan Topic, 8)
	off := h.s.OnChange(func(tp Topic) {
		if tp == TopicProcIcons {
			topics <- tp
		}
	})
	defer off()

	// The first call starts the load and returns nothing; the topic fires
	// when the icon is there, and the icon is then served from memory.
	if got := h.s.ProcessIcon(foo); got != nil {
		t.Fatalf("icon before loading: %q", got)
	}
	waitProcIcon(t, topics)
	if got := h.s.ProcessIcon(foo); !bytes.Equal(got, png) {
		t.Fatalf("icon after loading: %q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("platform asked %d times, want 1", calls.Load())
	}
	entries, err := os.ReadDir(filepath.Join(h.dir, procIconsDir))
	if err != nil || len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".png" {
		t.Fatalf("cache directory: %v %v", entries, err)
	}

	// A fresh in-memory cache (the next launch) finds the icon on disk
	// without asking the platform again.
	h.s.mu.Lock()
	h.s.state.procIcons = nil
	h.s.mu.Unlock()
	if got := h.s.ProcessIcon(foo); got != nil {
		t.Fatalf("icon before reading the disk cache: %q", got)
	}
	waitProcIcon(t, topics)
	if got := h.s.ProcessIcon(foo); !bytes.Equal(got, png) {
		t.Fatalf("icon from the disk cache: %q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("platform asked %d times after a restart, want 1", calls.Load())
	}

	// An application without an icon is asked about once and stays blank.
	if got := h.s.ProcessIcon(bar); got != nil {
		t.Fatalf("icon of Bar: %q", got)
	}
	settled(t, h, iconSubject(bar))
	if got := h.s.ProcessIcon(bar); got != nil {
		t.Fatalf("icon of Bar after failing: %q", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("platform asked %d times, want 2", calls.Load())
	}
	select {
	case <-topics:
		t.Fatal("TopicProcIcons fired for a failed load")
	case <-time.After(50 * time.Millisecond):
	}

	// A path that is not on disk is never sent to the platform.
	gone := filepath.Join(apps, "Gone.app", "Contents", "MacOS", "Gone")
	if got := h.s.ProcessIcon(gone); got != nil {
		t.Fatalf("icon of a missing app: %q", got)
	}
	settled(t, h, iconSubject(gone))
	if calls.Load() != 2 {
		t.Fatalf("platform asked about a missing app")
	}
	if got := h.s.ProcessIcon(""); got != nil {
		t.Fatalf("icon of no path: %q", got)
	}
	if runtime.GOOS == "darwin" {
		// A bare binary has no bundle, so no icon, without any load.
		if got := h.s.ProcessIcon("/usr/bin/curl"); got != nil {
			t.Fatalf("icon of a bare binary: %q", got)
		}
		if calls.Load() != 2 {
			t.Fatalf("platform asked about a bare binary")
		}
	}
}

func TestProcessIconWithoutPlatform(t *testing.T) {
	h := newHarness(t, nil)
	exe := fakeApp(t, t.TempDir(), "Foo")
	if got := h.s.ProcessIcon(exe); got != nil {
		t.Fatalf("icon without a platform loader: %q", got)
	}
}
