package app

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/model"
)

// TestGroupDelayProgressive checks that a group test reports each node as
// it finishes (docs/design.md §5.2 组内测速: 结果逐个出现) instead of all at
// the end, and that nested groups and DIRECT/REJECT are not tested.
func TestGroupDelayProgressive(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var mu sync.Mutex
	var asked []string
	h.ctrl.mu.Lock()
	h.ctrl.delayFn = func(name string) (int, error) {
		mu.Lock()
		asked = append(asked, name)
		mu.Unlock()
		switch name {
		case "东京 03":
			<-release // keeps one node pending while the others finish
			return 0, errors.New("timeout")
		case "美国 05":
			return 250, nil
		}
		return 80, nil
	}
	h.ctrl.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- h.s.TestGroupDelay("节点选择") }()

	// 香港 01 and 美国 05 finish at once and must show up while 东京 03 is
	// still being tested.
	if !waitFor(func() bool {
		px := h.s.Proxies()
		hk, us, jp := px.Proxies["香港 01"], px.Proxies["美国 05"], px.Proxies["东京 03"]
		return hk.Delay == 80 && !hk.Testing && us.Delay == 250 && !us.Testing && jp.Testing
	}) {
		t.Fatalf("results did not appear one by one: %+v", h.s.Proxies().Proxies)
	}
	select {
	case err := <-done:
		t.Fatalf("TestGroupDelay returned before every node finished: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("TestGroupDelay: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TestGroupDelay did not return")
	}
	px := h.s.Proxies()
	if jp := px.Proxies["东京 03"]; jp.Testing || jp.Delay != -1 {
		t.Fatalf("failed node should show 超时 (-1) and not testing: %+v", jp)
	}
	for n, p := range px.Proxies {
		if p.Testing {
			t.Fatalf("%s still marked testing after the group test", n)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(asked)
	if want := []string{"东京 03", "美国 05", "香港 01"}; !slices.Equal(asked, want) {
		t.Fatalf("tested %v, want only the group's nodes %v", asked, want)
	}
	if err := h.s.TestGroupDelay("不存在"); err == nil {
		t.Fatal("unknown group should fail")
	}
}

// TestAllDelaysProgressive checks 全部测速 goes node by node as well and
// leaves no testing flag behind.
func TestAllDelaysProgressive(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var asked []string
	h.ctrl.mu.Lock()
	h.ctrl.delayFn = func(name string) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, name)
		return 10 * (len(asked) + 1), nil
	}
	h.ctrl.mu.Unlock()
	if err := h.s.TestAllDelays(); err != nil {
		t.Fatal(err)
	}
	px := h.s.Proxies()
	for _, n := range []string{"香港 01", "东京 03", "美国 05"} {
		if p := px.Proxies[n]; p.Testing || p.Delay <= 0 {
			t.Fatalf("%s after 全部测速: %+v", n, p)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(asked)
	if want := []string{"东京 03", "美国 05", "香港 01"}; !slices.Equal(asked, want) {
		t.Fatalf("tested %v, want %v", asked, want)
	}
}

func TestProxyProviders(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.ctrl.pprovs = map[string]controller.ProxyProvider{
			"机场": {Name: "机场", Type: "Proxy", VehicleType: "HTTP", TestURL: "https://www.gstatic.com/generate_204",
				Proxies:          []controller.Proxy{{Name: "香港 01"}, {Name: "东京 03"}},
				UpdatedAt:        time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
				SubscriptionInfo: &controller.SubscriptionInfo{Upload: 1, Download: 2, Total: 10, Expire: 1800000000}},
			"自建": {Name: "自建", Type: "Proxy", VehicleType: "File", Proxies: []controller.Proxy{{Name: "美国 05"}}},
		}
	})
	if h.s.ProxyProviders() != nil && len(h.s.ProxyProviders()) != 0 {
		t.Fatal("providers before start")
	}
	if err := h.s.UpdateProxyProvider("机场"); !errors.Is(err, errCoreNotRunning) {
		t.Fatalf("update while stopped: %v", err)
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	provs := h.s.ProxyProviders()
	if len(provs) != 2 || provs[0].Name != "机场" || provs[1].Name != "自建" { // sorted by name
		t.Fatalf("providers: %+v", provs)
	}
	ap := provs[0]
	if !slices.Equal(ap.Proxies, []string{"香港 01", "东京 03"}) || ap.VehicleType != "HTTP" || ap.Total != 10 || ap.Download != 2 ||
		ap.Expire.Unix() != 1800000000 || ap.UpdatedAt.Year() != 2026 || ap.TestURL == "" {
		t.Fatalf("provider fields: %+v", ap)
	}
	if provs[1].Total != 0 || !provs[1].Expire.IsZero() {
		t.Fatalf("provider without subscription info: %+v", provs[1])
	}
	if err := h.s.UpdateProxyProvider("机场"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.HealthCheckProvider("自建"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.UpdateProxyProvider("没有"); err == nil {
		t.Fatal("unknown provider should fail")
	}
	if err := h.s.HealthCheckProvider("没有"); err == nil {
		t.Fatal("unknown provider should fail")
	}
	h.ctrl.mu.Lock()
	defer h.ctrl.mu.Unlock()
	if !slices.Equal(h.ctrl.pupdated, []string{"机场"}) || !slices.Equal(h.ctrl.pchecked, []string{"自建"}) {
		t.Fatalf("controller calls: updated %v checked %v", h.ctrl.pupdated, h.ctrl.pchecked)
	}
	for _, p := range h.s.ProxyProviders() {
		if p.Updating || p.Checking {
			t.Fatalf("busy flag left behind: %+v", p)
		}
	}
}

// TestUIStatePersists checks the expanded-group state survives a restart
// (docs/design.md §5.2 展开状态) without touching the rest of the settings.
func TestUIStatePersists(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.s.UIState(); len(got.OpenGroups) != 0 || got.ProxyGrid {
		t.Fatalf("fresh UI state: %+v", got)
	}
	u := model.UIState{OpenGroups: map[string]bool{"节点选择": false, "流媒体": true}, ProxyGrid: true,
		ProxySort: "delay", ProxyHideTimeout: true, ProxyProvidersOpen: true}
	if err := h.s.SaveUIState(u); err != nil {
		t.Fatal(err)
	}
	u.OpenGroups["流媒体"] = false // the service keeps its own copy
	got := h.s.UIState()
	if !got.ProxyGrid || !got.OpenGroups["流媒体"] || got.OpenGroups["节点选择"] {
		t.Fatalf("UIState after save: %+v", got)
	}
	set, err := loadSettings(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if !set.UI.ProxyGrid || !set.UI.OpenGroups["流媒体"] || set.MixedPort != 7890 ||
		set.UI.ProxySort != "delay" || !set.UI.ProxyHideTimeout || !set.UI.ProxyProvidersOpen {
		t.Fatalf("settings on disk: %+v", set.UI)
	}
	if h.ctrl.reloads != 0 {
		t.Fatal("saving UI state must not reload the core")
	}

	// A settings page saving the snapshot it opened with (older UI state)
	// keeps the UI state chosen since.
	stale := h.s.Settings()
	stale.UI = model.UIState{}
	stale.MixedPort = 7891
	if err := h.s.SaveSettings(stale); err != nil {
		t.Fatal(err)
	}
	if got := h.s.UIState(); got.ProxySort != "delay" || !got.ProxyGrid {
		t.Fatalf("SaveSettings rolled back the UI state: %+v", got)
	}
	if set, _ := loadSettings(h.dir); set.UI.ProxySort != "delay" || set.MixedPort != 7891 {
		t.Fatalf("on disk after SaveSettings: port %d ui %+v", set.MixedPort, set.UI)
	}
}
