package app

import (
	"errors"
	"sync"
	"testing"

	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// fakeSecrets is an in-memory keychain.
type fakeSecrets struct {
	mu   sync.Mutex
	m    map[string]string
	sets int
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{m: map[string]string{}} }

func (f *fakeSecrets) Get(key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[key]
	if !ok {
		return nil, ErrSecretNotFound
	}
	return []byte(v), nil
}
func (f *fakeSecrets) Set(key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	f.m[key] = string(value)
	return nil
}
func (f *fakeSecrets) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, key)
	return nil
}
func (f *fakeSecrets) get(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[key]
}

func TestLinkSecretsGoToKeychainOnce(t *testing.T) {
	h := newHarness(t, nil)
	ks := newFakeSecrets()
	h.s.cs.secrets = ks
	subs := h.s.d.store.List()
	if err := h.s.storeLinkSecrets(subs); err != nil {
		t.Fatal(err)
	}
	if err := h.s.storeLinkSecrets(subs); err != nil {
		t.Fatal(err)
	}
	if got := ks.get(syncfile.SubscriptionKey(subs[0].ID)); got != "https://example.com/sub" || ks.sets != 1 {
		t.Fatalf("keychain = %q after %d writes", got, ks.sets)
	}
	// the sync document names it, never spells it
	doc := buildDoc(h.s.snapshotLocal(), linksKeychain)
	if len(doc.Subscriptions) != 1 || doc.Subscriptions[0].URL != syncfile.SubscriptionRef(subs[0].ID) {
		t.Fatalf("doc subscriptions = %+v", doc.Subscriptions)
	}
	// no keychain: an error to report, nothing stored
	h.s.cs.secrets = nil
	saved := Secrets
	Secrets = nil
	defer func() { Secrets = saved }()
	h.s.cs.links.stored = nil
	if err := h.s.storeLinkSecrets(subs); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("without a keychain: %v", err)
	}
}

func TestResolveLink(t *testing.T) {
	h := newHarness(t, nil)
	ks := newFakeSecrets()
	ks.m["sub-abc"] = "https://a.example/sub"
	h.s.cs.secrets = ks
	for _, c := range []struct{ ref, local, want string }{
		{"keychain:sub-abc", "", "https://a.example/sub"},
		{"keychain:sub-missing", "", ""},                                               // another machine: the user enters it
		{"keychain:sub-missing", "https://mine/sub", "https://mine/sub"},               // this machine still knows it
		{"https://plain.example/sub", "https://mine/sub", "https://plain.example/sub"}, // written by an agent
		{"", "https://mine/sub", "https://mine/sub"},
	} {
		if got := h.s.resolveLink(c.ref, c.local); got != c.want {
			t.Errorf("resolveLink(%q, %q) = %q, want %q", c.ref, c.local, got, c.want)
		}
	}
}

func TestSubscriptionWithoutLinkNeedsURL(t *testing.T) {
	h := newHarness(t, nil)
	sub, _ := h.store.Put(model.Subscription{ID: "feedbeef", Name: "远端", Source: urlSubscriptionK})
	h.s.refreshStore(TopicSubs)
	var got model.Subscription
	for _, s := range h.s.Subscriptions() {
		if s.ID == sub.ID {
			got = s
		}
	}
	if !got.NeedsURL {
		t.Fatalf("NeedsURL not set: %+v", got)
	}
	if err := h.s.UpdateSubscription(sub.ID); !errors.Is(err, errNeedsURL) {
		t.Fatalf("update without a link: %v", err)
	}
	got.URL = "https://example.com/new"
	if err := h.s.EditSubscription(got); err != nil {
		t.Fatal(err)
	}
	for _, s := range h.s.Subscriptions() {
		if s.ID == sub.ID && s.NeedsURL {
			t.Fatal("still NeedsURL after the link was entered")
		}
	}
}
