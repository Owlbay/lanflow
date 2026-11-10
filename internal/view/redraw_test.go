package view

import (
	"sync"
	"testing"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
)

// TestDataRedrawsAreRateLimited: the first change after a quiet spell
// draws at once, a burst of changes within the gap draws once more when
// the gap ends, and an inactive window waits a full second.
func TestDataRedrawsAreRateLimited(t *testing.T) {
	api := fake.NewInstant()
	s := New(api, Options{Mac: true, Sync: true})
	defer s.Close()
	var mu sync.Mutex
	frames := 0
	s.invalidate = func() { mu.Lock(); frames++; mu.Unlock() }
	count := func() int { mu.Lock(); defer mu.Unlock(); return frames }
	s.active.Store(true)

	for i := 0; i < 10; i++ {
		s.onChange(app.TopicTraffic)
	}
	if count() != 1 {
		t.Fatalf("a burst should draw once at first, drew %d", count())
	}
	deadline := time.Now().Add(2 * time.Second)
	for count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count() != 2 {
		t.Fatalf("the burst should end in one trailing frame, drew %d", count())
	}
	time.Sleep(20 * time.Millisecond)
	if count() != 2 {
		t.Fatalf("no change, no frame: drew %d", count())
	}

	// Inactive: the next change after the active gap still waits for the idle gap.
	s.active.Store(false)
	s.rdMu.Lock()
	s.rdLast = time.Now().Add(-redrawActive * 2)
	s.rdMu.Unlock()
	s.onChange(app.TopicConns)
	time.Sleep(redrawActive + 50*time.Millisecond)
	if count() != 2 {
		t.Fatalf("an inactive window should wait %v, drew after %v", redrawIdle, redrawActive)
	}
	deadline = time.Now().Add(3 * time.Second)
	for count() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count() != 3 {
		t.Fatalf("the inactive frame never came: %d", count())
	}
}
