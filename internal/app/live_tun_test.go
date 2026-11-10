package app

import (
	"archive/zip"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/netx"
	"lanflow/internal/platform/atrust"
	"lanflow/internal/platform/netinfo"
	"lanflow/internal/platform/sysdns"
)

// TestLiveTUN turns TUN mode on for real through the installed privileged
// helper, checks that the TUN interface takes the default route and that a
// request made WITHOUT any proxy setting is captured by the core, then
// turns TUN off and checks the routes are gone. Run it in a VM only:
//
//	LANFLOW_LIVE_TUN=1 LANFLOW_TUN_DATA=<dir> LANFLOW_TUN_RES=<.app/Contents/Resources> ./app.test -test.run LiveTUN -test.v
//
// The helper must already be installed for LANFLOW_TUN_DATA/data (see
// scripts/vm-tun-test.sh), since the test cannot answer the admin prompt.
func TestLiveTUN(t *testing.T) {
	if os.Getenv("LANFLOW_LIVE_TUN") != "1" {
		t.Skip("set LANFLOW_LIVE_TUN=1 (in a VM) to test TUN for real")
	}
	root, res := os.Getenv("LANFLOW_TUN_DATA"), os.Getenv("LANFLOW_TUN_RES")
	if root == "" || res == "" {
		t.Fatal("LANFLOW_TUN_DATA and LANFLOW_TUN_RES are required")
	}
	if name, ok := netinfo.OtherTUN(context.Background()); ok {
		t.Fatalf("another TUN (%s) is already active; run this on a clean machine", name)
	}
	s, err := NewService(Options{DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "log"), CacheDir: filepath.Join(root, "cache"), ResourceDir: res})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	t.Cleanup(func() {
		if t.Failed() {
			for _, f := range []string{filepath.Join(root, "log", "mihomo.log"), filepath.Join(root, "data", "core", "mihomo-tun.log")} {
				if b, err := os.ReadFile(f); err == nil {
					t.Logf("%s (tail):\n%s", filepath.Base(f), tail(string(b), 40))
				}
			}
		}
	})

	set := s.Settings()
	set.CorePath, set.MixedPort, set.ProbeExits = filepath.Join(res, "mihomo"), freePort(t), false
	if err := s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub.yaml")
	os.WriteFile(sub, []byte("proxies:\n  - name: 直连节点\n    type: direct\nproxy-groups:\n  - name: 节点选择\n    type: select\n    proxies: [直连节点]\nrules:\n  - MATCH,节点选择\n"), 0o600)
	if _, err := s.ImportSubscriptionFile(sub, true); err != nil {
		t.Fatal(err)
	}
	e2eWait(t, 20*time.Second, "core running", func() bool { return s.Status().Core == model.CoreRunning })

	t0 := time.Now()
	if err := s.SetTUN(true); err != nil {
		t.Fatalf("SetTUN(true): %v", err)
	}
	e2eWait(t, 30*time.Second, "TUN running", func() bool { st := s.Status(); return st.Core == model.CoreRunning && st.TUN })
	t.Logf("TUN on after %v", time.Since(t0).Round(time.Millisecond))

	var tunIf string
	e2eWait(t, 15*time.Second, "TUN routes", func() bool {
		name, ok := netinfo.OtherTUN(context.Background())
		tunIf = name
		return ok
	})
	t.Logf("TUN interface: %s", tunIf)
	if snap, err := sysdns.Snapshot(); err == nil {
		t.Logf("system DNS while TUN: %s", snap)
		if !strings.Contains(string(snap), sysdns.TUNServers[0]) {
			t.Error("system DNS was not pointed at the TUN resolver")
		}
	}
	if out, _ := exec.Command("pgrep", "-lf", "com.lanflow.mihomo").Output(); len(out) == 0 {
		t.Error("the core is not the helper-run root copy")
	} else {
		t.Logf("core process: %s", strings.TrimSpace(string(out)))
	}

	// A request with NO proxy configured must go through the TUN and show up
	// as a connection of the core.
	host := "captive.apple.com"
	// The core may still be loading its providers for a moment after the TUN
	// interface appears and rejects connections meanwhile, so retry briefly.
	var reqErr error
	for i := 0; i < 20; i++ {
		resp, err := netx.Direct(10 * time.Second).Get("http://" + host + "/hotspot-detect.html")
		if reqErr = err; err == nil {
			resp.Body.Close()
			t.Logf("direct request under TUN ok after %d attempt(s)", i+1)
			break
		}
		t.Logf("attempt %d: %v", i+1, err)
		time.Sleep(500 * time.Millisecond)
	}
	if reqErr != nil {
		t.Fatalf("direct request under TUN: %v", reqErr)
	}
	e2eWait(t, 10*time.Second, "TUN connection", func() bool {
		for _, c := range append(s.Connections(), s.ClosedConnections()...) {
			if strings.HasPrefix(c.Host, host) && strings.EqualFold(c.Type, "tun") {
				t.Logf("captured: %s via %s (%s, chain %v)", c.Host, c.Type, c.Process, c.Chains)
				return true
			}
		}
		return false
	})

	liveCoexist(t, s)

	if err := s.SetTUN(false); err != nil {
		t.Fatalf("SetTUN(false): %v", err)
	}
	e2eWait(t, 30*time.Second, "TUN off", func() bool { st := s.Status(); return st.Core == model.CoreRunning && !st.TUN })
	e2eWait(t, 15*time.Second, "routes restored", func() bool { _, ok := netinfo.OtherTUN(context.Background()); return !ok })
	if snap, err := sysdns.Snapshot(); err == nil {
		t.Logf("system DNS after TUN off: %s", snap)
		if strings.Contains(string(snap), sysdns.TUNServers[0]) {
			t.Error("system DNS not restored after TUN off")
		}
	}
	if _, err := netx.Direct(15 * time.Second).Get("http://" + host + "/hotspot-detect.html"); err != nil {
		t.Fatalf("network after TUN off: %v", err)
	}
	liveDiagnostics(t, s, root)
	if err := s.StopCore(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("LIVE TUN TEST OK")
}

