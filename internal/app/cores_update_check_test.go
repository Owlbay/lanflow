package app

import (
	"net/http"
	"testing"
	"time"
)

// The automatic core update check runs only while CoreAutoCheck is on and
// the app's own update check is not disabled: with DisableUpdateCheck set
// nothing reaches GitHub at start, however CoreAutoCheck is set.
func TestCoreUpdateCheckRespectsDisableUpdateCheck(t *testing.T) {
	_, hits := apiServer(t, sampleReleases(), http.StatusOK)
	h := newHarness(t, nil)
	h.s.TakeNotices()

	set := h.s.Settings()
	set.CoreAutoCheck, set.DisableUpdateCheck = true, true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.s.runCoreUpdateCheck()
	h.s.cores.mu.Lock()
	last := h.s.cores.lastCheck
	h.s.cores.mu.Unlock()
	if hits.Load() != 0 || !last.IsZero() {
		t.Fatalf("the core check must not run with the app's update check disabled: GitHub hits=%d lastCheck=%v", hits.Load(), last)
	}
	if n := h.s.TakeNotices(); len(n) != 0 {
		t.Fatalf("no notice expected: %+v", n)
	}
	if autoCoreCheck(set) {
		t.Fatal("autoCoreCheck must be false with DisableUpdateCheck")
	}

	// Re-enabling the app's check lets the core check run again.
	set.DisableUpdateCheck = false
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.s.runCoreUpdateCheck()
	h.s.cores.mu.Lock()
	last = h.s.cores.lastCheck
	h.s.cores.mu.Unlock()
	if hits.Load() == 0 || last.IsZero() || time.Since(last) > time.Minute {
		t.Fatalf("the core check should have run: GitHub hits=%d lastCheck=%v", hits.Load(), last)
	}
	if !autoCoreCheck(set) {
		t.Fatal("autoCoreCheck must be true with both switches on")
	}
	set.CoreAutoCheck = false
	if autoCoreCheck(set) {
		t.Fatal("autoCoreCheck must be false with CoreAutoCheck off")
	}
}
