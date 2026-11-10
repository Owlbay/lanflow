package app

import (
	"testing"
	"time"
)

// TestBackgroundSlowsConnectionStream: the connections stream opens at
// connInterval; SetBackground(true) reopens it at connIntervalHidden and
// SetBackground(false) brings it back, each once per change.
func TestBackgroundSlowsConnectionStream(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	intervals := func() []time.Duration {
		h.ctrl.mu.Lock()
		defer h.ctrl.mu.Unlock()
		return append([]time.Duration(nil), h.ctrl.connIntervals...)
	}
	wait := func(n int) []time.Duration {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if got := intervals(); len(got) >= n {
				return got
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("stream opened %d times, want %d", len(intervals()), n)
		return nil
	}
	if got := wait(1); got[0] != connInterval {
		t.Fatalf("first stream interval %v, want %v", got[0], connInterval)
	}
	h.s.SetBackground(true)
	h.s.SetBackground(true) // no change: no restart
	if got := wait(2); got[1] != connIntervalHidden {
		t.Fatalf("hidden stream interval %v, want %v", got[1], connIntervalHidden)
	}
	h.s.SetBackground(false)
	if got := wait(3); got[2] != connInterval || len(got) != 3 {
		t.Fatalf("stream intervals %v", got)
	}
	time.Sleep(20 * time.Millisecond)
	if got := intervals(); len(got) != 3 {
		t.Errorf("an unchanged background restarted the stream: %v", got)
	}
	h.s.Shutdown()
}
