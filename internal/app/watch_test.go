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
		onWake:      func() { wakes++ },
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
