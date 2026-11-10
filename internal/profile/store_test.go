package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/model"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "profile")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestStoreLayoutAndPermissions(t *testing.T) {
	s, dir := openTestStore(t)
	sub, err := s.Put(model.Subscription{Name: "A", Source: "url", URL: "https://x/sub?token=t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetContent(sub.ID, []byte(validSub)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOverride(sub.ID, "a: 1\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOverride("global", "b: 2\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCustomRules([]model.CustomRule{{Type: "DOMAIN", Payload: "a", Target: "DIRECT"}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"profiles.json", "custom-rules.json", "overrides.json",
		filepath.Join("profiles", sub.ID+".yaml"),
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("expected %s: %v", p, err)
		}
	}
	if runtime.GOOS != "windows" {
		for _, d := range []string{dir, filepath.Join(dir, "profiles"), filepath.Join(dir, "overrides")} {
			if m := mode(t, d); m != 0o700 {
				t.Errorf("%s mode %o, want 0700", d, m)
			}
		}
		for _, f := range []string{"profiles.json", "custom-rules.json", "overrides.json", filepath.Join("profiles", sub.ID+".yaml")} {
			if m := mode(t, filepath.Join(dir, f)); m != 0o600 {
				t.Errorf("%s mode %o, want 0600", f, m)
			}
		}
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
	// Index JSON has no runtime flags and carries current separately.
	raw, _ := os.ReadFile(filepath.Join(dir, "profiles.json"))
	var idx map[string]any
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx["current"]; !ok {
		t.Error("index lacks current")
	}
	subs, _ := idx["subscriptions"].([]any)
	if len(subs) != 1 {
		t.Fatalf("index subs = %v", subs)
	}
	for k := range subs[0].(map[string]any) {
		if strings.EqualFold(k, "updating") || strings.EqualFold(k, "current") {
			t.Errorf("runtime flag %q persisted", k)
		}
	}
	if len(sub.ID) != 8 {
		t.Errorf("ID %q should be 8 hex chars", sub.ID)
	}
}

func TestStoreCRUD(t *testing.T) {
	s, dir := openTestStore(t)
	if _, ok := s.Current(); ok {
		t.Error("empty store has no current")
	}
	if len(s.List()) != 0 {
		t.Error("empty store has no subs")
	}
	a, err := s.Put(model.Subscription{Name: "A", Source: "url", URL: "https://a", Updating: true})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Updating {
		t.Error("Updating flag should be echoed back to the caller")
	}
	b, _ := s.Put(model.Subscription{Name: "B", Source: "file", FilePath: "/tmp/b.yaml", AutoUpdateHours: 6, UserAgent: "ua", ViaProxy: true})
	if a.ID == b.ID {
		t.Fatal("IDs must differ")
	}
	if a.Current || b.Current {
		t.Error("nothing is current yet")
	}
	if err := s.SetCurrent(b.ID); err != nil {
		t.Fatal(err)
	}
	cur, ok := s.Current()
	if !ok || cur.ID != b.ID || !cur.Current {
		t.Errorf("current = %+v", cur)
	}
	if got, _ := s.Get(a.ID); got.Current {
		t.Error("a must not be current")
	}
	if err := s.SetCurrent("nope"); err == nil || !strings.Contains(err.Error(), "订阅不存在") {
		t.Errorf("expected 订阅不存在, got %v", err)
	}

	// Update keeps position and current flag.
	exp := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	b.Name = "B2"
	b.Upload, b.Download, b.Total, b.Expire = 1, 2, 3, exp
	b.LastError = "x"
	if _, err := s.Put(b); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 2 || list[0].ID != a.ID || list[1].Name != "B2" || !list[1].Current || !list[1].Expire.Equal(exp) || list[1].Total != 3 {
		t.Errorf("list = %+v", list)
	}
	// Put with an unknown ID appends.
	if _, err := s.Put(model.Subscription{ID: "deadbeef", Name: "C"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Get("deadbeef"); !ok || got.Name != "C" {
		t.Errorf("get deadbeef = %+v %v", got, ok)
	}
	if _, err := s.Put(model.Subscription{ID: "../evil", Name: "C"}); err == nil {
		t.Error("path-like ID must be rejected")
	}

	// Content and overrides.
	if _, err := s.Content(a.ID); err == nil || !strings.Contains(err.Error(), "尚未下载") {
		t.Errorf("missing content error: %v", err)
	}
	if err := s.SetContent(a.ID, []byte(validSub)); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Content(a.ID); err != nil || string(c) != validSub {
		t.Errorf("content = %q %v", c, err)
	}
	if s.Override("global") != "" || s.Override(a.ID) != "" {
		t.Error("overrides should start empty")
	}
	if err := s.SetOverride(a.ID, "x: 1\n"); err != nil {
		t.Fatal(err)
	}
	if s.Override(a.ID) != "x: 1\n" {
		t.Error("override not stored")
	}
	if err := s.SetOverride(a.ID, ""); err != nil {
		t.Fatal(err)
	}
	if s.Override(a.ID) != "" {
		t.Error("empty override must delete")
	}
	if _, err := os.Stat(filepath.Join(dir, "overrides", a.ID+".yaml")); !os.IsNotExist(err) {
		t.Error("override file should be removed")
	}
	if err := s.SetOverride("../x", "a"); err == nil {
		t.Error("bad override key must be rejected")
	}
	if s.Override("../../etc/passwd") != "" {
		t.Error("bad override key must read as empty")
	}

	// Delete removes files and clears current.
	if err := s.SetOverride(b.ID, "y: 1\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetContent(b.ID, []byte("z")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(b.ID); ok {
		t.Error("deleted sub still present")
	}
	if _, ok := s.Current(); ok {
		t.Error("current must be cleared after deleting it")
	}
	for _, p := range []string{filepath.Join("profiles", b.ID+".yaml"), filepath.Join("overrides", b.ID+".yaml")} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed", p)
		}
	}
	if err := s.Delete(b.ID); err == nil {
		t.Error("deleting twice must fail")
	}

	// Custom rules: IDs assigned, order kept, copies returned.
	rules, err := s.SetCustomRules([]model.CustomRule{{Type: "DOMAIN", Payload: "a", Target: "DIRECT"}, {ID: "keep", Type: "DOMAIN", Payload: "b", Target: "REJECT"}})
	if err != nil {
		t.Fatal(err)
	}
	if rules[0].ID == "" || rules[1].ID != "keep" || rules[0].ID == rules[1].ID {
		t.Errorf("rules = %+v", rules)
	}
	got := s.CustomRules()
	got[0].Payload = "mutated"
	if s.CustomRules()[0].Payload != "a" {
		t.Error("CustomRules must return a copy")
	}
	if r, _ := s.SetCustomRules(nil); len(r) != 0 || len(s.CustomRules()) != 0 {
		t.Error("nil rules should clear")
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	s, dir := openTestStore(t)
	a, _ := s.Put(model.Subscription{Name: "A", Source: "url", URL: "https://a"})
	_, _ = s.Put(model.Subscription{Name: "B", Source: "url", URL: "https://b"})
	_ = s.SetCurrent(a.ID)
	_ = s.SetContent(a.ID, []byte(validSub))
	_ = s.SetOverride("global", "g: 1\n")
	_, _ = s.SetCustomRules([]model.CustomRule{{Type: "DOMAIN", Payload: "a", Target: "DIRECT"}})

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	list := s2.List()
	if len(list) != 2 || list[0].Name != "A" || !list[0].Current || list[1].Current {
		t.Errorf("reopened list = %+v", list)
	}
	if cur, ok := s2.Current(); !ok || cur.ID != a.ID {
		t.Errorf("current lost: %+v %v", cur, ok)
	}
	if c, _ := s2.Content(a.ID); string(c) != validSub {
		t.Error("content lost")
	}
	if s2.Override("global") != "g: 1\n" {
		t.Error("override lost")
	}
	if r := s2.CustomRules(); len(r) != 1 || r[0].ID == "" {
		t.Errorf("rules lost: %+v", r)
	}

	// A corrupt index is reported, not silently reset.
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "已损坏") {
		t.Errorf("corrupt index: got %v", err)
	}
	// A dangling current pointer is cleared.
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte(`{"current":"gone","subscriptions":[{"id":"abcd1234","name":"X"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s3, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s3.Current(); ok {
		t.Error("dangling current should be cleared")
	}
}

func TestStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	if err := atomicWrite(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	// A write into a missing directory fails and leaves nothing behind.
	if err := atomicWrite(filepath.Join(dir, "missing", "f"), []byte("x")); err == nil {
		t.Error("expected error")
	}
	if b, _ := os.ReadFile(path); string(b) != "one" {
		t.Errorf("content = %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("stray files: %v", entries)
	}
}

func TestStoreConcurrency(t *testing.T) {
	s, dir := openTestStore(t)
	var wg sync.WaitGroup
	const workers = 8
	const iters = 25
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				sub, err := s.Put(model.Subscription{Name: fmt.Sprintf("w%d-%d", w, i), Source: "url"})
				if err != nil {
					t.Error(err)
					return
				}
				if err := s.SetContent(sub.ID, []byte(validSub)); err != nil {
					t.Error(err)
					return
				}
				if err := s.SetOverride(sub.ID, "a: 1\n"); err != nil {
					t.Error(err)
					return
				}
				_ = s.SetOverride("global", fmt.Sprintf("w: %d\n", w))
				_ = s.SetCurrent(sub.ID)
				_, _ = s.SetCustomRules([]model.CustomRule{{Type: "DOMAIN", Payload: fmt.Sprint(w), Target: "DIRECT"}})
				_ = s.List()
				_, _ = s.Current()
				_ = s.Override("global")
				_ = s.CustomRules()
				if i%5 == 4 {
					if err := s.Delete(sub.ID); err != nil {
						t.Error(err)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	want := workers * iters * 4 / 5
	if got := len(s.List()); got != want {
		t.Errorf("subs = %d, want %d", got, want)
	}
	// Everything on disk is consistent and readable by a fresh store.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.List()) != want {
		t.Errorf("reopened subs = %d", len(s2.List()))
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
	for _, sub := range s2.List() {
		if _, err := s2.Content(sub.ID); err != nil {
			t.Errorf("content of %s: %v", sub.ID, err)
		}
	}
}
