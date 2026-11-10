package app

import (
	"testing"

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
