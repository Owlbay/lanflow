//go:build darwin

package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"lanflow/internal/locate"
	"lanflow/internal/model"
	"lanflow/internal/netx"
	"lanflow/internal/platform/netinfo"
	"lanflow/internal/platform/sysdns"
	"lanflow/internal/platform/sysproxy"
)

// The live end-to-end tests of scripts/vm-e2e.sh. They run inside a
// Parallels macOS VM against a proxy "fleet" on the host (one inbound per
// protocol, scripts/vm-e2e-fleet.sh) and an echo server that reports the
// client's source address: a request that came out of the fleet arrives
// from the fleet's address, a direct one from the VM's. Opt-in through
// LANFLOW_LIVE_E2E=1; the script sets everything else:
//
//	LANFLOW_E2E_DATA    run directory (data/, log/, cache/ below it; the
//	                    helper, when installed, is installed for data/)
//	LANFLOW_E2E_RES     the .app's Contents/Resources (core, helper)
//	LANFLOW_E2E_FLEET   directory with the fleet's sub.yaml, ports, secret
//	LANFLOW_E2E_HOST    the fleet / echo address (the VM's default gateway)
//	LANFLOW_E2E_TUN=1   the helper is installed: run the TUN parts
//	LANFLOW_E2E_IFACE   interface to capture on (tcpdump, /dev/bpf readable)
//	LANFLOW_E2E_OUT     where captures and evidence files go
//	LANFLOW_E2E_SYSDNS  the system DNS the script set (comma separated)
//	LANFLOW_E2E_ASK=1   the script answers ask-* marker files (root actions)
//
// Every checked item is printed as "E2E|<item>|PASS/FAIL/INFO|<evidence>"
// for the report.
const e2eGroup = "节点选择"

var e2eNodes = []string{"ss", "ss2022", "vmess", "vless", "trojan", "hysteria2", "tuic", "anytls", "socks5", "http"}

// e2eInbound is the fleet listener name of each node (scripts/vm-e2e-fleet.sh).
var e2eInbound = map[string]string{"ss": "ss-in", "ss2022": "ss2022-in", "vmess": "vmess-in", "vless": "vless-in", "trojan": "trojan-in", "hysteria2": "hy2-in", "tuic": "tuic-in", "anytls": "anytls-in", "socks5": "socks-in", "http": "http-in"}

type e2eEnv struct {
	root, res, out string
	host           string
	ports          map[string]int
	secret         string
	sub            string
	tun, ask       bool
	asRoot         bool // running as root (the VM denies LAN access to non-root, non-platform processes)
	v6Off          bool // the script turned the guest's IPv6 off: no v6 may leave at all
	iface, svc     string
	sysDNS         []string
	vmIP           string
}

func e2eSetup(t *testing.T) *e2eEnv {
	t.Helper()
	if os.Getenv("LANFLOW_LIVE_E2E") != "1" {
		t.Skip("set LANFLOW_LIVE_E2E=1 (in a VM, via scripts/vm-e2e.sh) to run the live e2e tests")
	}
	e := &e2eEnv{
		root: os.Getenv("LANFLOW_E2E_DATA"), res: os.Getenv("LANFLOW_E2E_RES"), out: os.Getenv("LANFLOW_E2E_OUT"),
		host: os.Getenv("LANFLOW_E2E_HOST"), tun: os.Getenv("LANFLOW_E2E_TUN") == "1", ask: os.Getenv("LANFLOW_E2E_ASK") == "1",
		iface: os.Getenv("LANFLOW_E2E_IFACE"), svc: os.Getenv("LANFLOW_E2E_SVC"), ports: map[string]int{},
		asRoot: os.Geteuid() == 0, v6Off: os.Getenv("LANFLOW_E2E_V6OFF") == "1",
	}
	fleet := os.Getenv("LANFLOW_E2E_FLEET")
	if e.root == "" || e.res == "" || fleet == "" || e.host == "" {
		t.Fatal("LANFLOW_E2E_DATA, LANFLOW_E2E_RES, LANFLOW_E2E_FLEET and LANFLOW_E2E_HOST are required")
	}
	if e.iface == "" {
		e.iface = "en0"
	}
	if e.out == "" {
		e.out = filepath.Join(e.root, "out")
	}
	os.MkdirAll(e.out, 0o755)
	e.sub = filepath.Join(fleet, "sub.yaml")
	if b, err := os.ReadFile(filepath.Join(fleet, "secret")); err == nil {
		e.secret = strings.TrimSpace(string(b))
	}
	b, err := os.ReadFile(filepath.Join(fleet, "ports"))
	if err != nil {
		t.Fatalf("fleet ports: %v", err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			e.ports[k], _ = strconv.Atoi(v)
		}
	}
	for _, n := range append([]string{"echo", "controller"}, e2eNodes...) {
		if e.ports[n] == 0 {
			t.Fatalf("fleet ports file lacks %s", n)
		}
	}
	if v := os.Getenv("LANFLOW_E2E_SYSDNS"); v != "" {
		e.sysDNS = strings.Split(v, ",")
	}
	if ifc, err := net.InterfaceByName(e.iface); err == nil {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				e.vmIP = ipn.IP.String()
			}
		}
	}
	if e.vmIP == "" {
		e.vmIP = netinfo.LANAddress()
	}
	if e.vmIP == "" {
		t.Fatalf("cannot find the VM's address on %s", e.iface)
	}
	t.Logf("e2e: vm %s, fleet %s, nodes %v, tun=%v root=%v v6off=%v", e.vmIP, e.host, e.ports, e.tun, e.asRoot, e.v6Off)
	return e
}

// echoURL is the echo server by IP (direct-able, LAN) or by the name only
// the fleet resolves (so a request can only succeed through the fleet,
// which is what the TUN checks need: LAN destinations never enter a TUN).
func (e *e2eEnv) echoURL(byName bool) string {
	h := e.host
	if byName {
		h = "echo.fleet.test"
	}
	return fmt.Sprintf("http://%s:%d/", h, e.ports["echo"])
}

func e2eReport(t *testing.T, item, status, evidence string) {
	t.Helper()
	evidence = strings.ReplaceAll(strings.TrimSpace(evidence), "\n", " ")
	t.Logf("%s: %s (%s)", item, status, evidence)
	fmt.Printf("E2E|%s|%s|%s\n", item, status, evidence)
}

// e2eCheck reports item as PASS/FAIL and fails the test (not fatally) on FAIL.
func e2eCheck(t *testing.T, item string, ok bool, evidence string) bool {
	t.Helper()
	if ok {
		e2eReport(t, item, "PASS", evidence)
	} else {
		e2eReport(t, item, "FAIL", evidence)
		t.Errorf("%s: %s", item, evidence)
	}
	return ok
}

func e2eDNS() model.DNSSettings {
	// Plain-IP upstreams only, so the leak audit can tell the core's own
	// DNS (allowed to leave the machine) from an application's.
	return model.DNSSettings{TakeOver: true, EnhancedMode: "fake-ip", Nameserver: []string{"223.6.6.6"}, ProxyServerNameserver: []string{"223.6.6.6"}, FakeIPFilter: []string{"*.lan", "*.local"}, UseSystemHosts: true}
}

// newService starts Lanflow's service on the run directory with the fleet
// subscription in use and the core running (TUN off, rule mode).
func (e *e2eEnv) newService(t *testing.T) (*Service, model.Settings) {
	t.Helper()
	s, err := NewService(Options{DataDir: filepath.Join(e.root, "data"), LogDir: filepath.Join(e.root, "log"), CacheDir: filepath.Join(e.root, "cache"), ResourceDir: e.res})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			for _, f := range []string{filepath.Join(e.root, "log", "mihomo.log"), filepath.Join(e.root, "data", "core", "mihomo-tun.log")} {
				if b, err := os.ReadFile(f); err == nil {
					t.Logf("%s (tail):\n%s", filepath.Base(f), tail(string(b), 30))
				}
			}
		}
	})
	set := s.Settings()
	set.CorePath, set.MixedPort, set.ProbeExits, set.DisableUpdateCheck = filepath.Join(e.res, "mihomo"), freePort(t), false, true
	set.TUN, set.Mode, set.AutoSystemProxy, set.DisconnectOnSwitch = false, model.ModeRule, false, false
	set.DNS = e2eDNS()
	if err := s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if subs := s.Subscriptions(); len(subs) > 0 {
		if err := s.UseSubscription(subs[0].ID); err != nil {
			t.Fatal(err)
		}
	} else if _, err := s.ImportSubscriptionFile(e.sub, true); err != nil {
		t.Fatal(err)
	}
	if s.Status().Core != model.CoreRunning {
		if err := s.StartCore(); err != nil {
			t.Fatal(err)
		}
	}
	e2eWait(t, 30*time.Second, "core running", func() bool { return s.Status().Core == model.CoreRunning })
	e2eWait(t, 10*time.Second, "fleet group loaded", func() bool { return e.groupNow(s) != "" })
	if err := s.SetMode(model.ModeRule); err != nil {
		t.Fatal(err)
	}
	return s, set
}

func (e *e2eEnv) groupNow(s *Service) string {
	for _, g := range s.Proxies().Groups {
		if g.Name == e2eGroup {
			return g.Now
		}
	}
	return ""
}

