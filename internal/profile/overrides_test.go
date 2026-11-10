package profile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lanflow/internal/model"
)

// A store with the former single texts gets them as list entries: the
// subscription's first, the global one last (the former order), the
// subscription selecting its own, and the old files gone.
func TestOverridesMigrateLegacyTexts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Put(model.Subscription{Name: "A", Source: "url", URL: "https://a"})
	b, _ := s.Put(model.Subscription{Name: "B", Source: "url", URL: "https://b"})
	// Write the old layout by hand, as a previous version left it.
	_ = os.Remove(filepath.Join(dir, "overrides.json"))
	if err := os.WriteFile(filepath.Join(dir, "overrides", "global.yaml"), []byte("log-level: warning\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "overrides", a.ID+".yaml"), []byte("mode: global\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	list := s.Overrides()
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	if list[0].ID != a.ID || list[0].Text != "mode: global\n" || !list[0].Enabled || list[0].Global || list[0].Name != "A 覆写" {
		t.Errorf("subscription entry = %+v", list[0])
	}
	if list[1].ID != "global" || list[1].Text != "log-level: warning\n" || !list[1].Global || !list[1].Enabled {
		t.Errorf("global entry = %+v", list[1])
	}
	got, _ := s.Get(a.ID)
	if len(got.Overrides) != 1 || got.Overrides[0] != a.ID {
		t.Errorf("A should select its own entry, got %v", got.Overrides)
	}
	if got, _ := s.Get(b.ID); len(got.Overrides) != 0 {
		t.Errorf("B selects nothing, got %v", got.Overrides)
	}
	for _, f := range []string{"global.yaml", a.ID + ".yaml"} {
		if _, err := os.Stat(filepath.Join(dir, "overrides", f)); !os.IsNotExist(err) {
			t.Errorf("old file %s should be removed", f)
		}
	}
	// The legacy accessors read the list.
	if s.Override("global") != "log-level: warning\n" || s.Override(a.ID) != "mode: global\n" {
		t.Error("legacy accessors must read the migrated entries")
	}
	// Reopening keeps the list (no second migration).
	s, _ = Open(dir)
	if l := s.Overrides(); len(l) != 2 || l[1].ID != "global" {
		t.Errorf("after reopen: %+v", l)
	}
}

func TestOverridesListCRUDAndOrder(t *testing.T) {
	s, _ := openTestStore(t)
	sub, _ := s.Put(model.Subscription{Name: "A", Source: "url", URL: "https://a"})
	o1, err := s.PutOverride(model.Override{Name: "one", Enabled: true, Text: "a: 1\n"})
	if err != nil || o1.ID == "" || o1.Source != model.OverrideLocal {
		t.Fatalf("put: %+v %v", o1, err)
	}
	o2, _ := s.PutOverride(model.Override{Name: "two", Enabled: true, Global: true, Text: "b: 2\n"})
	o3, _ := s.PutOverride(model.Override{Name: "three", Enabled: false, Text: "c: 3\n"})
	if _, err := s.PutOverride(model.Override{ID: "../x", Name: "bad"}); err == nil {
		t.Error("path-like ID must be rejected")
	}
	sub.Overrides = []string{o1.ID, o3.ID}
	if _, err := s.Put(sub); err != nil {
		t.Fatal(err)
	}

	// Layers: list order, enabled only, global or selected.
	got, _ := s.Get(sub.ID)
	layers := Layers(s.Overrides(), got)
	if len(layers) != 2 || layers[0].Name != "one" || layers[1].Name != "two" {
		t.Fatalf("layers = %+v", layers)
	}
	other := model.Subscription{ID: "deadbeef"}
	if l := Layers(s.Overrides(), other); len(l) != 1 || l[0].Name != "two" {
		t.Errorf("another subscription gets the global entry only, got %+v", l)
	}

	// Enabling three adds it; moving it up changes the order.
	o3.Enabled = true
	if _, err := s.PutOverride(o3); err != nil {
		t.Fatal(err)
	}
	if l := Layers(s.Overrides(), got); len(l) != 3 || l[2].Name != "three" {
		t.Fatalf("after enable: %+v", l)
	}
	if err := s.MoveOverride(o3.ID, -2); err != nil {
		t.Fatal(err)
	}
	if l := s.Overrides(); l[0].ID != o3.ID || l[1].ID != o1.ID || l[2].ID != o2.ID {
		t.Fatalf("after move: %+v", l)
	}
	if err := s.MoveOverride(o3.ID, -5); err != nil {
		t.Fatal("moving past the start clamps")
	}
	if err := s.MoveOverride("nope", 1); err == nil {
		t.Error("moving a missing entry must fail")
	}

	// Deleting drops the selection too.
	if err := s.DeleteOverride(o1.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(sub.ID)
	if len(got.Overrides) != 1 || got.Overrides[0] != o3.ID {
		t.Errorf("selection after delete = %v", got.Overrides)
	}
	if _, ok := s.GetOverride(o1.ID); ok {
		t.Error("deleted entry still present")
	}
	if err := s.DeleteOverride(o1.ID); err == nil {
		t.Error("deleting twice must fail")
	}

	// Persisted across reopen.
	s2, err := Open(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if l := s2.Overrides(); len(l) != 2 || l[0].ID != o3.ID || !l[0].Enabled || l[1].ID != o2.ID {
		t.Errorf("after reopen: %+v", l)
	}
}

// The legacy accessors create and select entries the way the single texts
// behaved, and compose attributes the layers by name.
func TestOverridesLegacyAccessorsAndCompose(t *testing.T) {
	s, _ := openTestStore(t)
	sub, _ := s.Put(model.Subscription{Name: "A", Source: "url", URL: "https://a"})
	if err := s.SetOverride(sub.ID, "log-level: debug\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOverride("global", "log-level: warning\n"); err != nil {
		t.Fatal(err)
	}
	extra, _ := s.PutOverride(model.Override{Name: "extra", Enabled: true, Global: true, Text: "rules+:\n  - DOMAIN,x.com,DIRECT\n"})
	got, _ := s.Get(sub.ID)
	if len(got.Overrides) != 1 || got.Overrides[0] != sub.ID {
		t.Fatalf("SetOverride must select the subscription's entry, got %v", got.Overrides)
	}
	layers := Layers(s.Overrides(), got)
	if len(layers) != 3 || layers[0].Name != layerSubOverride || layers[1].Name != layerGlobal || layers[2].Name != "extra" {
		t.Fatalf("layers = %+v", layers)
	}
	out, err := Compose(Input{Subscription: []byte(testSub), Overrides: layers, Managed: Managed{MixedPort: 7890, LogLevel: "info"}})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	if m["log-level"] != "info" {
		t.Errorf("managed log-level wins, got %v", m["log-level"])
	}
	rules, _ := m["rules"].([]any)
	if len(rules) == 0 || rules[len(rules)-1] != "DOMAIN,x.com,DIRECT" {
		t.Errorf("extra layer should append a rule, got %v", rules)
	}
	srcs := map[string]bool{}
	for _, l := range out.Lines {
		srcs[l.Source] = true
	}
	if !srcs["extra"] {
		t.Errorf("lines should be attributed to the entry's name, sources = %v", srcs)
	}
	// A bad layer names itself in the error.
	_, err = Compose(Input{Subscription: []byte(testSub), Overrides: []Layer{{Name: "broken", Text: "- a\n"}}})
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("error should name the layer: %v", err)
	}
	// Clearing deletes the entry.
	if err := s.SetOverride("global", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetOverride("global"); ok {
		t.Error("empty text must delete the entry")
	}
	_ = extra
}

func TestFetchOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.yaml":
			_, _ = w.Write([]byte("# from server\nlog-level: warning\n"))
		case "/list.yaml":
			_, _ = w.Write([]byte("- not\n- a mapping\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	text, err := FetchOverride(context.Background(), srv.Client(), srv.URL+"/ok.yaml")
	if err != nil || text != "# from server\nlog-level: warning\n" {
		t.Fatalf("fetch: %q %v", text, err)
	}
	if _, err := FetchOverride(context.Background(), srv.Client(), srv.URL+"/list.yaml"); err == nil || !strings.Contains(err.Error(), "YAML 覆写") {
		t.Errorf("a list is not an override: %v", err)
	}
	if _, err := FetchOverride(context.Background(), srv.Client(), srv.URL+"/missing.yaml"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404 should be reported: %v", err)
	}
	if _, err := FetchOverride(context.Background(), srv.Client(), "ftp://x"); err == nil {
		t.Error("non-http links are rejected")
	}
}

func TestFetchParsesHomePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Profile-Web-Page-Url", "https://portal.example.com/")
		_, _ = w.Write([]byte("proxies:\n  - {name: a, type: ss, server: 1.1.1.1, port: 1, cipher: aes-128-gcm, password: p}\n"))
	}))
	defer srv.Close()
	f, err := Fetch(context.Background(), srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.HomePage != "https://portal.example.com/" {
		t.Errorf("HomePage = %q", f.HomePage)
	}
}
