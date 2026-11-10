package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/platform/sysproxy"
)

// The watcher fires a wake when the clock jumps and a network change once
// a new interface fingerprint has stayed for two ticks.
func TestSysWatcherWakeAndNetworkChange(t *testing.T) {
	now := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	fp := "en0=192.168.1.23/24"
	wakes, nets := 0, 0
	w := &sysWatcher{
		now:         func() time.Time { return now },
		fingerprint: func() string { return fp },
		interval:    watchInterval,
		onWake:      func(time.Duration) { wakes++ },
		onNetChange: func() { nets++ },
	}
	for i := 0; i < 5; i++ {
		w.tick()
		now = now.Add(watchInterval)
	}
	if wakes != 0 || nets != 0 {
		t.Fatalf("steady ticks: wakes=%d nets=%d", wakes, nets)
	}
	// A late tick means the machine slept.
	now = now.Add(2 * time.Hour)
	w.tick()
	if wakes != 1 {
		t.Fatalf("wake not detected: %d", wakes)
	}
	// One odd fingerprint (DHCP renewing) does not count...
	now = now.Add(watchInterval)
	fp = "en0=192.168.1.99/24"
	w.tick()
	if nets != 0 {
		t.Fatal("network change fired on the first tick")
	}
	fp = "en0=192.168.1.23/24"
	now = now.Add(watchInterval)
	w.tick()
	if nets != 0 {
		t.Fatal("network change fired for a fingerprint that went back")
	}
	// ...a stable new one does, once.
	fp = "en1=10.0.0.5/24"
	for i := 0; i < 3; i++ {
		now = now.Add(watchInterval)
		w.tick()
	}
	if nets != 1 || wakes != 1 {
		t.Fatalf("after a new network: nets=%d wakes=%d", nets, wakes)
	}
}

// After a wake the service drops a system proxy another app took over,
// tells the user, and refreshes the network information and public IP.
func TestRecheckDetectsForeignProxyAndRefreshes(t *testing.T) {
	h := newHarness(t, nil)
	youBackoff = []time.Duration{time.Millisecond}
	t.Cleanup(func() {
		youBackoff = []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 2 * time.Minute, 5 * time.Minute}
	})
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	h.s.TakeNotices()
	// Another app pointed the system proxy elsewhere while we slept.
	h.sys.mu.Lock()
	h.sys.state = sysproxy.State{Enabled: true, Host: "127.0.0.1", Port: 7897}
	h.sys.mu.Unlock()
	netCalls := 0
	var nmu sync.Mutex
	h.s.d.netinfo = func(context.Context) (model.NetInfo, error) {
		nmu.Lock()
		netCalls++
		nmu.Unlock()
		return model.NetInfo{Interface: "Wi-Fi (en1)", LANIP: "10.0.0.5"}, nil
	}
	h.s.recheck()
	st := h.s.Status()
	if st.SystemProxy || st.Core != model.CoreRunning {
		t.Fatalf("status after recheck = %+v", st)
	}
	if h.s.markerExists() {
		t.Fatal("marker should be gone once the proxy is not ours")
	}
	var found bool
	for _, n := range h.s.TakeNotices() {
		if strings.Contains(n.Title, "系统代理已被其他程序修改") {
			found = true
		}
	}
	if !found {
		t.Fatal("no notice about the foreign proxy change")
	}
	if h.s.NetInfo().LANIP != "10.0.0.5" || h.s.Status().LANAddress != "10.0.0.5" {
		t.Fatalf("netinfo not refreshed: %+v", h.s.NetInfo())
	}
	if !waitFor(func() bool { return h.s.NetInfo().PublicIPStatus == "ok" && h.s.You() != nil }) {
		t.Fatalf("public IP not refreshed: %+v", h.s.NetInfo())
	}
	nmu.Lock()
	defer nmu.Unlock()
	if netCalls != 1 {
		t.Fatalf("netinfo called %d times", netCalls)
	}
}

