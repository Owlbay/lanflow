package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// Concurrent prepares (an import's check racing a reload or an update)
// each get their own temp file: none sees another's content or loses its
// file, and nothing is left behind.
func TestConcurrentPrepareUsesOwnTempFile(t *testing.T) {
	h := newHarness(t, nil)
	var ids []string
	for i := 0; i < 8; i++ {
		sub, _ := h.store.Put(model.Subscription{Name: fmt.Sprintf("s%d", i), Source: "url", URL: "https://example.com"})
		_ = h.store.SetContent(sub.ID, []byte(fmt.Sprintf("proxies: [p%d]\n", i)))
		ids = append(ids, sub.ID)
	}
	var wg sync.WaitGroup
	names := make([]string, len(ids))
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			p, err := h.s.prepare(composeArgs{subID: id})
			if err != nil {
				t.Error(err)
				return
			}
			got, err := os.ReadFile(p.tmp)
			if err != nil || !bytes.Contains(got, []byte(fmt.Sprintf("p%d]", i))) {
				t.Errorf("prepare %d: temp file %q content %q err %v", i, p.tmp, got, err)
			}
			names[i] = p.tmp
			p.discard()
		}(i, id)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("temp file %s shared", n)
		}
		seen[n] = true
	}
	ents, _ := os.ReadDir(filepath.Dir(h.s.runtime))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") || strings.HasSuffix(e.Name(), ".new") {
			t.Fatalf("leftover %s", e.Name())
		}
	}
}

// Updating the current subscription while running validates once (the
// prepared file is the one reloaded), and a concurrent update of another
// subscription does not disturb it.
func TestUpdateCurrentValidatesOnce(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	other, _ := h.store.Put(model.Subscription{Name: "other", Source: "url", URL: "https://example.com/o"})
	_ = h.store.SetContent(other.ID, []byte("proxies: []\n"))
	cur, _ := h.store.Current()
	h.prof.fetched = profile.Fetched{Data: []byte("proxies: [new]\n")}
	h.core.mu.Lock()
	h.core.validated = nil
	h.core.mu.Unlock()
	h.ctrl.mu.Lock()
	reloads := h.ctrl.reloads
	h.ctrl.mu.Unlock()

	var wg sync.WaitGroup
	for _, id := range []string{cur.ID, other.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := h.s.UpdateSubscription(id); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	h.core.mu.Lock()
	n := len(h.core.validated)
	h.core.mu.Unlock()
	if n != 2 { // one per subscription, not two for the current one
		t.Fatalf("validated %d times, want 2", n)
	}
	h.ctrl.mu.Lock()
	got := h.ctrl.reloads - reloads
	h.ctrl.mu.Unlock()
	if got != 1 {
		t.Fatalf("reloads = %d, want 1", got)
	}
	data, _ := os.ReadFile(h.s.runtime)
	if !bytes.Contains(data, []byte("proxies: [new]")) {
		t.Fatalf("runtime config not the updated one: %q", data)
	}
	if _, err := os.Stat(h.s.runtime + ".bak"); err != nil {
		t.Fatalf("no backup of the previous config: %v", err)
	}
}

// A failed reload restores the previous runtime file in one rename.
func TestReloadFailureRollsBack(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(h.s.runtime)
	h.ctrl.mu.Lock()
	h.ctrl.reloadFn = func(string) error { return fmt.Errorf("boom") }
	h.ctrl.mu.Unlock()
	cur, _ := h.store.Current()
	h.prof.fetched = profile.Fetched{Data: []byte("proxies: [changed]\n")}
	if err := h.s.UpdateSubscription(cur.ID); err == nil {
		t.Fatal("want reload error")
	}
	after, _ := os.ReadFile(h.s.runtime)
	if !bytes.Equal(before, after) {
		t.Fatalf("runtime config not rolled back:\n%s\n---\n%s", before, after)
	}
}
