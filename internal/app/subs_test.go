package app

import (
	"errors"
	"testing"

	"lanflow/internal/profile"
)

// An update whose new content fails validation keeps the stored content,
// raises the §6.3 dialog notice and returns ErrInvalidConfig.
func TestUpdateInvalidConfigRaisesDialogNotice(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	_ = h.s.TakeNotices()
	cur, _ := h.store.Current()
	h.prof.fetched = profile.Fetched{Data: []byte("proxies: [bad]\n")}
	h.core.validateErr = errors.New("proxy 'xx' not found")
	err := h.s.UpdateSubscription(cur.ID)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
	ns := h.s.TakeNotices()
	if len(ns) != 1 || ns[0].Kind != "invalid-config" || ns[0].Subject != cur.ID {
		t.Fatalf("notices = %+v", ns)
	}
	if data, _ := h.store.Content(cur.ID); string(data) != "proxies: []\n" {
		t.Fatalf("stored content replaced: %q", data)
	}
	if got, _ := h.store.Get(cur.ID); got.LastError == "" {
		t.Fatal("LastError not recorded")
	}

	// A good update afterwards clears the error without a notice.
	h.core.validateErr = nil
	if err := h.s.UpdateSubscription(cur.ID); err != nil {
		t.Fatal(err)
	}
	if ns := h.s.TakeNotices(); len(ns) != 0 {
		t.Fatalf("unexpected notices %+v", ns)
	}
	if got, _ := h.store.Get(cur.ID); got.LastError != "" {
		t.Fatalf("LastError = %q", got.LastError)
	}
}

// A subscription imported without switching is validated too and dropped
// when it fails; the import phase is cleared afterwards.
func TestImportWithoutSwitchingValidates(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.prof.fetched = profile.Fetched{Data: []byte("proxies: [a]\n"), Name: "机场 B"}
	h.core.validateErr = errors.New("bad")
	if _, err := h.s.ImportSubscriptionURL("https://example.com/b", "", "", false, false); err == nil {
		t.Fatal("invalid config must be rejected")
	}
	if n := len(h.store.List()); n != 1 {
		t.Fatalf("failed import kept: %d subscriptions", n)
	}
	if ph := h.s.Status().ImportPhase; ph != "" {
		t.Fatalf("ImportPhase = %q after import", ph)
	}
	h.core.validateErr = nil
	sub, err := h.s.ImportSubscriptionURL("https://example.com/b", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Current || sub.Name != "机场 B" || len(h.store.List()) != 2 {
		t.Fatalf("sub = %+v, %d subscriptions", sub, len(h.store.List()))
	}
	// Content that is not a Clash config fails before anything is stored.
	h.prof.fetched = profile.Fetched{Data: []byte("<html>")}
	if _, err := h.s.ImportSubscriptionURL("https://example.com/c", "", "", false, false); err == nil || len(h.store.List()) != 2 {
		t.Fatalf("err = %v, %d subscriptions", err, len(h.store.List()))
	}
}
