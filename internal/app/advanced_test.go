package app

import (
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/model"
	"lanflow/internal/platform/sysproxy"
)

func TestNormalizeAdvancedDropsInvalid(t *testing.T) {
	in := model.AdvancedSettings{
		HTTPPort: 7890, SocksPort: 70000, PACPort: -1,
		LANAllowedIPs:    []string{"192.168.0.0/16", "not-a-cidr", " 10.0.0.1 ", ""},
		Authentication:   []string{"user:pass", "nocolon", ":x", " a:b "},
		SkipAuthPrefixes: []string{"127.0.0.1/32", "bad"},
		TUNMTU:           100, FindProcessMode: "sometimes", GeodataLoader: "fast", DNSCacheAlgorithm: "fifo",
		KeepAliveInterval: -5, DNSListen: "not an address", InterfaceName: " en0 ",
		DNSFallbackFilter: model.DNSFallbackFilter{GeoIPCode: " cn ", IPCIDR: []string{"240.0.0.0/4", "x"}},
	}
	got := normalizeAdvanced(in, 7890)
	want := model.AdvancedSettings{
		LANAllowedIPs:     []string{"192.168.0.0/16", "10.0.0.1"},
		Authentication:    []string{"user:pass", "a:b"},
		SkipAuthPrefixes:  []string{"127.0.0.1/32"},
		InterfaceName:     "en0",
		DNSFallbackFilter: model.DNSFallbackFilter{GeoIPCode: "CN", IPCIDR: []string{"240.0.0.0/4"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalised:\n got %+v\nwant %+v", got, want)
	}
	// Valid values survive; a socks port equal to the HTTP port is dropped.
	ok := model.AdvancedSettings{HTTPPort: 7891, SocksPort: 7891, TUNMTU: 1500, FindProcessMode: "strict", GeodataLoader: "standard", DNSCacheAlgorithm: "arc", DNSListen: "127.0.0.1:1053", KeepAliveIdle: 600}
	got = normalizeAdvanced(ok, 7890)
	ok.SocksPort = 0
	if !reflect.DeepEqual(got, ok) {
		t.Fatalf("valid values changed:\n got %+v\nwant %+v", got, ok)
	}
}

func TestAdvancedPatchAndReload(t *testing.T) {
	old := model.AdvancedSettings{}
	hot := model.AdvancedSettings{HTTPPort: 7891, SocksPort: 7892, TCPConcurrent: true, InterfaceName: "en0", PACMode: true}
	p, changed := advancedPatch(old, hot)
	if !changed || *p.Port != 7891 || *p.SocksPort != 7892 || !*p.TCPConcurrent || *p.InterfaceName != "en0" || p.IPv6 != nil {
		t.Fatalf("patch: %+v", p)
	}
	if advancedNeedsReload(old, hot) {
		t.Fatal("hot fields and PAC must not need a reload")
	}
	// IPv6 is composed (dns.ipv6 + the reject rule), never patched hot.
	if p, changed := advancedPatch(old, model.AdvancedSettings{IPv6: true}); changed || p.IPv6 != nil {
		t.Fatalf("IPv6 must not be patched hot: %+v", p)
	}
	if !advancedNeedsReload(old, model.AdvancedSettings{IPv6: true}) {
		t.Fatal("IPv6 needs a reload")
	}
	if _, changed := advancedPatch(hot, hot); changed {
		t.Fatal("no change, no patch")
	}
	if !advancedNeedsReload(old, model.AdvancedSettings{UnifiedDelay: true}) || !advancedNeedsReload(old, model.AdvancedSettings{TUNMTU: 1500}) {
		t.Fatal("composed fields need a reload")
	}
	if !pacChanged(old, model.AdvancedSettings{PACMode: true}) || pacChanged(old, model.AdvancedSettings{PACDirectCN: true}) || !pacChanged(model.AdvancedSettings{PACMode: true}, model.AdvancedSettings{PACMode: true, PACPort: 12000}) {
		t.Fatal("pacChanged")
	}
}

func TestBuildPAC(t *testing.T) {
	pac := buildPAC(7890, []string{"localhost", "127.0.0.1", "192.168.0.0/16", "*.local", "<local>", "example.com", "::1"}, false)
	for _, want := range []string{
		`host == "localhost" || dnsDomainIs(host, ".localhost")`,
		`host == "127.0.0.1"`,
		`isInNet(host, "192.168.0.0", "255.255.0.0")`,
		`shExpMatch(host, "*.local")`,
		`isPlainHostName(host)`,
		`host == "example.com" || dnsDomainIs(host, ".example.com")`,
		`host == "::1"`,
		`return "PROXY 127.0.0.1:7890; SOCKS5 127.0.0.1:7890; DIRECT";`,
	} {
		if !strings.Contains(pac, want) {
			t.Errorf("PAC lacks %s:\n%s", want, pac)
		}
	}
	if strings.Contains(pac, `".cn"`) {
		t.Error("CN domains must not be direct unless asked")
	}
	cn := buildPAC(7890, nil, true)
	if !strings.Contains(cn, `dnsDomainIs(host, ".cn")`) || !strings.Contains(cn, `".baidu.com"`) {
		t.Errorf("CN direct missing:\n%s", cn)
	}
	// Still a syntactically plausible function with no rules at all.
	if empty := buildPAC(7890, nil, false); !strings.HasPrefix(empty, "// Lanflow PAC") || !strings.Contains(empty, "function FindProxyForURL(url, host) {") || !strings.HasSuffix(empty, "}\n") {
		t.Errorf("empty PAC:\n%s", empty)
	}
}

// TestPACModeSetsAutoProxyURL: with the PAC mode on, turning the system
// proxy on serves a PAC file on 127.0.0.1 and hands its URL to the OS
// instead of a manual proxy; turning it off restores the saved state.
func TestPACModeSetsAutoProxyURL(t *testing.T) {
	h := newHarness(t, nil)
	set := h.s.Settings()
	set.Advanced.PACMode, set.Advanced.PACPort = true, 0 // 0 = model.PACDefaultPort, or any free port
	set.Bypass = []string{"localhost", "*.corp.example"}
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.sys.state = sysproxy.State{PACURL: "http://pac.corp/old.pac"} // someone else's PAC before us
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	st, _ := h.sys.Get()
	if st.Enabled || !strings.HasPrefix(st.PACURL, "http://127.0.0.1:") || !strings.HasSuffix(st.PACURL, pacPath) {
		t.Fatalf("expected a PAC URL on 127.0.0.1 and no manual proxy, got %+v", st)
	}
	if sets, _ := h.sys.counts(); sets != 0 {
		t.Fatal("the manual proxy must not be set in PAC mode")
	}
	if m, ok := h.s.readMarker(); !ok || m.PAC != st.PACURL {
		t.Fatalf("marker: %+v %v", m, ok)
	}
	if !h.s.Status().SystemProxy {
		t.Fatal("status should show the system proxy on")
	}

	// The served file carries the mixed port and the bypass list.
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(st.PACURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ns-proxy-autoconfig" {
		t.Errorf("content type %q", ct)
	}
	pac := string(body)
	port := h.s.Settings().MixedPort
	if !strings.Contains(pac, "PROXY 127.0.0.1:"+strconv.Itoa(port)) || !strings.Contains(pac, `shExpMatch(host, "*.corp.example")`) {
		t.Fatalf("PAC body:\n%s", pac)
	}

	// Changing the direct-CN option re-sets the PAC (so the OS re-fetches it).
	pacSetsBefore := h.sys.pacSets
	set = h.s.Settings()
	set.Advanced.PACDirectCN = true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if h.sys.pacSets != pacSetsBefore+1 {
		t.Fatalf("PAC not re-applied after the CN option changed: %d → %d", pacSetsBefore, h.sys.pacSets)
	}
	resp, _ = client.Get(st.PACURL)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `dnsDomainIs(host, ".cn")`) {
		t.Fatal("CN rules missing after the option was turned on")
	}

	// Off: the previous PAC comes back and the marker goes.
	if err := h.s.SetSystemProxy(false); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.sys.Get(); st.PACURL != "http://pac.corp/old.pac" {
		t.Fatalf("previous PAC not restored: %+v", st)
	}
	if h.s.markerExists() {
		t.Fatal("marker left behind")
	}
}

// TestPACModeSwitchReappliesProxy: switching between the global proxy and
// PAC while the system proxy is ours changes the OS setting at once.
func TestPACModeSwitchReappliesProxy(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	set := h.s.Settings()
	set.Advanced.PACMode = true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	st, _ := h.sys.Get()
	if st.Enabled || st.PACURL == "" {
		t.Fatalf("after switching to PAC: %+v", st)
	}
	set = h.s.Settings()
	set.Advanced.PACMode = false
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	st, _ = h.sys.Get()
	if !st.Enabled || st.Port != set.MixedPort || st.PACURL != "" {
		t.Fatalf("after switching back to the global proxy: %+v", st)
	}
	// Recovery detection knows a PAC marker.
	m := proxyMarker{Port: 7890, PAC: "http://127.0.0.1:11233/proxy.pac"}
	if !proxyIsOurs(sysproxy.State{PACURL: m.PAC}, m, 7890) || proxyIsOurs(sysproxy.State{Enabled: true, Host: "127.0.0.1", Port: 7890}, m, 7890) {
		t.Fatal("proxyIsOurs with a PAC marker")
	}
	if !proxyIsOurs(sysproxy.State{Enabled: true, Host: "127.0.0.1", Port: 7890}, proxyMarker{Port: 7890}, 7890) {
		t.Fatal("proxyIsOurs with a manual marker")
	}
}

// TestAdvancedSettingsHotAndReload: the hot fields go through PATCH
// /configs, the rest reloads the config, and the composed config gets
// the Advanced section.
func TestAdvancedSettingsHotAndReload(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.ctrl.mu.Lock()
	h.ctrl.patches = nil
	reloads := h.ctrl.reloads
	h.ctrl.mu.Unlock()

	set := h.s.Settings()
	set.Advanced.HTTPPort, set.Advanced.TCPConcurrent = 7891, true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.ctrl.mu.Lock()
	patches := append([]controller.ConfigPatch(nil), h.ctrl.patches...)
	r := h.ctrl.reloads
	h.ctrl.mu.Unlock()
	if len(patches) != 1 || patches[0].Port == nil || *patches[0].Port != 7891 || patches[0].TCPConcurrent == nil || !*patches[0].TCPConcurrent {
		t.Fatalf("hot fields should be patched: %+v", patches)
	}
	if r != reloads {
		t.Fatal("hot fields must not reload")
	}

	set = h.s.Settings()
	set.Advanced.UnifiedDelay, set.Advanced.TUNMTU = true, 1500
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.ctrl.mu.Lock()
	r = h.ctrl.reloads
	h.ctrl.mu.Unlock()
	if r != reloads+1 {
		t.Fatalf("composed fields should reload once, reloads %d → %d", reloads, r)
	}
	in, _, err := h.s.buildInput(composeArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if !in.Advanced.UnifiedDelay || in.Advanced.TUNMTU != 1500 || in.Advanced.HTTPPort != 7891 {
		t.Fatalf("compose input lacks the advanced settings: %+v", in.Advanced)
	}

	// A taken HTTP port is refused and the settings stay.
	h.taken[7893] = true
	set = h.s.Settings()
	set.Advanced.HTTPPort = 7893
	if err := h.s.SaveSettings(set); err == nil || !strings.Contains(err.Error(), "7893") {
		t.Fatalf("taken port: %v", err)
	}
	if h.s.Settings().Advanced.HTTPPort != 7891 {
		t.Fatal("refused port must not be stored")
	}
}
