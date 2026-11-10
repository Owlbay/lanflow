package app

import (
	"errors"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// Server-sent intervals land on the offered choices (关闭 / 6 / 12 / 24 h).
func TestSnapAutoUpdate(t *testing.T) {
	cases := map[int]int{-3: 0, 0: 0, 1: 6, 6: 6, 8: 6, 9: 6, 10: 12, 12: 12, 17: 12, 18: 12, 19: 24, 24: 24, 48: 24, 720: 24}
	for in, want := range cases {
		if got := snapAutoUpdate(in); got != want {
			t.Errorf("snapAutoUpdate(%d) = %d, want %d", in, got, want)
		}
	}
}

// An imported subscription with profile-update-interval: 8 is stored as
// 6 h (not 8, which the dialog could not show), an update of a
// subscription without an interval adopts the snapped server value, and
// editing snaps too.
func TestImportSnapsAutoUpdateInterval(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.prof.fetched = profile.Fetched{Data: []byte("proxies: [a]\n"), Name: "机场 B", UpdateInterval: 8}
	sub, err := h.s.ImportSubscriptionURL("https://example.com/b", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if sub.AutoUpdateHours != 6 {
		t.Fatalf("imported AutoUpdateHours = %d, want 6", sub.AutoUpdateHours)
	}

	cur, _ := h.store.Current() // the harness subscription has no interval yet
	h.prof.fetched = profile.Fetched{Data: []byte("proxies: [b]\n"), UpdateInterval: 20}
	if err := h.s.UpdateSubscription(cur.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.store.Get(cur.ID); got.AutoUpdateHours != 24 {
		t.Fatalf("updated AutoUpdateHours = %d, want 24", got.AutoUpdateHours)
	}

	if err := h.s.EditSubscription(model.Subscription{ID: cur.ID, Name: cur.Name, URL: cur.URL, AutoUpdateHours: 11}); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.store.Get(cur.ID); got.AutoUpdateHours != 12 {
		t.Fatalf("edited AutoUpdateHours = %d, want 12", got.AutoUpdateHours)
	}
	if err := h.s.EditSubscription(model.Subscription{ID: cur.ID, Name: cur.Name, URL: cur.URL, AutoUpdateHours: -1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.store.Get(cur.ID); got.AutoUpdateHours != 0 {
		t.Fatalf("negative must turn auto-update off, got %d", got.AutoUpdateHours)
	}
}

// A failing auto update is retried with a backoff of at least an hour, not
// on every tick, and the same error raises one system notification.
func TestAutoUpdateFailureBacksOff(t *testing.T) {
	h := newHarness(t, nil)
	cur, _ := h.store.Current()
	cur.AutoUpdateHours = 6
	_, _ = h.store.Put(cur)
	now := time.Now()
	h.s.d.now = func() time.Time { return now }
	h.prof.mu.Lock()
	h.prof.fetchErr = errors.New("connection refused")
	h.prof.mu.Unlock()
	attempts := func() int {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		return h.s.state.autoFail[cur.ID].failures
	}
	systemNotices := func() int {
		n := 0
		for _, x := range h.s.TakeNotices() {
			if x.System {
				n++
			}
		}
		return n
	}

	h.s.runAutoUpdates()
	if attempts() != 1 || systemNotices() != 1 {
		t.Fatalf("first failure: attempts %d", attempts())
	}
	for i := 0; i < 5; i++ { // 10-minute ticks within the hour: no retry
		now = now.Add(autoUpdateTick)
		h.s.runAutoUpdates()
	}
	if attempts() != 1 {
		t.Fatalf("retried within the backoff: %d attempts", attempts())
	}
	now = now.Add(time.Hour)
	h.s.runAutoUpdates()
	if attempts() != 2 || systemNotices() != 0 {
		t.Fatalf("second failure: attempts %d (or a repeated notification)", attempts())
	}
	now = now.Add(time.Hour) // the backoff doubled to 2 h
	h.s.runAutoUpdates()
	if attempts() != 2 {
		t.Fatalf("backoff did not double: %d attempts", attempts())
	}

	h.prof.mu.Lock()
	h.prof.fetchErr = nil
	h.prof.fetched = profile.Fetched{Data: []byte("proxies: []\n")}
	h.prof.mu.Unlock()
	now = now.Add(2 * time.Hour)
	h.s.runAutoUpdates()
	h.s.mu.Lock()
	_, still := h.s.state.autoFail[cur.ID]
	h.s.mu.Unlock()
	if still {
		t.Fatal("success did not clear the failure record")
	}
}

func TestRetryAfter(t *testing.T) {
	for n, want := range map[int]time.Duration{1: time.Hour, 2: 2 * time.Hour, 3: 4 * time.Hour, 4: 6 * time.Hour, 9: 6 * time.Hour} {
		if got := retryAfter(n, 6*time.Hour); got != want {
			t.Errorf("retryAfter(%d) = %v, want %v", n, got, want)
		}
	}
}
