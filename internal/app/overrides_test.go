package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// composedLayers returns the layer names of the last composition.
func composedLayers(h *harness) []string {
	h.prof.mu.Lock()
	defer h.prof.mu.Unlock()
	if len(h.prof.inputs) == 0 {
		return nil
	}
	var out []string
	for _, l := range h.prof.inputs[len(h.prof.inputs)-1].Overrides {
		out = append(out, l.Name)
	}
	return out
}

// Entries are validated with the core before they are stored, apply in
// list order, and toggling or deleting one reloads the running core.
func TestOverrideEntriesValidateOrderAndReload(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	cur, _ := h.store.Current()

	// A rejected global entry is not stored.
	if _, err := h.s.SaveOverrideEntry(model.Override{Name: "bad", Enabled: true, Global: true, Text: "BAD: 1\n"}); err == nil {
		t.Fatal("compose failure must reject the entry")
	}
	if len(h.s.OverrideList()) != 0 {
		t.Fatal("rejected entry must not be stored")
	}
	// A nameless one is refused before anything runs.
	if _, err := h.s.SaveOverrideEntry(model.Override{Enabled: true, Text: "a: 1\n"}); err == nil {
		t.Fatal("a name is required")
	}

	reloads := func() int { h.ctrl.mu.Lock(); defer h.ctrl.mu.Unlock(); return h.ctrl.reloads }
	before := reloads()
	first, err := h.s.SaveOverrideEntry(model.Override{Name: "first", Enabled: true, Global: true, Text: "log-level: warning\n"})
	if err != nil || first.ID == "" {
		t.Fatalf("save: %+v %v", first, err)
	}
	if reloads() != before+1 {
		t.Fatalf("a global entry must reload the core: reloads %d → %d", before, reloads())
	}
	if got := composedLayers(h); len(got) != 1 || got[0] != "first" {
		t.Fatalf("layers = %v", got)
	}

	// A non-global entry nobody selects is syntax-checked only and does not reload.
	before = reloads()
	second, err := h.s.SaveOverrideEntry(model.Override{Name: "second", Enabled: true, Text: "mode: global\n"})
	if err != nil {
		t.Fatal(err)
	}
	if reloads() != before {
		t.Fatal("an unselected entry must not reload")
	}
	if _, err := h.s.SaveOverrideEntry(model.Override{Name: "broken", Enabled: true, Text: "- list\n"}); err == nil {
		t.Fatal("an unselected entry still needs valid YAML")
	}

	// Selecting it for the current subscription validates and reloads.
	cur.Overrides = []string{second.ID}
	before = reloads()
	if err := h.s.EditSubscription(cur); err != nil {
		t.Fatal(err)
	}
	if reloads() != before+1 {
		t.Fatal("a changed selection must reload")
	}
	if got := composedLayers(h); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("layers = %v", got)
	}
	// Moving second above first changes the order.
	if err := h.s.MoveOverrideEntry(second.ID, -1); err != nil {
		t.Fatal(err)
	}
	if got := composedLayers(h); len(got) != 2 || got[0] != "second" || got[1] != "first" {
		t.Fatalf("layers after move = %v", got)
	}
	// Disabling drops it from the composition; enabling with bad text is refused.
	second.Enabled = false
	if _, err := h.s.SaveOverrideEntry(second); err != nil {
		t.Fatal(err)
	}
	if got := composedLayers(h); len(got) != 1 || got[0] != "first" {
		t.Fatalf("layers after disable = %v", got)
	}
	second.Enabled, second.Text = true, "BAD: 1\n"
	if _, err := h.s.SaveOverrideEntry(second); err == nil {
		t.Fatal("enabling a selected entry with bad text must fail")
	}
	if o, _ := h.store.GetOverride(second.ID); o.Enabled || o.Text != "mode: global\n" {
		t.Fatalf("rejected change must not be stored: %+v", o)
	}
	// Deleting removes the selection and reloads.
	before = reloads()
	if err := h.s.DeleteOverrideEntry(first.ID); err != nil {
		t.Fatal(err)
	}
	if reloads() != before+1 {
		t.Fatal("deleting a global entry must reload")
	}
	if len(h.s.OverrideList()) != 1 || h.s.Overrides().Global != "" {
		t.Fatalf("list after delete = %+v", h.s.OverrideList())
	}
	// Legacy keys still work and show up in the legacy views.
	if err := h.s.SaveOverride("global", "ipv6: true\n"); err != nil {
		t.Fatal(err)
	}
	if ov := h.s.Overrides(); ov.Global != "ipv6: true\n" || len(ov.List) != 2 {
		t.Fatalf("legacy global view: %+v", ov)
	}
	if err := h.s.SaveOverride(cur.ID, "mode: rule\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := h.store.Get(cur.ID)
	if h.s.Overrides().PerSub[cur.ID] != "mode: rule\n" || !hasID(got.Overrides, cur.ID) {
		t.Fatalf("legacy per-subscription entry: %+v sel=%v", h.s.Overrides(), got.Overrides)
	}
}

