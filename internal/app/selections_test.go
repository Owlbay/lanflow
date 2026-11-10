package app

import (
	"os"
	"testing"

	"lanflow/internal/model"
)

func groupNowOf(s *Service, name string) string {
	for _, g := range s.Proxies().Groups {
		if g.Name == name {
			return g.Now
		}
	}
	return ""
}

// A selection made in Lanflow comes back on the next start even when the
// core itself restored nothing (fresh cache.db, defaults again).
func TestSelectionRestoredAfterRestart(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SelectProxy("节点选择", "美国 05"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SelectProxy("流媒体", "香港 01"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.s.selectionsPath()); err != nil {
		t.Fatalf("selections not saved: %v", err)
	}
	if err := h.s.StopCore(); err != nil {
		t.Fatal(err)
	}

	// A new service on the same data dir; the core comes up on its defaults.
	h2 := newHarness(t, func(h2 *harness) { h2.store = h.store })
	h2.s.opt.DataDir = h.s.opt.DataDir
	h2.s.selections = nil
	if err := h2.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if got := groupNowOf(h2.s, "节点选择"); got != "美国 05" {
		t.Errorf("节点选择 = %q, want 美国 05", got)
	}
	if got := groupNowOf(h2.s, "流媒体"); got != "香港 01" {
		t.Errorf("流媒体 = %q, want 香港 01", got)
	}
}

// Selections are kept per subscription, and stale entries are skipped.
func TestSelectionsPerSubscription(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	first, _ := h.store.Current()
	if err := h.s.SelectProxy("节点选择", "美国 05"); err != nil {
		t.Fatal(err)
	}

	other, _ := h.store.Put(model.Subscription{Name: "另一个", Source: "url", URL: "https://example.org/sub"})
	_ = h.store.SetContent(other.ID, []byte("proxies: []\n"))
	if err := h.s.UseSubscription(other.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SelectProxy("节点选择", "香港 01"); err != nil {
		t.Fatal(err)
	}

	// Back to the first subscription: its own pick returns.
	if err := h.s.UseSubscription(first.ID); err != nil {
		t.Fatal(err)
	}
	if got := groupNowOf(h.s, "节点选择"); got != "美国 05" {
		t.Errorf("first subscription: 节点选择 = %q, want 美国 05", got)
	}

	// A remembered member that no longer exists is left alone.
	h.s.selMu.Lock()
	h.s.loadSelectionsLocked()[first.ID]["节点选择"] = "已删除的节点"
	h.s.selMu.Unlock()
	h.ctrl.mu.Lock()
	n := len(h.ctrl.selected)
	h.ctrl.mu.Unlock()
	h.s.restoreSelections()
	h.ctrl.mu.Lock()
	defer h.ctrl.mu.Unlock()
	if len(h.ctrl.selected) != n {
		t.Errorf("selected a missing member: %v", h.ctrl.selected[n:])
	}
}

// The hidden probe group is never remembered.
func TestProbeGroupNotRemembered(t *testing.T) {
	h := newHarness(t, nil)
	h.s.rememberSelection(probeGroup, "香港 01")
	h.s.selMu.Lock()
	defer h.s.selMu.Unlock()
	for _, m := range h.s.loadSelectionsLocked() {
		if _, ok := m[probeGroup]; ok {
			t.Fatal("probe group remembered")
		}
	}
}