// A core that died while the machine slept is handled like a crash: one
// automatic restart and a notice.
func TestRecheckRestartsDeadCore(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.s.TakeNotices()
	h.mgr.mu.Lock()
	h.mgr.running = false
	h.mgr.mu.Unlock()
	h.s.recheck()
	if st := h.s.Status(); st.Core != model.CoreRunning {
		t.Fatalf("core not restarted: %+v", st)
	}
	if h.mgr.starts != 2 {
		t.Fatalf("starts = %d, want 2", h.mgr.starts)
	}
	var restarted bool
	for _, n := range h.s.TakeNotices() {
		if n.Title == "内核意外退出，已自动重新启动" && n.System {
			restarted = true
		}
	}
	if !restarted {
		t.Fatal("no system notice about the restart")
	}
}

// The public IP lookup retries with backoff instead of giving up, and
// the 网络 page can see it failing meanwhile.
func TestFetchYouRetries(t *testing.T) {
	var mu sync.Mutex
	fails := 2
	h := newHarness(t, func(h *harness) {
		h.loc.directErr = func() error {
			mu.Lock()
			defer mu.Unlock()
			if fails > 0 {
				fails--
				return errors.New("no route to host")
			}
			return nil
		}
	})
	youBackoff = []time.Duration{time.Millisecond}
	t.Cleanup(func() {
		youBackoff = []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 2 * time.Minute, 5 * time.Minute}
	})
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool { return h.s.You() != nil }) {
		t.Fatalf("public IP never arrived: %+v", h.s.NetInfo())
	}
	if n := h.s.NetInfo(); n.PublicIPStatus != "ok" || n.PublicIPError != "" {
		t.Fatalf("netinfo = %+v", n)
	}
	mu.Lock()
	if fails != 0 {
		mu.Unlock()
		t.Fatal("lookup did not retry")
	}
	mu.Unlock()
}

// The DNS tool reports the core's real answer and what applications get.
func TestDNSQueryAppAnswers(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.ctrl.dnsAnswers = []string{"142.250.196.68"}
	var gotServer string
	h.s.d.appDNS = func(_ context.Context, server, name, qtype string) ([]string, error) {
		gotServer = server
		return []string{"198.18.0.7"}, nil
	}
	// Without a DNS listener or TUN, applications do not resolve through the core.
	r, err := h.s.DNSQuery("example.com", "A")
	if err != nil {
		t.Fatal(err)
	}
	if r.FakeIP || len(r.AppAnswers) != 0 || r.Answers[0] != "142.250.196.68" {
		t.Fatalf("result = %+v", r)
	}
	// A listener in the running config: query it.
	h.s.mu.Lock()
	h.s.state.runtimeYAML = []byte("dns:\n  enable: true\n  listen: 0.0.0.0:1053\n  enhanced-mode: fake-ip\n  fake-ip-range: 28.0.0.1/8\n")
	h.s.state.runtimeDNS = parseRuntimeDNS(h.s.state.runtimeYAML)
	h.s.mu.Unlock()
	r, err = h.s.DNSQuery("example.com", "A")
	if err != nil {
		t.Fatal(err)
	}
	if gotServer != "127.0.0.1:1053" || r.AppSource != "listener" || r.AppAnswers[0] != "198.18.0.7" || r.FakeIP {
		t.Fatalf("listener result = %+v (server %q)", r, gotServer)
	}
	h.s.d.appDNS = func(context.Context, string, string, string) ([]string, error) { return []string{"28.1.2.3"}, nil }
	r, _ = h.s.DNSQuery("example.com", "A")
	if !r.FakeIP || r.Answers[0] != "142.250.196.68" {
		t.Fatalf("fake range from config not honoured: %+v", r)
	}
	// TUN on, no listener: the hijacked system resolver.
	h.s.mu.Lock()
	h.s.state.runtimeYAML = []byte("dns:\n  enhanced-mode: fake-ip\n")
	h.s.state.runtimeDNS = parseRuntimeDNS(h.s.state.runtimeYAML)
	h.s.state.st.TUN = true
	h.s.mu.Unlock()
	h.s.d.appDNS = func(_ context.Context, server, _, _ string) ([]string, error) {
		gotServer = server
		return []string{"198.18.0.9"}, nil
	}
	r, _ = h.s.DNSQuery("example.com", "A")
	if gotServer != "" || r.AppSource != "system" || !r.FakeIP {
		t.Fatalf("system result = %+v (server %q)", r, gotServer)
	}
}