// A URL entry is downloaded through the subscription client on save and
// on UpdateOverrideEntry; a server error leaves the stored text alone.
func TestOverrideURLUpdate(t *testing.T) {
	var body atomicText
	body.set("log-level: warning\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(body.get()))
	}))
	defer srv.Close()
	h := newHarness(t, nil)
	if _, err := h.s.SaveOverrideEntry(model.Override{Name: "remote", Enabled: true, Global: true, Source: model.OverrideURL, URL: "ftp://x"}); err == nil {
		t.Fatal("a non-http link is refused")
	}
	o, err := h.s.SaveOverrideEntry(model.Override{Name: "remote", Enabled: true, Global: true, Source: model.OverrideURL, URL: srv.URL + "/ov.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Text != "log-level: warning\n" || o.UpdatedAt.IsZero() {
		t.Fatalf("downloaded entry = %+v", o)
	}
	body.set("log-level: error\n")
	if err := h.s.UpdateOverrideEntry(o.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.store.GetOverride(o.ID); got.Text != "log-level: error\n" {
		t.Fatalf("update did not land: %+v", got)
	}
	// Bad content from the server is rejected, the old text stays.
	body.set("BAD: 1\n")
	if err := h.s.UpdateOverrideEntry(o.ID); err == nil {
		t.Fatal("content failing validation must be refused")
	}
	if got, _ := h.store.GetOverride(o.ID); got.Text != "log-level: error\n" {
		t.Fatalf("rejected update must not be stored: %+v", got)
	}
	o.URL = srv.URL + "/fail"
	o.Text = "x: 1\n"
	if _, err := h.s.SaveOverrideEntry(o); err != nil {
		t.Fatal(err)
	}
	if err := h.s.UpdateOverrideEntry(o.ID); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("server error should be reported: %v", err)
	}
	local, _ := h.s.SaveOverrideEntry(model.Override{Name: "local", Enabled: true, Text: "a: 1\n"})
	if err := h.s.UpdateOverrideEntry(local.ID); err == nil {
		t.Fatal("a local entry cannot be updated from a link")
	}
}

// atomicText is a string guarded for the test server.
type atomicText struct {
	mu sync.Mutex
	s  string
}

func (a *atomicText) set(s string) { a.mu.Lock(); a.s = s; a.mu.Unlock() }
func (a *atomicText) get() string  { a.mu.Lock(); defer a.mu.Unlock(); return a.s }

