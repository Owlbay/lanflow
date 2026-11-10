package app

import (
	"errors"
	"lanflow/internal/platform/sysproxy"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/model"
)

func TestSettingsDefaultsAndPersistence(t *testing.T) {
	h := newHarness(t, nil)
	set := h.s.Settings()
	if set.MixedPort != 7890 || set.Mode != model.ModeRule || !set.CloseToTray || !set.ProbeExits || !set.Sniffer.Enable {
		t.Fatalf("unexpected defaults: %+v", set)
	}
	if set.Geo.Mode != "db" || set.Geo.IntervalHours != 24 || len(set.Sites) != 7 || set.Sites[6].Name != "百度" {
		t.Fatalf("unexpected geo/site defaults: %+v %+v", set.Geo, set.Sites)
	}
	set.MixedPort = 7891
	set.AllowLAN = true
	set.CloseToTray = false
	set.DNS.TakeOver = true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	path := filepath.Join(h.dir, settingsFile)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("settings.json missing: %v", err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("settings.json mode = %o, want 600", st.Mode().Perm())
	}
	got, err := loadSettings(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.MixedPort != 7891 || !got.AllowLAN || got.CloseToTray || !got.DNS.TakeOver {
		t.Fatalf("reloaded settings mismatch: %+v", got)
	}
	// a field missing from the file keeps its default
	if err := os.WriteFile(path, []byte(`{"MixedPort": 1080}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ = loadSettings(h.dir)
	if got.MixedPort != 1080 || !got.CloseToTray || got.LogLevel != "info" || len(got.Sites) != 7 {
		t.Fatalf("partial file did not keep defaults: %+v", got)
	}
	if h.s.Status().MixedPort != 7891 {
		t.Fatalf("status port not updated while stopped")
	}
}

func TestRecoveryDetection(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.sys.state.Enabled, h.sys.state.Host, h.sys.state.Port = true, "127.0.0.1", 7890
		_ = os.MkdirAll(h.dir, 0o700)
		_ = os.WriteFile(filepath.Join(h.dir, markerFile), []byte("127.0.0.1:7890\n"), 0o600)
	})
	if !h.s.Status().PendingRecovery {
		t.Fatal("PendingRecovery should be set")
	}
	ns := h.s.TakeNotices()
	if len(ns) != 1 || ns[0].Kind != "recovery" {
		t.Fatalf("want one recovery notice, got %+v", ns)
	}
	if err := h.s.ResolveRecovery(true); err != nil {
		t.Fatal(err)
	}
	if _, c := h.sys.counts(); c != 1 {
		t.Fatalf("Clear calls = %d, want 1", c)
	}
	if h.s.markerExists() || h.s.Status().PendingRecovery || h.s.Status().SystemProxy {
		t.Fatal("recovery not cleared")
	}
}

func TestRecoveryIgnoredWhenProxyIsNotOurs(t *testing.T) {
	// marker present but the proxy points elsewhere: stale marker, no prompt
	h := newHarness(t, func(h *harness) {
		h.sys.state.Enabled, h.sys.state.Host, h.sys.state.Port = true, "127.0.0.1", 1080
		_ = os.WriteFile(filepath.Join(h.dir, markerFile), []byte("127.0.0.1:7890\n"), 0o600)
	})
	if h.s.Status().PendingRecovery || h.s.markerExists() {
		t.Fatal("stale marker should be removed without a prompt")
	}
	// proxy at our port but no marker: not ours
	h2 := newHarness(t, func(h *harness) {
		h.sys.state.Enabled, h.sys.state.Host, h.sys.state.Port = true, "127.0.0.1", 7890
	})
	if h2.s.Status().PendingRecovery {
		t.Fatal("no marker means no recovery prompt")
	}
}

func TestSystemProxyOwnershipOnStop(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatalf("SetSystemProxy: %v", err)
	}
	st := h.s.Status()
	if st.Core != model.CoreRunning || !st.SystemProxy || !h.s.markerExists() {
		t.Fatalf("after SetSystemProxy: %+v marker=%v", st, h.s.markerExists())
	}
	if sets, _ := h.sys.counts(); sets != 1 {
		t.Fatalf("Set calls = %d", sets)
	}
	if err := h.s.StopCore(); err != nil {
		t.Fatal(err)
	}
	if _, clears := h.sys.counts(); clears != 1 {
		t.Fatalf("StopCore should clear the proxy we set, clears = %d", clears)
	}
	st = h.s.Status()
	if st.Core != model.CoreStopped || st.SystemProxy || h.s.markerExists() {
		t.Fatalf("after StopCore: %+v", st)
	}
	// not ours: StopCore and Shutdown leave the proxy alone
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	_ = h.sys.Set("127.0.0.1", 7890, nil) // someone else set it
	if err := h.s.StopCore(); err != nil {
		t.Fatal(err)
	}
	if _, clears := h.sys.counts(); clears != 1 {
		t.Fatalf("StopCore must not clear a proxy Lanflow did not set, clears = %d", clears)
	}
	_ = h.s.StartCore()
	h.s.Shutdown()
	if _, clears := h.sys.counts(); clears != 1 {
		t.Fatalf("Shutdown must not clear a proxy Lanflow did not set, clears = %d", clears)
	}
	if h.mgr.Running() {
		t.Fatal("Shutdown should stop the core")
	}
}

func TestShutdownClearsOwnProxy(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	h.s.Shutdown()
	h.s.Shutdown() // idempotent
	if _, clears := h.sys.counts(); clears != 1 {
		t.Fatalf("clears = %d, want 1", clears)
	}
	if h.s.markerExists() {
		t.Fatal("marker should be removed")
	}
}

func TestConnectionTrackerSpeedsAndClosed(t *testing.T) {
	var tr connTracker
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := func(id string, up, down int64) controller.Connection {
		return controller.Connection{ID: id, Upload: up, Download: down, Start: t0,
			Metadata: controller.Metadata{Host: "www.google.com", DestinationPort: "443", DestinationIP: "1.1.1.1", Network: "tcp"},
			Chains:   []string{"东京 03", "节点选择"}, Rule: "DomainSuffix", RulePayload: "google.com"}
	}
	active, closed := tr.update(controller.Connections{Connections: []controller.Connection{c("a", 100, 1000), c("b", 0, 0)}}, t0)
	if len(active) != 2 || len(closed) != 0 {
		t.Fatalf("first snapshot: %d active %d closed", len(active), len(closed))
	}
	if active[0].Host != "www.google.com:443" || active[0].Chains[0] != "节点选择" || active[0].Chains[1] != "东京 03" {
		t.Fatalf("mapping wrong: %+v", active[0])
	}
	if active[0].UpSpeed != 0 {
		t.Fatal("no speed on the first sample")
	}
	active, closed = tr.update(controller.Connections{Connections: []controller.Connection{c("a", 300, 5000)}}, t0.Add(2*time.Second))
	if len(active) != 1 || active[0].UpSpeed != 100 || active[0].DownSpeed != 2000 {
		t.Fatalf("speeds: %+v", active[0])
	}
	if len(closed) != 1 || closed[0].ID != "b" || closed[0].ClosedAt.IsZero() {
		t.Fatalf("closed diff: %+v", closed)
	}
}

func TestClosedRingAndStreams(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	var conns []controller.Connection
	for i := 0; i < 600; i++ {
		conns = append(conns, controller.Connection{ID: string(rune('a'+i%26)) + string(rune(i)), Chains: []string{"东京 03", "节点选择"}})
	}
	h.ctrl.connsC <- controller.Connections{Connections: conns, UploadTotal: 10, DownloadTotal: 20}
	if !waitFor(func() bool { return h.s.Status().Connections == 600 }) {
		t.Fatal("connections not applied")
	}
	h.ctrl.connsC <- controller.Connections{UploadTotal: 10, DownloadTotal: 20}
	if !waitFor(func() bool { return len(h.s.ClosedConnections()) == closedRing }) {
		t.Fatalf("closed ring = %d, want %d", len(h.s.ClosedConnections()), closedRing)
	}
	h.ctrl.trafficC <- controller.Traffic{Up: 5, Down: 7}
	if !waitFor(func() bool { u, d := h.s.TrafficHistory(); return len(u) == 1 && u[0] == 5 && d[0] == 7 }) {
		t.Fatal("traffic sample not recorded")
	}
	h.ctrl.logsC <- controller.Log{Type: "INFO", Payload: "hello"}
	if !waitFor(func() bool { l := h.s.Logs(); return len(l) == 1 && l[0].Level == "info" }) {
		t.Fatal("log not recorded")
	}
	st := h.s.Status()
	if st.TotalUp != 10 || st.TotalDown != 20 || st.Up != 5 {
		t.Fatalf("status totals: %+v", st)
	}
}

func TestBuildSnapshotOrderAndNormalization(t *testing.T) {
	snap := buildSnapshot(sampleProxies(), map[string]bool{"香港 01": true}, "")
	names := make([]string, len(snap.Groups))
	for i, g := range snap.Groups {
		names[i] = g.Name
	}
	want := []string{"自动选择", "节点选择", "流媒体", "GLOBAL"}
	if len(names) != len(want) {
		t.Fatalf("groups = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("groups = %v, want %v", names, want)
		}
	}
	if snap.Groups[0].Type != model.GroupURLTest || snap.Groups[1].Type != model.GroupSelect {
		t.Fatalf("types not normalized: %+v", snap.Groups)
	}
	if _, ok := snap.Proxies[probeGroup]; ok {
		t.Fatal("probe group must be hidden")
	}
	for _, m := range snap.Groups[3].All {
		if m == probeGroup {
			t.Fatal("probe group must not appear as a member")
		}
	}
	if snap.MainGroup != "节点选择" {
		t.Fatalf("main group = %q", snap.MainGroup)
	}
	if snap.Proxies["香港 01"].Delay != 100 || !snap.Proxies["香港 01"].Testing {
		t.Fatalf("delay/testing: %+v", snap.Proxies["香港 01"])
	}
	if snap.Proxies["美国 05"].Delay != -1 {
		t.Fatalf("history delay 0 should map to -1, got %d", snap.Proxies["美国 05"].Delay)
	}
	if snap.Proxies["DIRECT"].Delay != 0 {
		t.Fatal("untested stays 0")
	}
	if buildSnapshot(sampleProxies(), nil, "流媒体").MainGroup != "流媒体" {
		t.Fatal("preferred main group ignored")
	}
	if buildSnapshot(sampleProxies(), nil, "不存在").MainGroup != "节点选择" {
		t.Fatal("missing preferred main group should fall back")
	}
	if resolveNode(snap, "流媒体") != "东京 03" {
		t.Fatalf("resolveNode = %q", resolveNode(snap, "流媒体"))
	}
}

func TestBuildRoutesGrouping(t *testing.T) {
	snap := buildSnapshot(sampleProxies(), nil, "")
	t0 := time.Now()
	conns := []model.Connection{
		{ID: "1", Chains: []string{"流媒体", "节点选择", "东京 03"}, UpSpeed: 10, DownSpeed: 100, Start: t0},
		{ID: "2", Chains: []string{"流媒体", "节点选择", "东京 03"}, UpSpeed: 1, DownSpeed: 1, Start: t0.Add(time.Second)},
		{ID: "3", Chains: []string{"DIRECT"}, UpSpeed: 999, DownSpeed: 999, Start: t0},
		{ID: "4", Chains: []string{"自动选择", "香港 01"}, UpSpeed: 2, DownSpeed: 3, Start: t0},
	}
	jp := model.Place{Country: "JP", City: "东京"}
	exits := map[string]model.ExitInfo{"东京 03": {Node: "东京 03", Place: &jp, Source: model.LocateName}}
	routes := buildRoutes(conns, snap, model.ModeRule, exits)
	if len(routes) != 3 {
		t.Fatalf("routes = %+v", routes)
	}
	if !routes[0].Main || routes[0].Group != "节点选择" || routes[0].Connections != 0 || routes[0].Node != "东京 03" {
		t.Fatalf("main route first even without connections: %+v", routes[0])
	}
	if routes[1].Group != "流媒体" || routes[1].Connections != 2 || routes[1].Speed != 112 || routes[1].Exit.Place == nil {
		t.Fatalf("streaming route: %+v", routes[1])
	}
	if routes[2].Group != "自动选择" || routes[2].Node != "香港 01" || routes[2].Exit.Source != model.LocateUnknown {
		t.Fatalf("auto route: %+v", routes[2])
	}
	if g := buildRoutes(conns, snap, model.ModeGlobal, exits); len(g) != 1 || g[0].Group != "GLOBAL" || g[0].Connections != 3 {
		t.Fatalf("global mode: %+v", g)
	}
	if buildRoutes(conns, snap, model.ModeDirect, exits) != nil {
		t.Fatal("direct mode has no routes")
	}
}

func TestExitRoutesFromService(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.ctrl.connsC <- controller.Connections{Connections: []controller.Connection{
		{ID: "1", Chains: []string{"东京 03", "节点选择", "流媒体"}, Metadata: controller.Metadata{RemoteDestination: "101.89.12.40"}},
	}}
	if !waitFor(func() bool { r := h.s.ExitRoutes(); return len(r) == 2 && r[1].Exit.Entry != nil }) {
		t.Fatalf("routes = %+v", h.s.ExitRoutes())
	}
	r := h.s.ExitRoutes()
	if !r[0].Main || r[0].Group != "节点选择" || r[1].Group != "流媒体" || r[1].Exit.Place == nil || r[1].Exit.Entry.IP != "101.89.12.40" {
		t.Fatalf("routes = %+v", r)
	}
	if err := h.s.ProbeExits(); err != nil {
		t.Fatal(err)
	}
	r = h.s.ExitRoutes()
	if r[0].Exit.Source != model.LocateProbe || r[0].Exit.Colo != "HKG" {
		t.Fatalf("probe result not applied: %+v", r[0].Exit)
	}
	h.ctrl.mu.Lock()
	sel := h.ctrl.selected
	h.ctrl.mu.Unlock()
	if len(sel) == 0 || sel[0][0] != probeGroup {
		t.Fatalf("probe must select in %s, got %v", probeGroup, sel)
	}
	if h.s.You() == nil || h.s.You().IP != "1.2.3.4" {
		if !waitFor(func() bool { return h.s.You() != nil }) {
			t.Fatal("You() not fetched after start")
		}
	}
}

func TestOverrideValidatesBeforeSave(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.SaveOverride("global", "BAD: yaml"); err == nil {
		t.Fatal("compose failure must reject the override")
	}
	if h.store.Override("global") != "" {
		t.Fatal("rejected override must not be stored")
	}
	h.core.validateErr = errors.New("line 3: unknown field")
	if err := h.s.SaveOverride("global", "log-level: warning\n"); err == nil {
		t.Fatal("validate failure must reject the override")
	}
	if h.store.Override("global") != "" {
		t.Fatal("rejected override must not be stored")
	}
	h.core.validateErr = nil
	if err := h.s.SaveOverride("global", "log-level: warning\n"); err != nil {
		t.Fatal(err)
	}
	if h.store.Override("global") != "log-level: warning\n" || h.s.Overrides().Global != "log-level: warning\n" {
		t.Fatal("override not stored")
	}
	// the runtime file is untouched while stopped; preview still works
	diff, err := h.s.PreviewOverride("global", "x: 1\n")
	if err != nil || len(diff) != 2 {
		t.Fatalf("preview: %v %+v", err, diff)
	}
	if len(h.core.validated) != 2 {
		t.Fatalf("validate calls = %d, want 2 (one rejected, one accepted)", len(h.core.validated))
	}
}

func TestReloadKeepsOldRuntimeOnFailure(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(h.s.runtime)
	h.ctrl.reloadFn = func(string) error { return errors.New("boom") }
	set := h.s.Settings()
	set.DNS.TakeOver = true
	if err := h.s.SaveSettings(set); err == nil {
		t.Fatal("reload failure must be reported")
	}
	after, _ := os.ReadFile(h.s.runtime)
	if string(before) != string(after) {
		t.Fatal("runtime.yaml must be restored after a failed reload")
	}
	if h.s.Settings().DNS.TakeOver {
		t.Fatal("settings must revert after a failed reload")
	}
	h.ctrl.reloadFn = nil
	set.DNS.TakeOver = true
	set.Sniffer.TakeOver = true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.prof.mu.Lock()
	last := h.prof.inputs[len(h.prof.inputs)-1]
	h.prof.mu.Unlock()
	if !last.DNS.TakeOver || !last.Sniffer.TakeOver || !h.s.Settings().DNS.TakeOver {
		t.Fatal("new settings must reach the composer and persist after a successful reload")
	}
	h.ctrl.mu.Lock()
	reloads := h.ctrl.reloads
	h.ctrl.mu.Unlock()
	if reloads != 2 {
		t.Fatalf("reloads = %d, want 2", reloads)
	}
}

func TestHotPatchForModeAndPort(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetMode(model.ModeGlobal); err != nil {
		t.Fatal(err)
	}
	set := h.s.Settings()
	set.MixedPort = 7777
	set.AllowLAN = true
	set.Sniffer.Enable = false
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.ctrl.mu.Lock()
	patches := h.ctrl.patches
	reloads := h.ctrl.reloads
	h.ctrl.mu.Unlock()
	if len(patches) != 2 || *patches[0].Mode != "global" || *patches[1].MixedPort != 7777 || !*patches[1].AllowLan || *patches[1].Sniffing {
		t.Fatalf("patches = %+v", patches)
	}
	if reloads != 0 {
		t.Fatalf("hot fields must not reload, reloads = %d", reloads)
	}
	st := h.s.Status()
	if st.Mode != model.ModeGlobal || st.MixedPort != 7777 || !st.AllowLAN {
		t.Fatalf("status = %+v", st)
	}
}

func TestCrashRestartsOnceAndNotifies(t *testing.T) {
	h := newHarness(t, nil)
	h.s.goBackground(h.s.watchCrashes)
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	h.mgr.crash(errors.New("exit status 2"))
	if !waitFor(func() bool { h.mgr.mu.Lock(); defer h.mgr.mu.Unlock(); return h.mgr.starts == 2 }) {
		t.Fatal("expected one automatic restart")
	}
	if !waitFor(func() bool { st := h.s.Status(); return st.Core == model.CoreRunning && st.SystemProxy }) {
		t.Fatalf("after restart: %+v", h.s.Status())
	}
	if _, clears := h.sys.counts(); clears != 1 {
		t.Fatalf("the proxy must be cleared while the core is down, clears = %d", clears)
	}
	if !waitFor(func() bool {
		for _, n := range h.s.TakeNotices() {
			if n.Kind == "toast" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("expected a toast about the automatic restart")
	}
	// a second crash right away is not restarted
	h.mgr.crash(errors.New("exit status 2"))
	if !waitFor(func() bool { return h.s.Status().Core == model.CoreError }) {
		t.Fatalf("status = %+v", h.s.Status())
	}
	if !waitFor(func() bool {
		for _, n := range h.s.TakeNotices() {
			if n.Kind == "core-crash" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("expected a core-crash notice")
	}
	h.mgr.mu.Lock()
	starts := h.mgr.starts
	h.mgr.mu.Unlock()
	if starts != 2 {
		t.Fatalf("starts = %d, want 2", starts)
	}
	if h.s.Status().SystemProxy {
		t.Fatal("system proxy must be off after the core gave up")
	}
}

func TestEmitterCoalescing(t *testing.T) {
	e := newEmitter()
	var n int32
	var mu sync.Mutex
	var topics []Topic
	off := e.add(func(t Topic) {
		atomic.AddInt32(&n, 1)
		mu.Lock()
		topics = append(topics, t)
		mu.Unlock()
	})
	for i := 0; i < 20; i++ {
		e.emit(TopicConns)
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("burst should deliver once immediately, got %d", got)
	}
	if !waitFor(func() bool { return atomic.LoadInt32(&n) == 2 }) {
		t.Fatalf("trailing delivery missing, n = %d", atomic.LoadInt32(&n))
	}
	time.Sleep(3 * coalesceWindow)
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("no further deliveries expected, got %d", got)
	}
	e.emit(TopicLogs)
	if !waitFor(func() bool { mu.Lock(); defer mu.Unlock(); return len(topics) == 3 && topics[2] == TopicLogs }) {
		t.Fatal("other topics are independent")
	}
	off()
	e.emit(TopicLogs)
	time.Sleep(2 * coalesceWindow)
	if atomic.LoadInt32(&n) != 3 {
		t.Fatal("removed listener must not be called")
	}
	e.close()
}

func TestNoticesAndTakeNotices(t *testing.T) {
	h := newHarness(t, nil)
	var got int32
	off := h.s.OnChange(func(tp Topic) {
		if tp == TopicNotice {
			atomic.AddInt32(&got, 1)
		}
	})
	defer off()
	h.s.notify(Notice{Kind: "toast", Title: "x"})
	if atomic.LoadInt32(&got) != 1 {
		t.Fatal("notice topic not emitted")
	}
	if ns := h.s.TakeNotices(); len(ns) != 1 || ns[0].Title != "x" {
		t.Fatalf("notices = %+v", ns)
	}
	if ns := h.s.TakeNotices(); len(ns) != 0 {
		t.Fatal("TakeNotices must clear")
	}
}

func TestFirstImportStartsCore(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.store = newFakeStore() // no subscriptions at all
		h.prof.fetched.Data = []byte("proxies: [a]\n")
		h.prof.fetched.Name = "机场 A"
		h.prof.fetched.Total = 100
	})
	if err := h.s.StartCore(); !errors.Is(err, errNoSubscription) {
		t.Fatalf("start without subscription: %v", err)
	}
	if h.s.Status().Core != model.CoreStopped {
		t.Fatalf("status = %+v", h.s.Status())
	}
	sub, err := h.s.ImportSubscriptionURL("https://example.com/s", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Name != "机场 A" || !sub.Current || sub.Total != 100 {
		t.Fatalf("sub = %+v", sub)
	}
	st := h.s.Status()
	if st.Core != model.CoreRunning || st.SystemProxy || !st.HasSubscriptions || st.CurrentSubscription != sub.ID {
		t.Fatalf("after first import: %+v", st)
	}
	if err := h.s.DeleteSubscription(sub.ID); err == nil {
		t.Fatal("deleting the current subscription must be refused")
	}
}

func TestUseSubscriptionRejectsInvalid(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	cur, _ := h.store.Current()
	bad, _ := h.store.Put(model.Subscription{Name: "坏订阅", Source: "url"})
	_ = h.store.SetContent(bad.ID, []byte("proxies: []\n"))
	h.core.validateErr = errors.New("proxy 'xx' not found")
	err := h.s.UseSubscription(bad.ID)
	if err == nil {
		t.Fatal("invalid config must be rejected")
	}
	if now, _ := h.store.Current(); now.ID != cur.ID {
		t.Fatal("current subscription must not change")
	}
	ns := h.s.TakeNotices()
	if len(ns) != 1 || ns[0].Kind != "invalid-config" || ns[0].Subject != bad.ID {
		t.Fatalf("notices = %+v", ns)
	}
	h.core.validateErr = nil
	if err := h.s.UseSubscription(bad.ID); err != nil {
		t.Fatal(err)
	}
	if now, _ := h.store.Current(); now.ID != bad.ID {
		t.Fatal("switch failed")
	}
	h.ctrl.mu.Lock()
	reloads := h.ctrl.reloads
	h.ctrl.mu.Unlock()
	if reloads != 1 {
		t.Fatalf("reloads = %d, want 1", reloads)
	}
}

func TestTUNRequiresHelperAndSwitchesRunner(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.SetTUN(true); !errors.Is(err, ErrHelperRequired) {
		t.Fatalf("want ErrHelperRequired, got %v", err)
	}
	if err := h.s.InstallTUNHelper(); err != nil {
		t.Fatal(err)
	}
	if !h.s.TUNHelperInstalled() || !h.s.Status().TUNAvailable {
		t.Fatal("helper should be installed")
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetTUN(true); err != nil {
		t.Fatal(err)
	}
	h.mgr.mu.Lock()
	r := h.mgr.runner.(namedRunner).name
	h.mgr.mu.Unlock()
	if r != "helper" || !h.s.Status().TUN || !h.s.Settings().TUN {
		t.Fatalf("runner = %s, status = %+v", r, h.s.Status())
	}
	// The helper only lets root write inside the data dir.
	h.mgr.mu.Lock()
	logFile := h.mgr.specs[len(h.mgr.specs)-1].LogFile
	h.mgr.mu.Unlock()
	if want := filepath.Join(h.s.opt.DataDir, "core", "mihomo-tun.log"); logFile != want {
		t.Fatalf("TUN core log = %s, want %s", logFile, want)
	}
	h.prof.mu.Lock()
	last := h.prof.inputs[len(h.prof.inputs)-1]
	h.prof.mu.Unlock()
	if !last.Managed.TUN || last.Managed.ProbePort != 40000 {
		t.Fatalf("composed managed = %+v", last.Managed)
	}
	if err := h.s.SetTUN(false); err != nil {
		t.Fatal(err)
	}
	h.mgr.mu.Lock()
	r = h.mgr.runner.(namedRunner).name
	h.mgr.mu.Unlock()
	if r != "exec" || h.s.Status().TUN {
		t.Fatalf("runner = %s after TUN off", r)
	}
}

func TestRulesMappingAndCustomRules(t *testing.T) {
	h := newHarness(t, nil)
	h.ctrl.rules = []controller.Rule{
		{Type: "DomainSuffix", Payload: "openai.com", Proxy: "节点选择", Size: -1},
		{Index: 1, Type: "GeoSite", Payload: "cn", Proxy: "DIRECT", Size: 1000},
		{Index: 2, Type: "Match", Proxy: "节点选择", Size: -1},
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	r, err := h.s.SaveCustomRule(model.CustomRule{Type: "domain-suffix", Payload: "openai.com", Target: "节点选择"})
	if err != nil || r.ID == "" || r.Type != "DOMAIN-SUFFIX" {
		t.Fatalf("SaveCustomRule: %v %+v", err, r)
	}
	rules := h.s.Rules()
	if len(rules) != 3 || !rules[0].Custom || rules[1].Custom || rules[1].Size != 1000 || rules[2].Size != -1 {
		t.Fatalf("rules = %+v", rules)
	}
	if _, err := h.s.SaveCustomRule(model.CustomRule{Type: "IP-CIDR", Payload: "bad", Target: "DIRECT"}); err == nil {
		t.Fatal("bad CIDR must be rejected")
	}
	r2, err := h.s.SaveCustomRule(model.CustomRule{Type: "IP-CIDR", Payload: "10.0.0.1", Target: "DIRECT"})
	if err != nil || r2.Payload != "10.0.0.1/32" {
		t.Fatalf("CIDR normalization: %v %+v", err, r2)
	}
	cr := h.s.CustomRules()
	if len(cr) != 2 || cr[0].ID != r2.ID {
		t.Fatalf("new rules go first: %+v", cr)
	}
	if err := h.s.MoveCustomRule(r2.ID, 1); err != nil {
		t.Fatal(err)
	}
	if cr = h.s.CustomRules(); cr[1].ID != r2.ID {
		t.Fatalf("move failed: %+v", cr)
	}
	if err := h.s.DeleteCustomRule(r2.ID); err != nil {
		t.Fatal(err)
	}
	if cr = h.s.CustomRules(); len(cr) != 1 {
		t.Fatalf("delete failed: %+v", cr)
	}
	m := matchFrom("DomainSuffix", "openai.com", []string{"节点选择", "东京 03"}, "api.openai.com", rules, 1)
	if m.RuleIndex != 0 || !m.Custom || m.Policy != "节点选择" || m.Rule != "DomainSuffix,openai.com" {
		t.Fatalf("match = %+v", m)
	}
	host, u, err := testTarget("api.openai.com")
	if err != nil || host != "api.openai.com" || u != "https://api.openai.com:443/" {
		t.Fatalf("testTarget: %v %s %s", err, host, u)
	}
}

func TestDNSQueryFakeIP(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.s.DNSQuery("example.com", "A"); err == nil {
		t.Fatal("needs a running core")
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	r, err := h.s.DNSQuery("example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if !r.FakeIP || r.Status != "NOERROR" || r.Type != "A" || len(r.Answers) != 1 {
		t.Fatalf("result = %+v", r)
	}
}

func TestMissingBinary(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.core.findErr = errors.New("not found") })
	st := h.s.Status()
	if st.Core != model.CoreMissing || st.CoreError == "" {
		t.Fatalf("status = %+v", st)
	}
	if err := h.s.StartCore(); err == nil {
		t.Fatal("start must fail without a binary")
	}
	if err := h.s.SetSystemProxy(true); err == nil || h.s.Status().SystemProxy {
		t.Fatal("system proxy must not be set when the core cannot start")
	}
}

func TestRuntimeConfigAndNetwork(t *testing.T) {
	h := newHarness(t, nil)
	lines, err := h.s.RuntimeConfig()
	if err != nil || len(lines) == 0 {
		t.Fatalf("RuntimeConfig while stopped: %v %d", err, len(lines))
	}
	if err := h.s.RefreshNetwork(); err != nil {
		t.Fatal(err)
	}
	if h.s.NetInfo().LANIP != "192.168.1.23" || h.s.Status().LANAddress != "192.168.1.23" {
		t.Fatalf("netinfo = %+v", h.s.NetInfo())
	}
	if err := h.s.UpdateGeoData(); err == nil {
		t.Fatal("geo update needs a running core")
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.UpdateGeoData(); err != nil || h.s.Settings().Geo.UpdatedAt.IsZero() {
		t.Fatalf("UpdateGeoData: %v", err)
	}
}

// Another app's proxy that was on before Lanflow took over comes back
// exactly when Lanflow lets go, instead of being switched off.
func TestSystemProxyRestoresPreviousOwner(t *testing.T) {
	h := newHarness(t, nil)
	h.sys.state = sysproxy.State{Enabled: true, Host: "127.0.0.1", Port: 7897} // e.g. another client
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.sys.Get(); st.Port != h.s.Settings().MixedPort {
		t.Fatalf("proxy not pointed at us: %+v", st)
	}
	if err := h.s.SetSystemProxy(false); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.sys.Get(); !st.Enabled || st.Port != 7897 {
		t.Fatalf("previous proxy not restored: %+v", st)
	}
	if h.s.markerExists() {
		t.Fatal("marker left behind")
	}
}

// If someone else re-pointed the system proxy while Lanflow owned it,
// turning ours off must leave their setting untouched.
func TestSystemProxyLeavesForeignChangeAlone(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	h.sys.Set("127.0.0.1", 7897, nil) // another app takes over
	_, before := h.sys.counts()
	if err := h.s.StopCore(); err != nil {
		t.Fatal(err)
	}
	if _, after := h.sys.counts(); after != before {
		t.Fatal("touched a proxy that is no longer ours")
	}
	if st, _ := h.sys.Get(); !st.Enabled || st.Port != 7897 {
		t.Fatalf("foreign proxy changed: %+v", st)
	}
}

// Mihomo keeps running when its mixed port is taken (it only logs), so the
// service must refuse to start instead of sharing another client's port.
func TestStartRefusesTakenPort(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.taken[7890] = true; h.taken[7891] = true })
	// first run next to the other client: a free port was chosen
	if p := h.s.Settings().MixedPort; p != 7892 {
		t.Fatalf("first-run port = %d, want 7892", p)
	}
	set := h.s.Settings()
	set.MixedPort = 7890
	if err := h.s.SaveSettings(set); err == nil || !strings.Contains(err.Error(), "已被其他程序占用") {
		t.Fatalf("SaveSettings to a taken port: %v", err)
	}
	if p := h.s.Settings().MixedPort; p != 7892 {
		t.Fatalf("port not kept after refusal: %d", p)
	}
	h.taken[7892] = true // something grabs our port while stopped
	if err := h.s.StartCore(); err == nil || !strings.Contains(err.Error(), "7892") {
		t.Fatalf("StartCore with a taken port: %v", err)
	}
	if st := h.s.Status(); st.Core != model.CoreError || !strings.Contains(st.CoreError, "已被其他程序占用") {
		t.Fatalf("status after refusal: %+v", st)
	}
}

// Two TUNs break the network: refuse while another client's TUN is active.
func TestSetTUNRefusesWithOtherTUN(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.helper.installed = true; h.tunOf = "utun1500" })
	err := h.s.SetTUN(true)
	if err == nil || !strings.Contains(err.Error(), "utun1500") {
		t.Fatalf("SetTUN with another TUN active: %v", err)
	}
	if h.s.Settings().TUN {
		t.Fatal("TUN must stay off")
	}
	h.tunOf = ""
	if err := h.s.SetTUN(true); err != nil {
		t.Fatalf("SetTUN without conflict: %v", err)
	}
}
