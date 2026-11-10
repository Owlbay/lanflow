package app

import (
	"sync"
	"testing"
	"time"
)

// Replacing the connections and log streams while the core restarts must
// never race streamWG.Add with Wait, never run two connections streams at
// once, and leave nothing running after stopStreams.
func TestStreamReplacementRace(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bg := i%2 == 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				bg = !bg
				h.s.SetBackground(bg)
				h.s.restartLogStream("debug")
			}
		}(i)
	}
	for i := 0; i < 20; i++ {
		h.s.stopStreams()
		h.s.startStreams()
	}
	close(stop)
	wg.Wait()
	h.s.stopStreams()

	h.ctrl.mu.Lock()
	active, most := h.ctrl.connActive, h.ctrl.connMaxActive
	h.ctrl.mu.Unlock()
	if active != 0 {
		t.Fatalf("%d connections streams still open after stopStreams", active)
	}
	if most > 1 {
		t.Fatalf("%d connections streams ran at once", most)
	}
}

// A replaced log stream derives from the streams' context: stopping the
// streams stops it too (it used to hang off rootCtx and outlive them).
func TestRestartedLogStreamStopsWithStreams(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.s.restartLogStream("warning")
	done := make(chan struct{})
	go func() { h.s.stopStreams(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopStreams did not return: a stream outlived its context")
	}
}