// The export carries the list (minus per-subscription entries unless the
// subscriptions travel too) and the legacy views; importing merges new
// entries by name and text and reads files of the old format.
func TestOverrideListExportImport(t *testing.T) {
	src := newHarness(t, nil)
	cur, _ := src.store.Current()
	if err := src.s.SaveOverride("global", "log-level: warning\n"); err != nil {
		t.Fatal(err)
	}
	if err := src.s.SaveOverride(cur.ID, "mode: global\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.s.SaveOverrideEntry(model.Override{Name: "extra", Enabled: true, Text: "ipv6: true\n"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lanflow.yaml")
	if err := src.s.ExportSettings(path, false); err != nil {
		t.Fatal(err)
	}
	ex := readDoc(t, path)
	if len(ex.Overrides) != 2 || ex.Overrides[0].ID != "global" || ex.Overrides[0].Text != "log-level: warning\n" || len(ex.Subscriptions) != 0 {
		t.Fatalf("the subscription's entry must stay home without its subscription: %+v", ex.Overrides)
	}
	if err := src.s.ExportSettings(path, true); err != nil {
		t.Fatal(err)
	}
	ex = readDoc(t, path)
	if len(ex.Overrides) != 3 || len(ex.Subscriptions) != 1 {
		t.Fatalf("with subscriptions: %+v", ex.Overrides)
	}

	dst := newHarness(t, nil)
	summary, err := dst.s.ImportSettings(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(summary, "全局覆写") || !strings.Contains(summary, "1 条覆写") {
		t.Fatalf("summary = %q", summary)
	}
	list := dst.s.OverrideList()
	names := map[string]model.Override{}
	for _, o := range list {
		names[o.Name] = o
	}
	if dst.s.Overrides().Global != "log-level: warning\n" || names["extra"].Text != "ipv6: true\n" || !names["extra"].Enabled {
		t.Fatalf("imported list = %+v", list)
	}
	// The fake store hands both harnesses the same subscription ID, so the
	// per-subscription entry lands on dst's subscription as a legacy
	// subscription override (real stores use random IDs and skip it).
	if len(list) != 3 || !strings.Contains(summary, "1 条订阅覆写") || dst.s.Overrides().PerSub[cur.ID] != "mode: global\n" {
		t.Fatalf("subscription entry: %q %+v", summary, list)
	}
	// Importing again adds nothing.
	summary, err = dst.s.ImportSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(summary, "条覆写") || len(dst.s.OverrideList()) != 3 {
		t.Fatalf("second import: %q %+v", summary, dst.s.OverrideList())
	}

	// An old-format file (single texts only) still imports.
	old := `{"app":"lanflow","format":1,"overrides":{"Global":"log-level: debug\n","PerSub":{}}}`
	p := filepath.Join(t.TempDir(), "old.json")
	_ = os.WriteFile(p, []byte(old), 0o600)
	summary, err = dst.s.ImportSettings(p)
	if err != nil || !strings.Contains(summary, "全局覆写") {
		t.Fatalf("old format: %q %v", summary, err)
	}
	if dst.s.Overrides().Global != "log-level: debug\n" {
		t.Fatal("old-format global text not applied")
	}
}

// Delay tests use Settings.DelayURL / DelayTimeoutMs; invalid values fall
// back to the defaults.
func TestDelaySettingsUsedByTests(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	set := h.s.Settings()
	if set.DelayURL != defaultDelayURL || set.DelayTimeoutMs != 5000 {
		t.Fatalf("defaults: %q %d", set.DelayURL, set.DelayTimeoutMs)
	}
	set.DelayURL, set.DelayTimeoutMs = "https://cp.cloudflare.com/generate_204", 8000
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if err := h.s.TestProxyDelay("香港 01"); err != nil {
		t.Fatal(err)
	}
	h.ctrl.mu.Lock()
	last := h.ctrl.delays[len(h.ctrl.delays)-1]
	h.ctrl.mu.Unlock()
	if last[0] != "https://cp.cloudflare.com/generate_204" || last[1] != "8s" {
		t.Fatalf("delay params = %v", last)
	}
	set.DelayURL, set.DelayTimeoutMs = "not a url", 99
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if got := h.s.Settings(); got.DelayURL != defaultDelayURL || got.DelayTimeoutMs != 5000 {
		t.Fatalf("invalid values must normalize to the defaults: %q %d", got.DelayURL, got.DelayTimeoutMs)
	}
}

// With 切换节点时断开该组的连接 on, SelectProxy closes the connections whose
// chain goes through the group.
func TestSelectProxyDisconnectsGroup(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	seed := func() {
		h.s.mu.Lock()
		h.s.state.conns = []model.Connection{
			{ID: "c1", Chains: []string{"节点选择", "东京 03"}},
			{ID: "c2", Chains: []string{"流媒体", "节点选择", "东京 03"}},
			{ID: "c3", Chains: []string{"DIRECT"}},
		}
		h.s.mu.Unlock()
	}
	closed := func() []string {
		h.ctrl.mu.Lock()
		defer h.ctrl.mu.Unlock()
		return append([]string(nil), h.ctrl.closedIDs...)
	}
	seed()
	if err := h.s.SelectProxy("节点选择", "香港 01"); err != nil {
		t.Fatal(err)
	}
	if len(closed()) != 0 {
		t.Fatal("off by default: nothing closed")
	}
	set := h.s.Settings()
	set.DisconnectOnSwitch = true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	seed()
	if err := h.s.SelectProxy("节点选择", "东京 03"); err != nil {
		t.Fatal(err)
	}
	if got := closed(); len(got) != 2 || got[0] != "c1" || got[1] != "c2" {
		t.Fatalf("closed = %v", got)
	}
}

// UIState keeps the logs page's filter and the subscription's extras round-trip.
func TestUIStateAndSubscriptionExtras(t *testing.T) {
	h := newHarness(t, nil)
	u := h.s.UIState()
	u.LogLevel, u.LogSource = "warning", "app"
	if err := h.s.SaveUIState(u); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(h.dir)
	if err != nil || got.UI.LogLevel != "warning" || got.UI.LogSource != "app" {
		t.Fatalf("UI state not persisted: %+v %v", got.UI, err)
	}
	cur, _ := h.store.Current()
	cur.TimeoutSec = 45
	if err := h.s.EditSubscription(cur); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.store.Get(cur.ID); got.TimeoutSec != 45 || subTimeout(got) != 45*time.Second {
		t.Fatalf("timeout = %+v", got)
	}
	cur.TimeoutSec = 10000
	if err := h.s.EditSubscription(cur); err == nil {
		t.Fatal("an absurd timeout is refused")
	}
	if subTimeout(model.Subscription{}) != defaultSubTimeout {
		t.Fatal("zero means the default")
	}
	// The home page from the download lands on the record.
	h.prof.fetched = profile.Fetched{Data: []byte("proxies: []\n"), HomePage: "https://portal.example.com"}
	if err := h.s.UpdateSubscription(cur.ID); err != nil && !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if got, _ := h.store.Get(cur.ID); got.HomePage != "https://portal.example.com" {
		t.Fatalf("home page = %q", got.HomePage)
	}
}