// tunConnTo reports whether a TUN connection to ip started after since.
func tunConnTo(s *Service, ip string, since time.Time) bool {
	for _, c := range append(s.Connections(), s.ClosedConnections()...) {
		if strings.EqualFold(c.Type, "tun") && (c.DstIP == ip || strings.HasPrefix(c.Host, ip+":")) && !c.Start.Before(since) {
			return true
		}
	}
	return false
}

// liveCoexist checks aTrust 兼容 against the real core: with a (simulated)
// aTrust route to 1.1.1.1/32 the address must leave the TUN (the automatic
// system DNS stays), and come back when aTrust goes away.
func liveCoexist(t *testing.T, s *Service) {
	const ip = "1.1.1.1"
	cl := netx.Direct(10 * time.Second)
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse } // stay on the IP
	get := func() error {
		cl.CloseIdleConnections() // a kept-alive connection would hide the route in use
		resp, err := cl.Get("http://" + ip + "/")
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	t0 := time.Now()
	if err := get(); err != nil {
		t.Fatalf("request to %s under TUN: %v", ip, err)
	}
	e2eWait(t, 10*time.Second, "TUN connection to "+ip, func() bool { return tunConnTo(s, ip, t0.Add(-time.Second)) })
	t.Logf("before aTrust: %s goes through the TUN", ip)

	fake := atrust.Info{Processes: []string{"aTrustAgent"}, Ifaces: []string{"utun99"}, CIDRs: []string{ip + "/32"}, Domains: []string{"corp.example.com"}}
	s.mu.Lock()
	s.d.vpnDetect = func(context.Context, func(string) bool) atrust.Info { return fake }
	s.mu.Unlock()
	s.recheckCoexist()
	if !s.Status().ATrust.Running {
		t.Fatal("simulated aTrust not picked up")
	}
	s.mu.Lock()
	rt := string(s.state.runtimeYAML)
	s.mu.Unlock()
	if !strings.Contains(rt, "route-exclude-address") || !strings.Contains(rt, "IP-CIDR,"+ip+"/32,DIRECT") {
		t.Fatalf("runtime config lacks the aTrust layer:\n%s", tail(rt, 60))
	}
	if snap, err := sysdns.Snapshot(); err == nil {
		t.Logf("system DNS with aTrust: %s", snap)
		if !strings.Contains(string(snap), sysdns.TUNServers[0]) {
			t.Error("auto system DNS dropped while aTrust runs")
		}
	}
	// A name resolved to a fake IP before the reload (still in the system
	// cache) must keep working after it.
	for i := 0; i < 6; i++ {
		resp, err := netx.Direct(10 * time.Second).Get("http://captive.apple.com/hotspot-detect.html")
		if err == nil {
			resp.Body.Close()
			t.Logf("fake-ip name after the aTrust reload: ok (attempt %d)", i+1)
			break
		}
		t.Logf("fake-ip name after the aTrust reload, attempt %d: %v", i+1, err)
		time.Sleep(time.Second)
	}
	routes := func(when string) {
		out, _ := exec.Command("/sbin/route", "-n", "get", ip).CombinedOutput()
		tab, _ := exec.Command("/usr/sbin/netstat", "-rn", "-f", "inet").Output()
		var keep []string
		for _, l := range strings.Split(string(tab), "\n") {
			if f := strings.Fields(l); len(f) > 3 && (strings.HasPrefix(f[0], "1.1.1") || strings.HasPrefix(f[3], "utun") || f[0] == "default") {
				keep = append(keep, l)
			}
		}
		t.Logf("routes %s:\n%s\n%s", when, strings.TrimSpace(string(out)), strings.Join(keep, "\n"))
	}
	routes("with aTrust")
	time.Sleep(time.Second)
	t1 := time.Now()
	var err error
	for i := 0; i < 10; i++ {
		if err = get(); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("request to %s with aTrust excluded: %v", ip, err)
	}
	time.Sleep(2 * time.Second)
	if tunConnTo(s, ip, t1) {
		t.Errorf("%s still enters the TUN although aTrust excludes it", ip)
	} else {
		t.Logf("with aTrust: %s bypasses the TUN", ip)
	}

	s.mu.Lock()
	s.d.vpnDetect = func(context.Context, func(string) bool) atrust.Info { return atrust.Info{} }
	s.mu.Unlock()
	s.recheckCoexist()
	if s.Status().ATrust.Running {
		t.Fatal("aTrust still reported after it went away")
	}
	if snap, err := sysdns.Snapshot(); err == nil && !strings.Contains(string(snap), sysdns.TUNServers[0]) {
		t.Errorf("auto system DNS gone after aTrust quit: %s", snap)
	}
	routes("after aTrust quit")
	t2 := time.Now()
	time.Sleep(time.Second)
	if err := get(); err != nil {
		t.Fatalf("request after aTrust quit: %v", err)
	}
	e2eWait(t, 10*time.Second, "TUN connection after aTrust quit", func() bool { return tunConnTo(s, ip, t2) })
	t.Logf("after aTrust: %s goes through the TUN again", ip)
}