func (e *e2eEnv) selectNode(t *testing.T, s *Service, node string) {
	t.Helper()
	if err := s.SelectProxy(e2eGroup, node); err != nil {
		t.Fatalf("select %s: %v", node, err)
	}
	e2eWait(t, 5*time.Second, "node "+node+" selected", func() bool { return e.groupNow(s) == node })
}

type echoReply struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	Addr string `json:"addr"`
	Host string `json:"host"`
}

// fetchEcho GETs the echo server with a fresh connection (a kept-alive one
// would hide which node or mode handled the request).
func fetchEcho(c *http.Client, url string) (echoReply, error) {
	c.CloseIdleConnections()
	defer c.CloseIdleConnections()
	resp, err := c.Get(url)
	if err != nil {
		return echoReply{}, err
	}
	defer resp.Body.Close()
	var r echoReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&r); err != nil {
		return echoReply{}, fmt.Errorf("echo status %d: %v", resp.StatusCode, err)
	}
	return r, nil
}

// fetchEchoRetry retries for up to d (the core may still be loading after
// a reload or TUN switch).
func fetchEchoRetry(c *http.Client, url string, d time.Duration) (echoReply, int, error) {
	deadline := time.Now().Add(d)
	var r echoReply
	var err error
	for n := 1; ; n++ {
		if r, err = fetchEcho(c, url); err == nil || time.Now().After(deadline) {
			return r, n, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// openStream opens the echo's long-lived stream and returns after the
// first line arrived.
func openStream(c *http.Client, url string, secs int) (*http.Response, *bufio.Reader, string, error) {
	c.CloseIdleConnections()
	resp, err := c.Get(fmt.Sprintf("%sstream?secs=%d", url, secs))
	if err != nil {
		return nil, nil, "", err
	}
	rd := bufio.NewReader(resp.Body)
	line, err := rd.ReadString('\n')
	if err != nil {
		resp.Body.Close()
		return nil, nil, "", err
	}
	return resp, rd, strings.TrimSpace(line), nil
}

// nodeConn finds a Lanflow connection to host that the given node handled.
func (e *e2eEnv) nodeConn(s *Service, hostPrefix, node string, since time.Time, tun bool) (model.Connection, bool) {
	for _, c := range append(s.Connections(), s.ClosedConnections()...) {
		if !strings.HasPrefix(c.Host, hostPrefix) || c.Start.Before(since) || len(c.Chains) == 0 || c.Chains[len(c.Chains)-1] != node {
			continue
		}
		if tun && !strings.EqualFold(c.Type, "tun") {
			continue
		}
		return c, true
	}
	return model.Connection{}, false
}

// connDump lists the most recent connections (for evidence when a lookup fails).
func connDump(s *Service, n int) string {
	all := append(s.Connections(), s.ClosedConnections()...)
	slices.SortFunc(all, func(a, b model.Connection) int { return b.Start.Compare(a.Start) })
	if len(all) > n {
		all = all[:n]
	}
	var parts []string
	for _, c := range all {
		parts = append(parts, fmt.Sprintf("%s/%s/%v", c.Host, c.Type, c.Chains))
	}
	return "recent conns: " + strings.Join(parts, "; ")
}

type fleetConn struct {
	ID       string `json:"id"`
	Metadata struct {
		InboundPort     string `json:"inboundPort"`
		InboundName     string `json:"inboundName"`
		DestinationPort string `json:"destinationPort"`
		DestinationIP   string `json:"destinationIP"`
		Host            string `json:"host"`
		SourceIP        string `json:"sourceIP"`
		Network         string `json:"network"`
	} `json:"metadata"`
}

// fleetConns asks the fleet's controller (host, bridge address) for its
// open connections: the inbound port tells which protocol carried them.
func (e *e2eEnv) fleetConns() ([]fleetConn, error) {
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s:%d/connections", e.host, e.ports["controller"]), nil)
	req.Header.Set("Authorization", "Bearer "+e.secret)
	resp, err := netx.Direct(5 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Connections []fleetConn `json:"connections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Connections, nil
}

// fleetSaw waits until the fleet has an open connection to the echo
// through node's listener (by port, or by name: QUIC inbounds report port
// 0) and returns its inbound name.
func (e *e2eEnv) fleetSaw(node string, d time.Duration) (string, bool) {
	deadline := time.Now().Add(d)
	for {
		conns, _ := e.fleetConns()
		for _, c := range conns {
			if (c.Metadata.InboundPort == strconv.Itoa(e.ports[node]) || c.Metadata.InboundName == e2eInbound[node]) && c.Metadata.DestinationPort == strconv.Itoa(e.ports["echo"]) {
				return c.Metadata.InboundName + ":" + c.Metadata.InboundPort, true
			}
		}
		if time.Now().After(deadline) {
			return "", false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// tunUp turns TUN on and waits for the routes and the system DNS.
func (e *e2eEnv) tunUp(t *testing.T, s *Service) string {
	t.Helper()
	t0 := time.Now()
	if err := s.SetTUN(true); err != nil {
		t.Fatalf("SetTUN(true): %v", err)
	}
	e2eWait(t, 40*time.Second, "TUN running", func() bool { st := s.Status(); return st.Core == model.CoreRunning && st.TUN })
	var tunIf string
	e2eWait(t, 15*time.Second, "TUN routes", func() bool {
		name, ok := netinfo.OtherTUN(context.Background())
		tunIf = name
		return ok
	})
	e2eWait(t, 10*time.Second, "system DNS set", func() bool {
		snap, err := sysdns.Snapshot()
		return err == nil && strings.Contains(string(snap), sysdns.TUNServers[0])
	})
	t.Logf("TUN up (%s) after %v", tunIf, time.Since(t0).Round(time.Millisecond))
	return tunIf
}

func (e *e2eEnv) tunDown(t *testing.T, s *Service) {
	t.Helper()
	if err := s.SetTUN(false); err != nil {
		t.Fatalf("SetTUN(false): %v", err)
	}
	e2eWait(t, 40*time.Second, "TUN off", func() bool { st := s.Status(); return st.Core == model.CoreRunning && !st.TUN })
	e2eWait(t, 15*time.Second, "routes restored", func() bool { _, ok := netinfo.OtherTUN(context.Background()); return !ok })
}

// TestLiveProxyMatrix: every fleet protocol through the mixed port and
// through the TUN, the modes, custom-rule reloads, node switching while a
// connection is open, system proxy on/off and a core restart.
func TestLiveProxyMatrix(t *testing.T) {
	e := e2eSetup(t)
	s, set := e.newService(t)
	defer s.Shutdown()
	via := func() *http.Client { return netx.ViaPort(set.MixedPort, 15*time.Second) }
	ipURL, nameURL := e.echoURL(false), e.echoURL(true)

	r, err := fetchEcho(netx.Direct(10*time.Second), ipURL)
	e2eCheck(t, "echo/direct-baseline", err == nil && r.IP == e.vmIP, fmt.Sprintf("without proxy the echo sees %s (vm %s) err=%v", r.Addr, e.vmIP, err))

	for _, node := range e2eNodes {
		e.selectNode(t, s, node)
		e.nodeCheck(t, s, node, "mixed", via(), ipURL, e.host+":", false)
	}

	// Switching nodes while a connection is open: by default it survives
	// (and new requests take the new node); with 切换节点时断开 it is cut.
	e.selectNode(t, s, "ss")
	if resp, rd, first, err := openStream(via(), ipURL, 6); err == nil {
		for i := 0; i < 4; i++ {
			rd.ReadString('\n')
		}
		e.selectNode(t, s, "trojan")
		since := time.Now()
		lines, rerr := 5, error(nil)
		for {
			if _, rerr = rd.ReadString('\n'); rerr != nil {
				break
			}
			lines++
		}
		resp.Body.Close()
		r, _, ferr := fetchEchoRetry(via(), ipURL, 5*time.Second)
		newNode := false
		if resp2, _, _, err2 := openStream(via(), ipURL, 4); err2 == nil {
			newNode = e2eWait2(4*time.Second, func() bool { _, ok := e.nodeConn(s, e.host+":", "trojan", since.Add(-time.Second), false); return ok })
			resp2.Body.Close()
		}
		e2eCheck(t, "switch/keep-connections", rerr == io.EOF && lines >= 20 && ferr == nil && r.IP == e.host && newNode,
			fmt.Sprintf("stream via ss (%s) kept %d lines after switching to trojan, ended with %v; new request via trojan=%v", first, lines, rerr, newNode))
	} else {
		e2eCheck(t, "switch/keep-connections", false, "stream: "+err.Error())
	}
	set2 := s.Settings()
	set2.DisconnectOnSwitch = true
	if err := s.SaveSettings(set2); err != nil {
		t.Fatal(err)
	}
	e.selectNode(t, s, "ss")
	time.Sleep(1500 * time.Millisecond) // let the previous stream's connection leave the snapshot
	opened := time.Now().Add(-time.Second)
	if resp, rd, _, err := openStream(via(), ipURL, 12); err == nil {
		// This connection must be in Lanflow's snapshot (stream, ~1 s)
		// before the switch can close it.
		seen := e2eWait2(6*time.Second, func() bool {
			for _, c := range s.Connections() {
				if strings.HasPrefix(c.Host, e.host+":") && !c.Start.Before(opened) && len(c.Chains) > 0 && c.Chains[len(c.Chains)-1] == "ss" {
					return true
				}
			}
			return false
		})
		t.Logf("disconnect-on-switch: stream connection in snapshot=%v", seen)
		rd.ReadString('\n')
		t0 := time.Now()
		e.selectNode(t, s, "vmess")
		var rerr error
		for {
			if _, rerr = rd.ReadString('\n'); rerr != nil {
				break
			}
			if time.Since(t0) > 8*time.Second {
				break
			}
		}
		resp.Body.Close()
		e2eCheck(t, "switch/disconnect-on-switch", rerr != nil && time.Since(t0) < 5*time.Second, fmt.Sprintf("stream cut %v after the switch (err %v)", time.Since(t0).Round(time.Millisecond), rerr))
	} else {
		e2eCheck(t, "switch/disconnect-on-switch", false, "stream: "+err.Error())
	}
	set2.DisconnectOnSwitch = false
	s.SaveSettings(set2)

	// Modes and a custom rule (recompose + hot reload).
	e.selectNode(t, s, "ss")
	cr, err := s.SaveCustomRule(model.CustomRule{Type: "IP-CIDR", Payload: e.host + "/32", Target: "DIRECT"})
	if err != nil {
		t.Fatalf("SaveCustomRule: %v", err)
	}
	e2eWait(t, 15*time.Second, "custom rule live", func() bool {
		for _, r := range s.Rules() {
			if r.Custom && strings.HasPrefix(r.Payload, e.host) {
				return true
			}
		}
		return false
	})
	r, _, err = fetchEchoRetry(via(), ipURL, 10*time.Second)
	e2eCheck(t, "modes/rule+custom-DIRECT-rule(reload)", err == nil && r.IP == e.vmIP, fmt.Sprintf("echo saw %s err=%v (expected direct %s)", r.Addr, err, e.vmIP))
	if err := s.SetMode(model.ModeGlobal); err != nil {
		t.Fatal(err)
	}
	globalNow := ""
	for _, g := range s.Proxies().Groups {
		if g.Name == "GLOBAL" {
			globalNow = g.Now
		}
	}
	e2eCheck(t, "modes/global-selects-an-exit", globalNow != "" && globalNow != "DIRECT" && globalNow != "REJECT", fmt.Sprintf("GLOBAL group now=%q after switching to global mode (Mihomo's default is DIRECT)", globalNow))
	r, _, err = fetchEchoRetry(via(), ipURL, 10*time.Second)
	e2eCheck(t, "modes/global-ignores-rules", err == nil && r.IP == e.host, fmt.Sprintf("echo saw %s err=%v (expected fleet %s)", r.Addr, err, e.host))
	if err := s.SetMode(model.ModeDirect); err != nil {
		t.Fatal(err)
	}
	r, _, err = fetchEchoRetry(via(), ipURL, 10*time.Second)
	e2eCheck(t, "modes/direct", err == nil && r.IP == e.vmIP, fmt.Sprintf("echo saw %s err=%v (expected direct %s)", r.Addr, err, e.vmIP))
	if err := s.SetMode(model.ModeRule); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCustomRule(cr.ID); err != nil {
		t.Fatal(err)
	}
	e2eWait(t, 15*time.Second, "custom rule gone", func() bool {
		for _, r := range s.Rules() {
			if r.Custom && strings.HasPrefix(r.Payload, e.host) {
				return false
			}
		}
		return s.Status().Core == model.CoreRunning
	})
	r, _, err = fetchEchoRetry(via(), ipURL, 10*time.Second)
	e2eCheck(t, "modes/rule-after-rule-removed(reload)", err == nil && r.IP == e.host, fmt.Sprintf("echo saw %s err=%v (expected fleet %s)", r.Addr, err, e.host))

	// DNS settings change = another reload.
	d := e2eDNS()
	d.FakeIPFilter = append(d.FakeIPFilter, "+.e2e-reload.test")
	if err := s.ApplyDNS(d); err != nil {
		t.Fatalf("ApplyDNS: %v", err)
	}
	e2eWait(t, 15*time.Second, "dns reload", func() bool {
		s.mu.Lock()
		rt := string(s.state.runtimeYAML)
		s.mu.Unlock()
		return strings.Contains(rt, "e2e-reload.test") && s.Status().Core == model.CoreRunning
	})
	r, _, err = fetchEchoRetry(via(), ipURL, 10*time.Second)
	e2eCheck(t, "reload/dns-settings", err == nil && r.IP == e.host, fmt.Sprintf("runtime has the new fake-ip-filter; echo saw %s err=%v", r.Addr, err))

	// System proxy on/off puts the previous setting back.
	before, _ := sysproxy.Get()
	if err := s.SetSystemProxy(true); err != nil {
		t.Fatalf("SetSystemProxy(true): %v", err)
	}
	on, _ := sysproxy.Get()
	okOn := on.Enabled && on.Host == "127.0.0.1" && on.Port == set.MixedPort
	// Core restart keeps the system proxy.
	st0 := s.Status().StartedAt
	t0 := time.Now()
	if err := s.RestartCore(); err != nil {
		t.Fatalf("RestartCore: %v", err)
	}
	e2eWait(t, 30*time.Second, "restarted", func() bool { st := s.Status(); return st.Core == model.CoreRunning && !st.StartedAt.Equal(st0) })
	restartDur := time.Since(t0)
	r, tries, err := fetchEchoRetry(via(), ipURL, 15*time.Second)
	afterRestart, _ := sysproxy.Get()
	e2eCheck(t, "core/restart", err == nil && r.IP == e.host && afterRestart.Enabled && afterRestart.Port == set.MixedPort,
		fmt.Sprintf("restart took %v; echo via fleet after %d try(s) err=%v; system proxy still %+v", restartDur.Round(time.Millisecond), tries, err, afterRestart))
	if err := s.SetSystemProxy(false); err != nil {
		t.Fatalf("SetSystemProxy(false): %v", err)
	}
	after, _ := sysproxy.Get()
	e2eCheck(t, "sysproxy/on-off-restores", okOn && after.Enabled == before.Enabled && after.Host == before.Host && after.Port == before.Port,
		fmt.Sprintf("before %+v; on %+v; after %+v", before, on, after))

	if e.tun {
		dnsBefore, _ := sysdns.Snapshot()
		routesBefore := routeTable()
		tunIf := e.tunUp(t, s)
		t.Logf("TUN interface %s", tunIf)
		for _, node := range e2eNodes {
			e.selectNode(t, s, node)
			e.nodeCheck(t, s, node, "tun", netx.Direct(10*time.Second), nameURL, "echo.fleet.test:", true)
		}
		// A LAN destination stays off the TUN (direct), as designed.
		r, err := fetchEcho(netx.Direct(10*time.Second), ipURL)
		e2eCheck(t, "tun/lan-destination-stays-direct", err == nil && r.IP == e.vmIP, fmt.Sprintf("LAN echo by IP under TUN saw %s err=%v (LAN never enters the TUN, by design)", r.Addr, err))
		e.tunDown(t, s)
		dnsAfter, _ := sysdns.Snapshot()
		routesAfter := routeTable()
		e2eCheck(t, "tun/off-restores-routes-dns", bytes.Equal(dnsBefore, dnsAfter) && routesBefore == routesAfter,
			fmt.Sprintf("dns before=%s after=%s; default routes before=[%s] after=[%s]", dnsBefore, dnsAfter, routesBefore, routesAfter))
	}
	if err := s.StopCore(); err != nil {
		t.Fatal(err)
	}
}

// nodeCheck proves node carried a request: the echo saw the fleet's
// address, and while a stream is held open both Lanflow (connection with
// the node in its chain) and the fleet (connection on the node's inbound)
// list it. A short request alone may never show up in a connection
// snapshot, hence the stream.
func (e *e2eEnv) nodeCheck(t *testing.T, s *Service, node, via string, c *http.Client, url, hostPrefix string, tun bool) {
	t.Helper()
	since := time.Now().Add(-2 * time.Second)
	r, tries, err := fetchEchoRetry(c, url, 15*time.Second)
	okEcho := err == nil && r.IP == e.host
	chain, inbound := "", "-"
	if resp, _, _, serr := openStream(c, url, 6); serr == nil {
		e2eWait2(5*time.Second, func() bool {
			if chain == "" {
				if cn, ok := e.nodeConn(s, hostPrefix, node, since, tun); ok {
					chain = fmt.Sprintf("%v/%s", cn.Chains, cn.Type)
				}
			}
			if inbound == "-" {
				if name, ok := e.fleetSaw(node, 0); ok {
					inbound = name
				}
			}
			return chain != "" && inbound != "-"
		})
		resp.Body.Close()
	} else {
		inbound = "stream error: " + serr.Error()
	}
	ok := okEcho && chain != "" && inbound != "-" && !strings.HasPrefix(inbound, "stream")
	ev := fmt.Sprintf("echo saw %s after %d try(s) err=%v; lanflow conn %s; fleet inbound %s", r.Addr, tries, err, chain, inbound)
	if !ok {
		ev += "; " + connDump(s, 6)
	}
	e2eCheck(t, "protocol/"+node+"/"+via, ok, ev)
}

// e2eWait2 polls without failing.
func e2eWait2(d time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if ok() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// routeTable is the IPv4 default / TUN routes as one line (for before/after).
func routeTable() string {
	out, _ := exec.Command("/usr/sbin/netstat", "-rn", "-f", "inet").Output()
	var keep []string
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) > 3 && (f[0] == "default" || strings.HasPrefix(f[3], "utun")) {
			keep = append(keep, f[0]+">"+f[1]+"@"+f[3])
		}
	}
	return strings.Join(keep, " ")
}

// ---- leak audit ----

type capture struct {
	cmd  *exec.Cmd
	file string
	errb bytes.Buffer
}

// startCapture runs tcpdump on iface (the script made /dev/bpf* readable).
func startCapture(t *testing.T, iface, file string) *capture {
	t.Helper()
	c := &capture{file: file}
	c.cmd = exec.Command("/usr/sbin/tcpdump", "-i", iface, "-n", "-U", "-w", file, "not arp")
	c.cmd.Stderr = &c.errb
	if err := c.cmd.Start(); err != nil {
		t.Fatalf("tcpdump: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if c.cmd.ProcessState != nil || strings.Contains(c.errb.String(), "Permission denied") {
		t.Fatalf("tcpdump did not start: %s", c.errb.String())
	}
	return c
}

// stop ends the capture and returns the packets as `tcpdump -n -tt -q` text
// (also saved next to the pcap).
func (c *capture) stop(t *testing.T) string {
	t.Helper()
	time.Sleep(1500 * time.Millisecond)
	c.cmd.Process.Signal(syscall.SIGINT)
	c.cmd.Wait()
	out, err := exec.Command("/usr/sbin/tcpdump", "-n", "-tt", "-q", "-r", c.file).Output()
	if err != nil {
		t.Fatalf("read capture: %v (%s)", err, c.errb.String())
	}
	syn, _ := exec.Command("/usr/sbin/tcpdump", "-n", "-tt", "-q", "-r", c.file, "tcp[tcpflags] & tcp-syn != 0").Output()
	os.WriteFile(strings.TrimSuffix(c.file, ".pcap")+".txt", out, 0o644)
	// Flows that began inside the window, marked so tallyDump can tell a
	// new connection from the tail of one that predates the capture.
	return string(out) + "\n" + synMarker + "\n" + string(syn)
}

const synMarker = "#SYN-FLOWS"

type pkt struct {
	v6           bool
	src, dst     string
	sport, dport int
	proto        string
	raw          string
}

func parseDump(text string) []pkt {
	var out []pkt
	for _, l := range strings.Split(text, "\n") {
		f := strings.Fields(l)
		if len(f) < 6 || (f[1] != "IP" && f[1] != "IP6") || f[3] != ">" {
			continue
		}
		p := pkt{v6: f[1] == "IP6", raw: l}
		rest := strings.Join(f[5:], " ")
		p.proto = strings.ToLower(strings.TrimSuffix(strings.SplitN(rest, " ", 2)[0], ","))
		hasPort := p.proto == "tcp" || p.proto == "udp"
		p.src, p.sport = splitAddrPort(f[2], hasPort)
		p.dst, p.dport = splitAddrPort(strings.TrimSuffix(f[4], ":"), hasPort)
		out = append(out, p)
	}
	return out
}

func splitAddrPort(s string, hasPort bool) (string, int) {
	if !hasPort {
		return s, 0
	}
	i := strings.LastIndex(s, ".")
	if i < 0 {
		return s, 0
	}
	port, _ := strconv.Atoi(s[i+1:])
	return s[:i], port
}

var (
	fakeIPNet  = mustCIDR("198.18.0.0/15")
	v6LinkNet  = mustCIDR("fe80::/10")
	v6ULANet   = mustCIDR("fc00::/7")
	v6MultiNet = mustCIDR("ff00::/8")
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// classify says what a packet on the VM's interface is. The "bad" classes
// (for TUN + global mode) are everything but fleet, core-dns, multicast,
// dhcp, harness, v6-local and foreign.
func (e *e2eEnv) classify(p pkt, coreDNS []string) (class string, outbound bool) {
	if p.v6 {
		local := func(a string) bool {
			ip := net.ParseIP(a)
			return ip != nil && (v6LinkNet.Contains(ip) || v6ULANet.Contains(ip) || v6MultiNet.Contains(ip) || ip.IsLoopback())
		}
		if local(p.src) && local(p.dst) {
			return "v6-local", false
		}
		return "v6-egress", true
	}
	var peer string
	var peerPort, myPort int
	switch {
	case p.src == e.vmIP:
		outbound, peer, peerPort, myPort = true, p.dst, p.dport, p.sport
	case p.dst == e.vmIP:
		peer, peerPort, myPort = p.src, p.sport, p.dport
	default:
		return "foreign", false
	}
	ip := net.ParseIP(peer)
	if ip == nil || ip.IsMulticast() || ip.Equal(net.IPv4bcast) || strings.HasSuffix(peer, ".255") {
		return "multicast", outbound
	}
	if peer == e.host {
		switch {
		case p.proto == "icmp":
			return "gateway-icmp", outbound
		case peerPort == e.ports["echo"]:
			return "echo-direct", outbound
		case peerPort == e.ports["controller"]:
			return "harness", outbound
		case peerPort == 67 || peerPort == 68 || myPort == 68:
			return "dhcp", outbound
		case peerPort == 53:
			return "app-dns", outbound
		}
		for _, n := range e2eNodes {
			if peerPort == e.ports[n] {
				return "fleet", outbound
			}
		}
		return "gateway-other", outbound
	}
	if peerPort == 53 {
		if slices.Contains(coreDNS, peer) {
			return "core-dns", outbound
		}
		return "app-dns", outbound
	}
	if p.proto == "udp" && (peerPort == 19302 || peerPort == 3478) {
		return "stun", outbound
	}
	if fakeIPNet.Contains(ip) {
		return "fakeip-direct", outbound
	}
	return "other", outbound
}

type tally struct {
	out, in  map[string]int
	outPeers map[string]map[string]int // class -> "peer:port/proto" -> packets
}

func (e *e2eEnv) tallyDump(text string, coreDNS []string) tally {
	tl := tally{out: map[string]int{}, in: map[string]int{}, outPeers: map[string]map[string]int{}}
	synText := ""
	if i := strings.Index(text, synMarker); i >= 0 {
		text, synText = text[:i], text[i+len(synMarker):]
	}
	flows := map[string]bool{} // "src:sport>dst:dport" of TCP flows that started in the window
	for _, p := range parseDump(synText) {
		flows[fmt.Sprintf("%s:%d>%s:%d", p.src, p.sport, p.dst, p.dport)] = true
	}
	for _, p := range parseDump(text) {
		class, out := e.classify(p, coreDNS)
		if out && p.proto == "tcp" && !flows[fmt.Sprintf("%s:%d>%s:%d", p.src, p.sport, p.dst, p.dport)] {
			class = "tcp-stray" // tail of a connection older than the capture
		}
		if out {
			tl.out[class]++
			if tl.outPeers[class] == nil {
				tl.outPeers[class] = map[string]int{}
			}
			tl.outPeers[class][fmt.Sprintf("%s:%d/%s", p.dst, p.dport, p.proto)]++
		} else {
			tl.in[class]++
		}
	}
	return tl
}

func (tl tally) String() string {
	var b strings.Builder
	keys := make([]string, 0, len(tl.out))
	for k := range tl.out {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%d ", k, tl.out[k])
	}
	return "outbound{" + strings.TrimSpace(b.String()) + "}"
}

// peers lists the destinations of a class (at most n).
func (tl tally) peers(class string, n int) string {
	m := tl.outPeers[class]
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return m[b] - m[a] })
	if len(keys) > n {
		keys = keys[:n]
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// runtimeUpstreams returns the plain-IP DNS upstreams of the running core
// (anything else, e.g. DoH hosts, is returned separately).
func runtimeUpstreams(s *Service) (ips, others []string) {
	s.mu.Lock()
	rt := s.state.runtimeYAML
	s.mu.Unlock()
	var doc struct {
		DNS struct {
			Nameserver       []string          `yaml:"nameserver"`
			Default          []string          `yaml:"default-nameserver"`
			ProxyServer      []string          `yaml:"proxy-server-nameserver"`
			Direct           []string          `yaml:"direct-nameserver"`
			Fallback         []string          `yaml:"fallback"`
			NameserverPolicy map[string]string `yaml:"nameserver-policy"`
		} `yaml:"dns"`
	}
	yaml.Unmarshal(rt, &doc)
	all := slices.Concat(doc.DNS.Nameserver, doc.DNS.Default, doc.DNS.ProxyServer, doc.DNS.Direct, doc.DNS.Fallback)
	for _, v := range doc.DNS.NameserverPolicy {
		all = append(all, v)
	}
	for _, u := range all {
		h := u
		if i := strings.Index(h, "://"); i >= 0 {
			h = h[i+3:]
		}
		h = strings.SplitN(h, "/", 2)[0]
		h = strings.SplitN(h, "#", 2)[0]
		if hp, _, err := net.SplitHostPort(h); err == nil {
			h = hp
		}
		if net.ParseIP(h) != nil {
			ips = append(ips, h)
		} else if h != "system" && h != "" {
			others = append(others, h)
		}
	}
	return ips, others
}

// generateTraffic makes the kinds of traffic an audit cares about and
// returns one line per probe. proxied is the client for requests that
// follow the proxy setting (nil = none).
func (e *e2eEnv) generateTraffic(t *testing.T, proxied *http.Client) []string {
	t.Helper()
	var lines []string
	add := func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) }
	direct := netx.Direct(8 * time.Second)
	direct.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, u := range []string{"http://1.1.1.1/", "http://223.5.5.5/", "http://captive.apple.com/hotspot-detect.html", "http://www.gstatic.com/generate_204", "https://www.baidu.com/"} {
		direct.CloseIdleConnections()
		resp, err := direct.Get(u)
		if err != nil {
			add("direct GET %s: %v", u, err)
			continue
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		add("direct GET %s: %d", u, resp.StatusCode)
	}
	if proxied != nil {
		for _, u := range []string{e.echoURL(false), "http://www.gstatic.com/generate_204"} {
			proxied.CloseIdleConnections()
			resp, err := proxied.Get(u)
			if err != nil {
				add("proxied GET %s: %v", u, err)
				continue
			}
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			add("proxied GET %s: %d", u, resp.StatusCode)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if addrs, err := net.DefaultResolver.LookupHost(ctx, "www.cloudflare.com"); err != nil {
		add("system resolver www.cloudflare.com: %v", err)
	} else {
		add("system resolver www.cloudflare.com: %v", addrs)
	}
	// A raw UDP DNS query to a public resolver, as an app with its own
	// resolver would send it.
	if ans, err := rawDNSQuery("8.8.8.8:53", "www.example.com", 5*time.Second); err != nil {
		add("raw UDP DNS to 8.8.8.8: %v", err)
	} else {
		add("raw UDP DNS to 8.8.8.8: answer %v", ans)
	}
	sctx, scancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer scancel()
	if mapped, err := netx.STUNPublicIP(sctx, nil, nil); err != nil {
		add("STUN: %v", err)
	} else {
		add("STUN mapped address: %s", mapped)
	}
	if c, err := net.DialTimeout("tcp6", "[2606:4700:4700::1111]:80", 4*time.Second); err != nil {
		add("IPv6 TCP to 2606:4700:4700::1111: %v", err)
	} else {
		c.Close()
		add("IPv6 TCP to 2606:4700:4700::1111: connected from %s", c.LocalAddr())
	}
	return lines
}

// rawDNSQuery sends one A query over UDP and returns the answer's IPs.
func rawDNSQuery(server, name string, timeout time.Duration) ([]string, error) {
	msg := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0, 0, 1, 0, 1)
	c, err := net.DialTimeout("udp", server, timeout)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(timeout))
	if _, err := c.Write(msg); err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	if err != nil {
		return nil, err
	}
	resp := buf[:n]
	if len(resp) < 12 || resp[0] != 0x12 || resp[1] != 0x34 {
		return nil, fmt.Errorf("bad response (%d bytes)", n)
	}
	// Skip the question, then read A records.
	off := 12
	for off < len(resp) && resp[off] != 0 {
		if resp[off]&0xC0 == 0xC0 {
			off++
			break
		}
		off += int(resp[off]) + 1
	}
	off += 5
	var ips []string
	for i := 0; i < int(binary.BigEndian.Uint16(resp[6:8])) && off+12 <= len(resp); i++ {
		if resp[off]&0xC0 == 0xC0 {
			off += 2
		} else {
			for off < len(resp) && resp[off] != 0 {
				off += int(resp[off]) + 1
			}
			off++
		}
		if off+10 > len(resp) {
			break
		}
		typ := binary.BigEndian.Uint16(resp[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(resp[off+8 : off+10]))
		off += 10
		if off+rdlen > len(resp) {
			break
		}
		if typ == 1 && rdlen == 4 {
			ips = append(ips, net.IP(resp[off:off+4]).String())
		}
		off += rdlen
	}
	return ips, nil
}

func leakItems(s *Service) string {
	var parts []string
	for _, it := range s.LeakCheck().Items {
		parts = append(parts, fmt.Sprintf("%s=%s(%s)", it.Name, it.Status, it.Summary))
	}
	return strings.Join(parts, "; ")
}

func leakStatus(s *Service, name string) string {
	for _, it := range s.LeakCheck().Items {
		if it.Name == name {
			return it.Status
		}
	}
	return ""
}

// TestLiveLeaks captures the VM's interface while traffic is generated and
// checks what leaves the machine: under TUN + global mode only the fleet
// and the core's DNS upstreams may appear; in system proxy mode the
// expected bypasses are documented and compared with Lanflow's own leak
// check; IPv6 never leaves when it is off.
func TestLiveLeaks(t *testing.T) {
	e := e2eSetup(t)
	if !e.tun {
		t.Skip("TUN helper not installed (LANFLOW_E2E_TUN=1): the leak audit needs TUN")
	}
	s, set := e.newService(t)
	defer s.Shutdown()
	coreDNS, otherUp := runtimeUpstreams(s)
	e2eReport(t, "leak/core-upstreams", "INFO", fmt.Sprintf("core DNS upstreams %v (non-IP: %v); system DNS during tests %v", coreDNS, otherUp, e.sysDNS))

	// (a) TUN + global.
	e.selectNode(t, s, "ss")
	e.tunUp(t, s)
	if err := s.SetMode(model.ModeGlobal); err != nil {
		t.Fatal(err)
	}
	// Warm up: the first request after TUN start may be refused while the
	// core finishes loading.
	fetchEchoRetry(netx.Direct(10*time.Second), e.echoURL(true), 15*time.Second)
	cap := startCapture(t, e.iface, filepath.Join(e.out, "tun-global.pcap"))
	probes := e.generateTraffic(t, nil)
	if err := s.RunLeakCheckWith(""); err != nil {
		t.Logf("leak check: %v", err)
	}
	text := cap.stop(t)
	tl := e.tallyDump(text, coreDNS)
	for _, l := range probes {
		t.Logf("  probe: %s", l)
	}
	bad := map[string]int{}
	for _, c := range []string{"app-dns", "echo-direct", "gateway-other", "stun", "fakeip-direct", "other", "v6-egress"} {
		if tl.out[c] > 0 {
			bad[c] = tl.out[c]
		}
	}
	var badDetail []string
	for c := range bad {
		badDetail = append(badDetail, c+": "+tl.peers(c, 6))
	}
	slices.Sort(badDetail)
	e2eCheck(t, "leak/tun-global/no-bypass", len(bad) == 0, fmt.Sprintf("%s; leaked: %s", tl, strings.Join(badDetail, " | ")))
	e2eCheck(t, "leak/tun-global/no-app-dns", tl.out["app-dns"] == 0, fmt.Sprintf("DNS leaving en0: core-dns=%d (%s) app-dns=%d (%s)", tl.out["core-dns"], tl.peers("core-dns", 4), tl.out["app-dns"], tl.peers("app-dns", 6)))
	e.v6Check(t, "leak/ipv6/no-egress-tun", tl, probes, true)
	e2eReport(t, "leak/tun-global/probes", "INFO", strings.Join(probes, " | "))
	e2eReport(t, "leak/tun-global/lanflow-leakcheck", "INFO", leakItems(s))
	e2eCheck(t, "leak/tun-global/leakcheck-dns-ok", leakStatus(s, "DNS") == model.LeakOK, "Lanflow's DNS item under TUN: "+leakStatus(s, "DNS"))
	e2eCheck(t, "leak/tun-global/leakcheck-webrtc-ok", leakStatus(s, model.LeakItemWebRTC) == model.LeakOK, "Lanflow's WebRTC item under TUN: "+leakStatus(s, model.LeakItemWebRTC))

	if err := s.SetMode(model.ModeRule); err != nil {
		t.Fatal(err)
	}
	e.tunDown(t, s)

	// (b) System proxy mode: what bypasses, and does the leak check say so.
	if err := s.SetSystemProxy(true); err != nil {
		t.Fatalf("SetSystemProxy(true): %v", err)
	}
	cap = startCapture(t, e.iface, filepath.Join(e.out, "sysproxy.pcap"))
	probes = e.generateTraffic(t, netx.ViaPort(set.MixedPort, 10*time.Second))
	if err := s.RunLeakCheckWith(""); err != nil {
		t.Logf("leak check: %v", err)
	}
	text = cap.stop(t)
	tl2 := e.tallyDump(text, coreDNS)
	stunBypass := tl2.out["stun"] > 0
	dnsBypass := tl2.out["app-dns"] > 0
	// Besides the system DNS, two direct queries are the audit's own: the
	// raw probe to 8.8.8.8 and Lanflow's leak check asking leakPublicDNS.
	expectedDNS := append(slices.Clone(e.sysDNS), "8.8.8.8", strings.TrimSuffix(leakPublicDNS, ":53"))
	var unexpectedDNS []string
	for peer := range tl2.outPeers["app-dns"] {
		h, _, _ := net.SplitHostPort(strings.SplitN(peer, "/", 2)[0])
		if !slices.Contains(expectedDNS, h) {
			unexpectedDNS = append(unexpectedDNS, peer)
		}
	}
	e2eReport(t, "leak/sysproxy/bypass-inventory", "INFO", fmt.Sprintf("%s; direct app traffic: other=%s; app-dns=%s; stun=%s; fake-ip(host)=%s", tl2, tl2.peers("other", 8), tl2.peers("app-dns", 4), tl2.peers("stun", 3), tl2.peers("fakeip-direct", 4)))
	e2eCheck(t, "leak/sysproxy/app-dns-goes-to-system-dns-only", len(unexpectedDNS) == 0, fmt.Sprintf("app DNS destinations %s; unexpected %v (expected %v)", tl2.peers("app-dns", 6), unexpectedDNS, expectedDNS))
	e2eReport(t, "leak/sysproxy/probes", "INFO", strings.Join(probes, " | "))
	e2eReport(t, "leak/sysproxy/lanflow-leakcheck", "INFO", leakItems(s))
	rtc := leakStatus(s, model.LeakItemWebRTC)
	stunLine := pick(probes, "STUN")
	stunAnswered := strings.Contains(stunLine, "mapped")
	mapped := strings.TrimSpace(strings.TrimPrefix(stunLine, "STUN mapped address:"))
	rtcEv := fmt.Sprintf("capture: STUN packets bypassing=%v answered=%v; leak check WebRTC=%s (%s)", stunBypass, stunAnswered, rtc, leakSummary(s, model.LeakItemWebRTC))
	if stunBypass && stunAnswered && rtc == model.LeakOK && mapped != "" && strings.Contains(leakSummary(s, model.LeakItemWebRTC), mapped) {
		// The fleet leaves through the same network as the VM, so "proxy
		// exit" and "real address" are one and the same: undecidable here.
		e2eReport(t, "leak/sysproxy/leakcheck-webrtc-matches-capture", "INFO", "inconclusive in this environment (fleet egress == VM egress "+mapped+"): "+rtcEv)
	} else {
		e2eCheck(t, "leak/sysproxy/leakcheck-webrtc-matches-capture", (stunBypass && stunAnswered && rtc == model.LeakWarn) || (!stunAnswered && rtc == model.LeakOK), rtcEv)
	}
	dnsItem := leakStatus(s, "DNS")
	e2eCheck(t, "leak/sysproxy/leakcheck-dns-matches-capture", dnsBypass && dnsItem != model.LeakOK, fmt.Sprintf("capture: app DNS bypassing=%v; leak check DNS=%s (%s)", dnsBypass, dnsItem, leakSummary(s, "DNS")))
	e.v6Check(t, "leak/ipv6/no-egress-sysproxy", tl2, probes, false)
	e2eCheck(t, "leak/ipv6/leakcheck-item", leakStatus(s, "IPv6") == model.LeakOK, fmt.Sprintf("leak check IPv6=%s (%s)", leakStatus(s, "IPv6"), leakSummary(s, "IPv6")))
	if err := s.SetSystemProxy(false); err != nil {
		t.Fatal(err)
	}
	if err := s.StopCore(); err != nil {
		t.Fatal(err)
	}
}

// v6Check: nothing IPv6 may leave when the guest's IPv6 is off, nor under
// TUN (the TUN takes ::/0 as well, profile.TUNInet6Address); in system
// proxy mode with IPv6 on, a direct IPv6 connection is the expected bypass
// and is reported.
func (e *e2eEnv) v6Check(t *testing.T, item string, tl tally, probes []string, tun bool) {
	t.Helper()
	ev := fmt.Sprintf("v6-egress=%d (%s) v6-local(in+out)=%d; probe: %s", tl.out["v6-egress"], tl.peers("v6-egress", 3), tl.in["v6-local"]+tl.out["v6-local"], pick(probes, "IPv6"))
	switch {
	case e.v6Off:
		e2eCheck(t, item, tl.out["v6-egress"] == 0, "guest IPv6 off: "+ev)
	case tun:
		e2eCheck(t, item, tl.out["v6-egress"] == 0, "guest IPv6 on, TUN on: "+ev)
	case tl.out["v6-egress"] == 0:
		e2eReport(t, item, "PASS", "guest IPv6 on, system proxy only: "+ev)
	default:
		e2eReport(t, item, "INFO", "guest IPv6 on, system proxy only: IPv6 left en0 directly (the expected bypass of system-proxy mode): "+ev)
	}
}

// TestLiveLeaksIPv6 runs with the guest's IPv6 on (scripts/vm-e2e.sh turns
// it on for this test only; the virtual network hands out a ULA with
// NAT66, through which a v4-only TUN let IPv6 slip). With TUN on nothing
// IPv6 may leave en0, in global mode (rules skipped: an IPv6 literal goes
// to the proxy), in rule mode with Lanflow's IPv6 off (the core refuses
// the literal) and in rule mode with IPv6 on (MATCH sends it to the
// fleet). The TUN must hold the IPv6 routes, the resolver must answer no
// AAAA while IPv6 is off, and Lanflow's own IPv6 item must be OK.
func TestLiveLeaksIPv6(t *testing.T) {
	e := e2eSetup(t)
	if !e.tun {
		t.Skip("TUN helper not installed (LANFLOW_E2E_TUN=1): the IPv6 scenario needs TUN")
	}
	if e.v6Off {
		t.Skip("the guest's IPv6 is off (LANFLOW_E2E_V6OFF=1)")
	}
	addr := guestV6Addr(e.iface)
	before := netinfo.IPv6Route(context.Background())
	if addr == "" || before.Interface == "" {
		e2eReport(t, "leak/ipv6-on/guest", "INFO", fmt.Sprintf("no usable IPv6 on %s (addr=%q route=%+v): scenario skipped", e.iface, addr, before))
		t.Skip("guest has no IPv6")
	}
	e2eReport(t, "leak/ipv6-on/guest", "INFO", fmt.Sprintf("%s inet6 %s; IPv6 default route via %s; TUN routes before: %v", e.iface, addr, before.Interface, before.ViaTUN))
	s, _ := e.newService(t)
	defer s.Shutdown()
	coreDNS, _ := runtimeUpstreams(s)
	e.selectNode(t, s, "ss")
	tunIf := e.tunUp(t, s)
	r := netinfo.IPv6Route(context.Background())
	e2eCheck(t, "leak/ipv6-on/tun-holds-v6-routes", r.ViaTUN && r.Interface == before.Interface, fmt.Sprintf("IPv6Route=%+v, TUN %s; inet6 routes: %s", r, tunIf, v6RouteTable()))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip6", "www.cloudflare.com")
	cancel()
	e2eCheck(t, "leak/ipv6-on/no-aaaa-while-ipv6-off", len(ips) == 0, fmt.Sprintf("AAAA www.cloudflare.com via the system resolver (TUN-hijacked): %v err=%v", ips, err))

	for _, sc := range []struct {
		name string
		mode model.Mode
		ipv6 bool
	}{{"global-ipv6-off", model.ModeGlobal, false}, {"rule-ipv6-off", model.ModeRule, false}, {"rule-ipv6-on", model.ModeRule, true}} {
		e.setIPv6(t, s, sc.ipv6)
		if err := s.SetMode(sc.mode); err != nil {
			t.Fatal(err)
		}
		fetchEchoRetry(netx.Direct(10*time.Second), e.echoURL(true), 15*time.Second)
		s.ClearLogs()
		cap := startCapture(t, e.iface, filepath.Join(e.out, "ipv6-"+sc.name+".pcap"))
		probes := e.v6Probes()
		if err := s.RunLeakCheckWith(""); err != nil {
			t.Logf("leak check: %v", err)
		}
		text := cap.stop(t)
		tl := e.tallyDump(text, coreDNS)
		for _, l := range probes {
			t.Logf("  %s probe: %s", sc.name, l)
		}
		p := "leak/ipv6-on/" + sc.name + "/"
		e2eCheck(t, p+"no-v6-egress", tl.out["v6-egress"] == 0, fmt.Sprintf("%s; v6-egress peers: %s; v6-local(in+out)=%d", tl, tl.peers("v6-egress", 4), tl.in["v6-local"]+tl.out["v6-local"]))
		lit := pick(probes, "IPv6 TCP to [2606")
		entered := strings.Contains(lit, "from [fdfe:dcba:9876::")
		matched := coreLogsAbout(s, "2606:4700:4700::1111")
		switch sc.name {
		case "rule-ipv6-off":
			e2eCheck(t, p+"literal-refused", entered && strings.Contains(matched, "REJECT") && !strings.Contains(lit, "HTTP/"), fmt.Sprintf("the literal must enter the TUN and be refused by the core: probe %q; core log: %s", lit, matched))
		default:
			// Global mode logs "using GLOBAL" (the group that now points at
			// the fleet), rule mode "using 节点选择[ss]".
			viaProxy := strings.Contains(matched, "[ss]") || strings.Contains(matched, "using GLOBAL")
			e2eCheck(t, p+"literal-via-fleet", entered && viaProxy && !strings.Contains(matched, "REJECT") && tl.out["fleet"] > 0, fmt.Sprintf("the literal must enter the TUN (source = the TUN's inet6 address) and go to the fleet (fleet packets=%d): probe %q; core log: %s", tl.out["fleet"], lit, matched))
		}
		e2eReport(t, p+"probes", "INFO", strings.Join(probes, " | "))
		e2eCheck(t, p+"leakcheck-ipv6-item", leakStatus(s, "IPv6") == model.LeakOK, fmt.Sprintf("Lanflow's IPv6 item: %s (%s)", leakStatus(s, "IPv6"), leakSummary(s, "IPv6")))
	}
	e.setIPv6(t, s, false)
	if err := s.SetMode(model.ModeRule); err != nil {
		t.Fatal(err)
	}
	e.tunDown(t, s)
	after := netinfo.IPv6Route(context.Background())
	e2eCheck(t, "leak/ipv6-on/tun-off-restores-v6-routes", !after.ViaTUN && after.Interface == before.Interface, fmt.Sprintf("IPv6Route after TUN off=%+v (before %+v)", after, before))
	if err := s.StopCore(); err != nil {
		t.Fatal(err)
	}
}

// guestV6Addr is the first non-link-local IPv6 address of iface (a ULA or
// a global address), "" when none.
func guestV6Addr(iface string) string {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return ""
	}
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() == nil && !ipn.IP.IsLinkLocalUnicast() {
			return ipn.IP.String()
		}
	}
	return ""
}

// v6RouteTable is the default / split-default rows of `netstat -rn -f
// inet6` as one line.
func v6RouteTable() string {
	out, _ := exec.Command("/usr/sbin/netstat", "-rn", "-f", "inet6").Output()
	var keep []string
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) >= 4 && (f[0] == "default" || strings.HasSuffix(f[0], "::/1") || strings.HasSuffix(f[0], "::/3") || strings.HasPrefix(f[3], "utun")) && !strings.Contains(f[2], "I") {
			keep = append(keep, f[0]+">"+f[1]+"@"+f[3])
		}
	}
	return strings.Join(keep, " ")
}

// setIPv6 flips Lanflow's IPv6 setting and waits for the reload (the
// reject rule appears in the runtime config when it is off).
func (e *e2eEnv) setIPv6(t *testing.T, s *Service, on bool) {
	t.Helper()
	set := s.Settings()
	if set.Advanced.IPv6 == on {
		return
	}
	set.Advanced.IPv6 = on
	if err := s.SaveSettings(set); err != nil {
		t.Fatalf("IPv6=%v: %v", on, err)
	}
	e2eWait(t, 20*time.Second, fmt.Sprintf("runtime config for IPv6=%v", on), func() bool {
		s.mu.Lock()
		rt := string(s.state.runtimeYAML)
		s.mu.Unlock()
		return strings.Contains(rt, "IP-CIDR6,::/0,REJECT") != on && s.Status().Core == model.CoreRunning
	})
	time.Sleep(1500 * time.Millisecond)
}

// v6Probes makes the IPv6 traffic an app would: TCP to two IPv6 literals
// (an HTTP request is sent so a refused connection shows as closed without
// a response) and an HTTPS trace forced over IPv6, as Lanflow's own check
// does.
func (e *e2eEnv) v6Probes() []string {
	var lines []string
	for _, target := range []string{"[2606:4700:4700::1111]:80", "[2400:3200::1]:80"} {
		c, err := net.DialTimeout("tcp6", target, 6*time.Second)
		if err != nil {
			lines = append(lines, fmt.Sprintf("IPv6 TCP to %s: %v", target, err))
			continue
		}
		c.SetDeadline(time.Now().Add(6 * time.Second))
		fmt.Fprintf(c, "GET / HTTP/1.0\r\nHost: %s\r\n\r\n", strings.Trim(strings.SplitN(target, "]", 2)[0], "["))
		buf := make([]byte, 256)
		n, rerr := c.Read(buf)
		res := fmt.Sprintf("closed (%v)", rerr)
		if n > 0 {
			res = strings.SplitN(string(buf[:n]), "\r\n", 2)[0]
		}
		lines = append(lines, fmt.Sprintf("IPv6 TCP to %s: connected from %s, %s", target, c.LocalAddr(), res))
		c.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if ip, err := fetchTraceIP(ctx, directIPv6Client(8*time.Second), locate.TraceURL); err != nil {
		lines = append(lines, fmt.Sprintf("IPv6-only trace: %v", err))
	} else {
		lines = append(lines, fmt.Sprintf("IPv6-only trace: ip=%s", ip))
	}
	return lines
}

// coreLogsAbout returns the core log lines mentioning s (the match lines
// say which rule and proxy handled a connection).
func coreLogsAbout(svc *Service, s string) string {
	var out []string
	for _, l := range svc.Logs() {
		if line := fmt.Sprintf("%v", l); strings.Contains(line, s) {
			out = append(out, line)
		}
	}
	if len(out) > 4 {
		out = out[len(out)-4:]
	}
	if len(out) == 0 {
		return "(no core log line about " + s + ")"
	}
	return strings.Join(out, " || ")
}

func leakSummary(s *Service, name string) string {
	for _, it := range s.LeakCheck().Items {
		if it.Name == name {
			return it.Summary
		}
	}
	return ""
}

func pick(lines []string, prefix string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

// ---- robustness ----

func corePIDs(path string) []int {
	out, _ := exec.Command("/usr/bin/pgrep", "-f", path).Output()
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(f); err == nil && n != os.Getpid() {
			pids = append(pids, n)
		}
	}
	return pids
}

// askRoot performs a privileged action (kill the helper-run core, bounce
// the network service): directly when running as root, else through an
// ask-<action> marker that scripts/vm-e2e.sh answers as root.
func (e *e2eEnv) askRoot(t *testing.T, action string, d time.Duration) bool {
	t.Helper()
	if e.asRoot {
		switch action {
		case "kill-tun-core":
			out, err := exec.Command("/usr/bin/pkill", "-9", "-x", "com.lanflow.mihomo").CombinedOutput()
			t.Logf("pkill -9 com.lanflow.mihomo: %v %s", err, out)
			return err == nil
		case "net-toggle":
			if e.svc == "" {
				return false
			}
			exec.Command("/usr/sbin/networksetup", "-setnetworkserviceenabled", e.svc, "off").Run()
			time.Sleep(3 * time.Second)
			err := exec.Command("/usr/sbin/networksetup", "-setnetworkserviceenabled", e.svc, "on").Run()
			return err == nil
		}
		return false
	}
	if !e.ask {
		return false
	}
	os.Remove(filepath.Join(e.root, "done-"+action))
	if err := os.WriteFile(filepath.Join(e.root, "ask-"+action), []byte(time.Now().String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return e2eWait2(d, func() bool { _, err := os.Stat(filepath.Join(e.root, "done-"+action)); return err == nil })
}

// TestLiveRobustness: the core is killed (-9) and the service recovers,
// restoring the system proxy; a second crash within a minute is not
// restarted and the proxy is released; the helper-run (TUN) core is killed
// as root; the network service is bounced under TUN.
func TestLiveRobustness(t *testing.T) {
	e := e2eSetup(t)
	s, set := e.newService(t)
	defer s.Shutdown()
	via := func() *http.Client { return netx.ViaPort(set.MixedPort, 15*time.Second) }
	e.selectNode(t, s, "ss")
	before, _ := sysproxy.Get()
	if err := s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	pids := corePIDs(set.CorePath)
	if len(pids) != 1 {
		t.Fatalf("expected one user-run core, found %v", pids)
	}
	st0 := s.Status().StartedAt
	t0 := time.Now()
	syscall.Kill(pids[0], syscall.SIGKILL)
	recovered := e2eWait2(30*time.Second, func() bool {
		st := s.Status()
		return st.Core == model.CoreRunning && st.StartedAt.After(st0)
	})
	recoverDur := time.Since(t0)
	var sp sysproxy.State
	e2eWait2(10*time.Second, func() bool { sp, _ = sysproxy.Get(); return sp.Enabled && sp.Port == set.MixedPort })
	r, tries, err := fetchEchoRetry(via(), e.echoURL(false), 15*time.Second)
	newPids := corePIDs(set.CorePath)
	e2eCheck(t, "robust/kill9-user-core-auto-restart", recovered && err == nil && r.IP == e.host && sp.Enabled && sp.Port == set.MixedPort && len(newPids) == 1 && newPids[0] != pids[0],
		fmt.Sprintf("pid %d killed; running again after %v (pid %v); echo via fleet after %d try(s) err=%v; system proxy %+v", pids[0], recoverDur.Round(time.Millisecond), newPids, tries, err, sp))

	// Second crash inside autoRestartGap: no restart, proxy released.
	if len(newPids) == 1 {
		syscall.Kill(newPids[0], syscall.SIGKILL)
		stopped := e2eWait2(15*time.Second, func() bool { return s.Status().Core == model.CoreError })
		time.Sleep(time.Second)
		sp2, _ := sysproxy.Get()
		e2eCheck(t, "robust/second-crash-stays-down-proxy-released", stopped && s.Status().Core == model.CoreError && sp2.Enabled == before.Enabled && sp2.Port == before.Port,
			fmt.Sprintf("core state %v (%s); system proxy after %+v (before tests %+v)", s.Status().Core, s.Status().CoreError, sp2, before))
	}
	if err := s.StartCore(); err != nil {
		t.Fatalf("StartCore after crash: %v", err)
	}
	e2eWait(t, 30*time.Second, "core back", func() bool { return s.Status().Core == model.CoreRunning })
	sp3, _ := sysproxy.Get()
	if sp3.Enabled {
		s.SetSystemProxy(false)
	}

	// Wake / network-change handling: the service rechecks and stays sane.
	s.NetworkChanged()
	s.Resumed()
	time.Sleep(3 * time.Second)
	r, _, err = fetchEchoRetry(via(), e.echoURL(false), 10*time.Second)
	e2eCheck(t, "robust/recheck-after-network-change-signal", err == nil && r.IP == e.host && s.Status().Core == model.CoreRunning, fmt.Sprintf("after NetworkChanged+Resumed: core %v, echo saw %s err=%v", s.Status().Core, r.Addr, err))

	if e.tun && (e.ask || e.asRoot) {
		e.tunUp(t, s)
		fetchEchoRetry(netx.Direct(10*time.Second), e.echoURL(true), 15*time.Second)
		// The crashes above used up the one automatic restart per minute;
		// this is a new scenario.
		s.mu.Lock()
		s.state.lastAutoStart = time.Time{}
		s.mu.Unlock()
		st0 := s.Status().StartedAt
		t0 := time.Now()
		if !e.askRoot(t, "kill-tun-core", 30*time.Second) {
			e2eReport(t, "robust/kill9-root-core-tun", "INFO", "the script did not answer the kill request; skipped")
		} else {
			recovered := e2eWait2(45*time.Second, func() bool {
				st := s.Status()
				_, tun := netinfo.OtherTUN(context.Background())
				return st.Core == model.CoreRunning && st.TUN && tun && st.StartedAt.After(st0)
			})
			dur := time.Since(t0)
			r, tries, err := fetchEchoRetry(netx.Direct(10*time.Second), e.echoURL(true), 20*time.Second)
			snap, _ := sysdns.Snapshot()
			e2eCheck(t, "robust/kill9-root-core-tun", recovered && err == nil && r.IP == e.host && strings.Contains(string(snap), sysdns.TUNServers[0]),
				fmt.Sprintf("helper-run core killed as root; recovered=%v after %v (core %v tun=%v); echo via fleet after %d try(s) err=%v; system DNS %s", recovered, dur.Round(time.Millisecond), s.Status().Core, s.Status().TUN, tries, err, snap))
		}
		t1 := time.Now()
		if !e.askRoot(t, "net-toggle", 40*time.Second) {
			e2eReport(t, "robust/network-toggle-under-tun", "INFO", "the script did not answer the network toggle; skipped")
		} else {
			r, tries, err := fetchEchoRetry(netx.Direct(10*time.Second), e.echoURL(true), 45*time.Second)
			_, tun := netinfo.OtherTUN(context.Background())
			st := s.Status()
			e2eCheck(t, "robust/network-toggle-under-tun", err == nil && r.IP == e.host && tun && st.Core == model.CoreRunning && st.TUN,
				fmt.Sprintf("network service off/on; echo via fleet %v after %d try(s) err=%v; TUN route present=%v; core %v tun=%v", time.Since(t1).Round(time.Millisecond), tries, err, tun, st.Core, st.TUN))
		}
		e.tunDown(t, s)
	}
	if err := s.StopCore(); err != nil {
		t.Fatal(err)
	}
}

// TestLiveSeedRealApp writes the fleet subscription and test settings into
// the real app's data directory (which the script moved aside first), so
// the real Lanflow.app can be launched against the fleet for the resource
// baseline. Opt-in: LANFLOW_E2E_SEED=1.
func TestLiveSeedRealApp(t *testing.T) {
	e := e2eSetup(t)
	if os.Getenv("LANFLOW_E2E_SEED") != "1" {
		t.Skip("LANFLOW_E2E_SEED=1 seeds the real data directory")
	}
	o := DefaultOptions("Lanflow")
	o.ResourceDir = e.res
	if entries, _ := os.ReadDir(o.DataDir); len(entries) > 0 {
		t.Fatalf("%s is not empty; the script must move the user's data aside first", o.DataDir)
	}
	s, err := NewService(o)
	if err != nil {
		t.Fatal(err)
	}
	set := s.Settings()
	set.MixedPort, set.AutoSystemProxy, set.TUN, set.ProbeExits, set.DisableUpdateCheck, set.Mode = 7890, true, false, false, true, model.ModeRule
	set.CorePath = filepath.Join(e.res, "mihomo")
	// The shell syncs the login item with this setting at start: mirror
	// the user's value so launching the test copy does not change it.
	set.OpenAtLogin = os.Getenv("LANFLOW_E2E_SEED_OPENATLOGIN") == "1"
	set.DNS = e2eDNS()
	if err := s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportSubscriptionFile(e.sub, true); err != nil {
		t.Fatal(err)
	}
	e2eWait(t, 30*time.Second, "core running", func() bool { return s.Status().Core == model.CoreRunning })
	if err := s.SelectProxy(e2eGroup, "ss"); err != nil {
		t.Fatal(err)
	}
	s.Shutdown()
	// TUN for the real app (written after the shutdown so no TUN cycle runs
	// here): its core then runs through the helper, as root, which is what
	// lets it reach the fleet on the local network in this VM.
	if os.Getenv("LANFLOW_E2E_SEED_TUN") == "1" {
		set.TUN = true
		if err := saveSettings(o.DataDir, set); err != nil {
			t.Fatal(err)
		}
	}
	e2eReport(t, "seed/real-app-data", "PASS", fmt.Sprintf("%s seeded with the fleet subscription (port 7890, auto system proxy on, TUN=%v, core %s)", o.DataDir, set.TUN, set.CorePath))
}

// ---- core switching ----

// TestLiveCoreSwitch switches between the bundled core and an installed
// copy (the same binary under <data>/cores/<version>, as InstallCore would
// leave it) while a long-lived connection is open and requests are polled
// every 50 ms, and measures the interruption. Needs the helper NOT
// installed: with a helper, UseCore registers the new binary through an
// administrator prompt nobody can answer in the VM.
func TestLiveCoreSwitch(t *testing.T) {
	e := e2eSetup(t)
	s, set := e.newService(t)
	defer s.Shutdown()
	if s.TUNHelperInstalled() {
		t.Skip("the helper is installed: UseCore would need an administrator prompt")
	}
	e.selectNode(t, s, "ss")
	ipURL := e.echoURL(false)
	ver := strings.TrimSpace(s.Status().CoreVersion)
	if err := checkVersionName(ver); err != nil {
		t.Fatalf("core version %q: %v", ver, err)
	}
	dir := s.coreVersionDir(ver)
	os.MkdirAll(dir, 0o755)
	src, _ := os.ReadFile(set.CorePath)
	if err := os.WriteFile(s.coreVersionPath(ver), src, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(coreMeta{Version: ver, Channel: model.CoreChannelStable, InstalledAt: time.Now(), Source: "e2e-copy"})
	os.WriteFile(filepath.Join(dir, coreMetaFile), meta, 0o644)
	s.cores.mu.Lock()
	s.cores.installed = nil
	s.cores.mu.Unlock()
	if !s.isInstalled(ver) {
		t.Fatalf("installed copy at %s not listed: %+v", dir, s.InstalledCores())
	}

	// switchWith performs fn while polling the echo through the proxy and
	// holding a stream open; it returns the gap during which requests
	// failed and whether the stream survived.
	switchWith := func(fn func() error) (gap time.Duration, failed int, streamCut bool, err error) {
		via := netx.ViaPort(set.MixedPort, 2*time.Second)
		resp, rd, _, serr := openStream(via, ipURL, 20)
		if serr != nil {
			return 0, 0, true, serr
		}
		defer resp.Body.Close()
		stop := make(chan struct{})
		type sample struct {
			at time.Time
			ok bool
		}
		var samples []sample
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				default:
				}
				c := netx.ViaPort(set.MixedPort, 2*time.Second)
				r, perr := fetchEcho(c, ipURL)
				samples = append(samples, sample{time.Now(), perr == nil && r.IP == e.host})
				time.Sleep(50 * time.Millisecond)
			}
		}()
		time.Sleep(500 * time.Millisecond)
		err = fn()
		time.Sleep(3 * time.Second)
		close(stop)
		<-done
		var lastOK time.Time
		for i, smp := range samples {
			if smp.ok {
				if !lastOK.IsZero() && i > 0 && !samples[i-1].ok {
					gap = max(gap, smp.at.Sub(lastOK))
				}
				lastOK = smp.at
			} else {
				failed++
			}
		}
		if !lastOK.IsZero() && !samples[len(samples)-1].ok {
			gap = max(gap, time.Since(lastOK))
		}
		// The stream sends a line every 200 ms: silence for a second means
		// the connection did not survive the switch.
		lineCh := make(chan error, 1)
		go func() { _, rerr := rd.ReadString('\n'); lineCh <- rerr }()
		select {
		case rerr := <-lineCh:
			streamCut = rerr != nil
		case <-time.After(time.Second):
			streamCut = true
		}
		return gap, failed, streamCut, err
	}
	gap, failed, cut, err := switchWith(func() error { return s.UseCore(ver) })
	pids := corePIDs(s.coreVersionPath(ver))
	e2eCheck(t, "core/switch-bundled-to-installed", err == nil && s.Status().Core == model.CoreRunning && s.Settings().CorePath == s.coreVersionPath(ver) && len(pids) == 1 && gap < time.Second,
		fmt.Sprintf("UseCore(%s): err=%v; requests failed during %v (%d failed polls); open stream cut=%v; CorePath=%s; running pid %v", ver, err, gap.Round(time.Millisecond), failed, cut, s.Settings().CorePath, pids))
	gap, failed, cut, err = switchWith(func() error { return s.UseCore("") })
	bundledBin, _ := s.bundledCore()
	pids = corePIDs(bundledBin)
	e2eCheck(t, "core/switch-back-to-bundled", err == nil && s.Status().Core == model.CoreRunning && s.Settings().CorePath == "" && len(pids) == 1 && gap < time.Second,
		fmt.Sprintf("UseCore(bundled): err=%v; requests failed during %v (%d failed polls); open stream cut=%v; CorePath=%q; running %s pid %v", err, gap.Round(time.Millisecond), failed, cut, s.Settings().CorePath, bundledBin, pids))
	if err := s.RemoveCore(ver); err != nil {
		t.Logf("RemoveCore: %v", err)
	}
	if err := s.StopCore(); err != nil {
		t.Fatal(err)
	}
}
