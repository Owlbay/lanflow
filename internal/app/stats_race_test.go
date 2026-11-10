package app

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"lanflow/internal/model"
)

func statsFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if _, ok := classifyStatsFile(statsFileOf(e.Name())); ok {
			out = append(out, e.Name())
		}
	}
	return out
}

// A flush that cannot write keeps the day dirty: the next flush writes it.
func TestStatsFlushFailureRetries(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "stats")
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.Local)
	s := newStatsStore(dir, func() time.Time { return now }, nil)
	s.record([]model.Connection{statsConn("a", "example.com:443", "192.168.1.2", 0, 0)}, now)
	s.record([]model.Connection{statsConn("a", "example.com:443", "192.168.1.2", 100, 900)}, now)
	if err := os.WriteFile(dir, []byte("in the way"), 0o600); err != nil { // the directory cannot be made
		t.Fatal(err)
	}
	if err := s.flush(); err == nil {
		t.Fatal("flush should fail")
	}
	os.Remove(dir)
	if err := s.flush(); err != nil {
		t.Fatal(err)
	}
	if got := statsFilesIn(t, dir); len(got) != 1 {
		t.Fatalf("files after retry: %v", got)
	}
	re := newStatsStore(dir, func() time.Time { return now }, nil)
	if err := re.load(); err != nil {
		t.Fatal(err)
	}
	if st := re.snapshot(model.StatsToday); st.Totals.Download != 900 {
		t.Fatalf("reloaded download = %d, want 900", st.Totals.Download)
	}
}

// reset racing flush must never let the flush write erased days back:
// whenever the memory is empty after a reset, so is the disk.
func TestStatsResetFlushRace(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.Local)
	s := newStatsStore(dir, func() time.Time { return now }, nil)
	for i := 0; i < 30; i++ {
		s.record(nil, now)
		s.record([]model.Connection{statsConn("a", "example.com:443", "192.168.1.2", int64(i+1), int64(i+1))}, now)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.flush() }()
		go func() { defer wg.Done(); _ = s.reset() }()
		wg.Wait()
		s.mu.Lock()
		empty := len(s.days) == 0
		s.mu.Unlock()
		if files := statsFilesIn(t, dir); empty && len(files) > 0 {
			t.Fatalf("round %d: memory reset but disk holds %v", i, files)
		}
		_ = s.reset()
	}
}

// Recording today leaves the cached history of a range alone; a change to
// a past day (load) invalidates it.
func TestStatsHistoryCache(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.Local) // a Thursday
	s := newStatsStore(dir, func() time.Time { return now }, nil)
	yesterday := now.AddDate(0, 0, -1)
	s.record([]model.Connection{statsConn("y", "old.example.com:443", "192.168.1.2", 0, 0)}, yesterday)
	s.record([]model.Connection{statsConn("y", "old.example.com:443", "192.168.1.2", 10, 10)}, yesterday)
	s.record(nil, now)
	week := s.snapshot(model.StatsWeek)
	s.mu.Lock()
	h := s.hist[model.StatsWeek]
	s.mu.Unlock()
	for i := 1; i <= 5; i++ {
		s.record([]model.Connection{statsConn("t", "new.example.com:443", "192.168.1.3", int64(i), int64(i*2))}, now)
		got := s.snapshot(model.StatsWeek)
		if got.Totals.Download != week.Totals.Download+int64(i*2) {
			t.Fatalf("today not added: %d", got.Totals.Download)
		}
	}
	s.mu.Lock()
	same := s.hist[model.StatsWeek] == h
	s.mu.Unlock()
	if !same {
		t.Fatal("recording today rebuilt the history")
	}
	// a late sample for yesterday is a change to the history
	s.record([]model.Connection{statsConn("y", "old.example.com:443", "192.168.1.2", 10, 110)}, yesterday)
	if got := s.snapshot(model.StatsWeek); got.Totals.Download != week.Totals.Download+10+110 { // today 10, the late sample counts whole (a new ID to the tracker)
		t.Fatalf("history change not seen: %d", got.Totals.Download)
	}
}