func TestListenAddr(t *testing.T) {
	for in, want := range map[string]string{"0.0.0.0:1053": "127.0.0.1:1053", ":53": "127.0.0.1:53", "[::]:1053": "127.0.0.1:1053", "127.0.0.1:5353": "127.0.0.1:5353", "bad": "", "": ""} {
		if got := listenAddr(in); got != want {
			t.Errorf("listenAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiffNet(t *testing.T) {
	a := netSnap{ifaces: map[string]string{"en0": "192.168.1.5/24", "en5": "10.8.0.2/24"}, iface: "Wi-Fi (en0)", gateway: "192.168.1.1", dns: "192.168.1.1"}
	b := netSnap{ifaces: map[string]string{"en0": "10.0.0.7/24", "en7": "172.16.0.2/16"}, iface: "Wi-Fi (en0)", gateway: "10.0.0.1", dns: "10.0.0.1"}
	var got []string
	for _, c := range diffNet(a, b) {
		got = append(got, c.kind+": "+c.text)
	}
	want := []string{
		"interface: en0 192.168.1.5/24 → 10.0.0.7/24",
		"interface: en5 down",
		"interface: en7 up (172.16.0.2/16)",
		`route: gateway "192.168.1.1" → "10.0.0.1"`,
		`dns: DNS "192.168.1.1" → "10.0.0.1"`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("diff:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if d := diffNet(a, a); len(d) != 0 {
		t.Fatalf("no change expected: %+v", d)
	}
}

func TestRecheckDelay(t *testing.T) {
	first := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	if d := recheckDelay(first, first); d != recheckDebounce {
		t.Fatalf("first event: %v", d)
	}
	if d := recheckDelay(first, first.Add(9*time.Second)); d != time.Second {
		t.Fatalf("near the cap: %v", d)
	}
	if d := recheckDelay(first, first.Add(time.Minute)); d != 0 {
		t.Fatalf("past the cap: %v", d)
	}
}

// netHarness is a harness whose network the test changes.
type netHarness struct {
	*harness
	mu     sync.Mutex
	info   model.NetInfo
	ifaces map[string]string
	reads  int
}

func newNetHarness(t *testing.T) *netHarness {
	n := &netHarness{
		harness: newHarness(t, nil),
		info:    model.NetInfo{Interface: "Wi-Fi (en0)", LANIP: "192.168.1.5", Gateway: "192.168.1.1", DNS: []string{"192.168.1.1"}},
		ifaces:  map[string]string{"en0": "192.168.1.5/24"},
	}
	n.s.d.netinfo = func(context.Context) (model.NetInfo, error) {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.reads++
		return n.info, nil
	}
	n.s.nw.ifaceSrc = func() map[string]string {
		n.mu.Lock()
		defer n.mu.Unlock()
		out := map[string]string{}
		for k, v := range n.ifaces {
			out[k] = v
		}
		return out
	}
	n.s.takeNetBaseline()
	n.s.ClearAppLogs()
	return n
}

func (n *netHarness) set(fn func(n *netHarness)) { n.mu.Lock(); fn(n); n.mu.Unlock() }

// recheckLogs returns the network recheck lines as "level: payload".
func (n *netHarness) recheckLogs() []string {
	var out []string
	for _, l := range n.s.AppLogs() {
		if strings.HasPrefix(l.Payload, "network recheck") {
			out = append(out, l.Level+": "+l.Payload)
		}
	}
	return out
}

// The OS reports Lanflow's own system DNS change as a network change; with
// nothing else changed it is skipped at debug level.
func TestRecheckSkipsLanflowsOwnDNSChange(t *testing.T) {
	n := newNetHarness(t)
	if err := n.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	starts := n.mgr.starts
	n.s.takeNetBaseline()
	n.s.ClearAppLogs()
	if err := n.s.d.dns.Set([]string{"223.5.5.5"}); err != nil {
		t.Fatal(err)
	}
	n.set(func(n *netHarness) { n.info.DNS = []string{"223.5.5.5"} })
	n.s.NetworkChanged()
	n.s.runPendingRecheck()
	logs := n.recheckLogs()
	if len(logs) != 1 || !strings.HasPrefix(logs[0], "debug: network recheck skipped after network (OS network event)") || !strings.Contains(logs[0], "Lanflow's own changes (") || !strings.Contains(logs[0], "system DNS") {
		t.Fatalf("logs: %q", logs)
	}
	if n.mgr.starts != starts {
		t.Fatal("the core was restarted for Lanflow's own DNS change")
	}
}

// A real change is logged at info level with what changed; events that
// come together make one recheck naming them all.
func TestRecheckLogsWhatChanged(t *testing.T) {
	n := newNetHarness(t)
	n.set(func(n *netHarness) {
		n.info.DNS = []string{"10.0.0.1"}
		n.info.Gateway = "10.0.0.1"
		n.ifaces = map[string]string{"en0": "10.0.0.7/24"}
	})
	n.s.NetworkChanged()
	n.s.systemChanged(srcInterfaces, "")
	n.s.NetworkChanged() // the same event again is named once
	n.s.runPendingRecheck()
	n.s.runPendingRecheck() // nothing pending: no second recheck
	logs := n.recheckLogs()
	if len(logs) != 1 {
		t.Fatalf("one recheck expected: %q", logs)
	}
	l := logs[0]
	for _, want := range []string{
		"info: network recheck after network (OS network event), interfaces;",
		"en0 192.168.1.5/24 → 10.0.0.7/24",
		`gateway "192.168.1.1" → "10.0.0.1"`,
		`DNS "192.168.1.1" → "10.0.0.1"`,
		"no repair needed",
	} {
		if !strings.Contains(l, want) {
			t.Fatalf("log %q lacks %q", l, want)
		}
	}
	if !strings.HasSuffix(l, "ms)") && !strings.HasSuffix(l, "s)") {
		t.Fatalf("no duration: %q", l)
	}
	if n.s.NetInfo().Gateway != "10.0.0.1" {
		t.Fatalf("network information not stored: %+v", n.s.NetInfo())
	}
}

// A wake after which nothing changed and the core answers: no restart, and
// the line is at debug level.
func TestRecheckAfterQuietWakeIsDebug(t *testing.T) {
	n := newNetHarness(t)
	if err := n.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	n.s.takeNetBaseline()
	n.s.ClearAppLogs()
	starts := n.mgr.starts
	n.s.Resumed()
	n.s.runPendingRecheck()
	logs := n.recheckLogs()
	if len(logs) != 1 || !strings.HasPrefix(logs[0], "debug: network recheck after wake (OS resume event); nothing changed; no repair needed") {
		t.Fatalf("logs: %q", logs)
	}
	if n.mgr.starts != starts {
		t.Fatal("the core was restarted though it answered")
	}
	// A core that died while asleep is restarted, and the line says so.
	n.mgr.mu.Lock()
	n.mgr.running = false
	n.mgr.mu.Unlock()
	n.s.ClearAppLogs()
	n.s.Resumed()
	n.s.runPendingRecheck()
	logs = n.recheckLogs()
	if len(logs) != 1 || !strings.HasPrefix(logs[0], "info: ") || !strings.Contains(logs[0], "repaired: core had exited, restarted") {
		t.Fatalf("logs after a dead core: %q", logs)
	}
}

// Lanflow's own proxy change (no network difference) is skipped too, while
// a gateway change is attributed to a core start (TUN routes) only then.
func TestRecheckAttributesRouteChangeToCoreStart(t *testing.T) {
	n := newNetHarness(t)
	if err := n.s.StartCore(); err != nil { // notes a core start
		t.Fatal(err)
	}
	n.set(func(n *netHarness) { n.info.Interface = "utun1500" })
	n.s.NetworkChanged()
	n.s.runPendingRecheck()
	if logs := n.recheckLogs(); len(logs) != 1 || !strings.HasPrefix(logs[0], "debug: network recheck skipped") || !strings.Contains(logs[0], "core start/stop") {
		t.Fatalf("logs: %q", logs)
	}
	if err := n.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	n.s.ClearAppLogs()
	n.s.NetworkChanged()
	n.s.runPendingRecheck()
	if logs := n.recheckLogs(); len(logs) != 1 || !strings.Contains(logs[0], "system proxy") || !strings.HasPrefix(logs[0], "debug: network recheck skipped") {
		t.Fatalf("logs after the proxy change: %q", logs)
	}
}