// liveDiagnostics checks Lanflow's run log and the diagnostics export on a
// real system.
func liveDiagnostics(t *testing.T, s *Service, root string) {
	var logText strings.Builder
	for _, e := range s.AppLogs() {
		logText.WriteString(e.Level + " " + e.Payload + "\n")
	}
	lt := logText.String()
	for _, want := range []string{"start core: ok", "TUN on: ok", "system DNS: set", "aTrust: processes [aTrustAgent]", "system DNS: restored", "TUN off: ok"} {
		if !strings.Contains(lt, want) {
			t.Errorf("app log lacks %q", want)
		}
	}
	t.Logf("app log (tail):\n%s", tail(lt, 25))
	out := filepath.Join(root, "diagnostics.zip")
	if err := s.ExportDiagnostics(out); err != nil {
		t.Fatalf("export diagnostics: %v", err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	have := map[string]uint64{}
	for _, f := range zr.File {
		have[f.Name] = f.UncompressedSize64
	}
	for _, want := range []string{"system.txt", "config/runtime.yaml", "config/settings.json", "logs/lanflow.log", "logs/mihomo.log", "logs/mihomo-tun.log", "network/routes.txt", "network/dns.txt", "network/proxy.txt"} {
		if have[want] == 0 {
			t.Errorf("diagnostics lack %s", want)
		}
	}
	t.Logf("diagnostics: %v", have)
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
