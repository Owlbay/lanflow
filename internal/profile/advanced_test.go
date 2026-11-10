package profile

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lanflow/internal/model"
)

// fullAdvanced sets every 高级 field.
var fullAdvanced = model.AdvancedSettings{
	HTTPPort: 17901, SocksPort: 17902,
	LANAllowedIPs: []string{"192.168.0.0/16", "127.0.0.1/32"}, LANDisallowedIPs: []string{"192.168.0.3/32"},
	Authentication: []string{"user1:pass1"}, SkipAuthPrefixes: []string{"127.0.0.1/32"},
	PACMode: true, PACDirectCN: true, PACPort: 11234,
	TUNStrictRoute: true, TUNMTU: 1500, TUNEndpointIndependentNAT: true,
	TUNRouteAddress: []string{"0.0.0.0/1", "128.0.0.0/1"}, TUNRouteExcludeAddress: []string{"10.0.0.0/8"},
	TUNDNSHijack: []string{"any:53"}, TUNDevice: "utun1800", TUNIncludeInterface: []string{"en0"}, TUNExcludeInterface: []string{"en1"},
	IPv6: true, UnifiedDelay: true, TCPConcurrent: true, FindProcessMode: "strict", KeepAliveInterval: 15, KeepAliveIdle: 600,
	DisableKeepAlive: true, InterfaceName: "en0", GeodataLoader: "memconservative", DisableETag: true, StoreFakeIP: true, GlobalUA: "clash.meta",
	DNSListen: "127.0.0.1:1053", DNSPreferH3: true, DNSCacheAlgorithm: "arc", DNSDefaultNameserver: []string{"223.5.5.5"},
	DNSFallback: []string{"https://1.1.1.1/dns-query"}, DNSFallbackFilter: model.DNSFallbackFilter{GeoIP: true, GeoIPCode: "CN", IPCIDR: []string{"240.0.0.0/4"}, Domain: []string{"+.google.com"}},
	SnifferNoForceDNSMapping: true,
}

func TestComposeAdvancedZeroEmitsNothing(t *testing.T) {
	out, err := Compose(Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 17890, TUN: true}})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	for _, k := range []string{"port", "socks-port", "lan-allowed-ips", "lan-disallowed-ips", "authentication", "skip-auth-prefixes",
		"unified-delay", "tcp-concurrent", "find-process-mode", "keep-alive-interval", "keep-alive-idle", "disable-keep-alive",
		"interface-name", "geodata-loader", "etag-support", "global-ua"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s emitted without being set", k)
		}
	}
	tun := m["tun"].(map[string]any)
	for _, k := range []string{"mtu", "endpoint-independent-nat", "route-address", "route-exclude-address", "include-interface", "exclude-interface"} {
		if _, ok := tun[k]; ok {
			t.Errorf("tun.%s emitted without being set", k)
		}
	}
	if tun["strict-route"] != false || !reflect.DeepEqual(tun["dns-hijack"], []any{"any:53", "tcp://any:53"}) {
		t.Errorf("tun defaults changed: %v", tun)
	}
	if prof := m["profile"].(map[string]any); prof["store-fake-ip"] != nil {
		t.Errorf("profile.store-fake-ip emitted: %v", prof)
	}
	if sn := m["sniffer"].(map[string]any); sn["force-dns-mapping"] != nil {
		t.Errorf("sniffer.force-dns-mapping emitted: %v", sn)
	}
	if d := m["dns"].(map[string]any); d["listen"] != nil || d["fallback"] != nil || d["cache-algorithm"] != nil {
		t.Errorf("dns extras emitted: %v", d)
	}
}

func TestComposeAdvancedFull(t *testing.T) {
	out, err := Compose(Input{
		Subscription: []byte(testSub),
		DNS:          model.DNSSettings{TakeOver: true},
		Sniffer:      model.SnifferSettings{Enable: true, TakeOver: true, TLS: true},
		Managed:      Managed{MixedPort: 17890, TUN: true, AllowLAN: true},
		Advanced:     fullAdvanced,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	want := map[string]any{
		"port": 17901, "socks-port": 17902,
		"lan-allowed-ips": []any{"192.168.0.0/16", "127.0.0.1/32"}, "lan-disallowed-ips": []any{"192.168.0.3/32"},
		"authentication": []any{"user1:pass1"}, "skip-auth-prefixes": []any{"127.0.0.1/32"},
		"unified-delay": true, "tcp-concurrent": true, "find-process-mode": "strict",
		"keep-alive-interval": 15, "keep-alive-idle": 600, "disable-keep-alive": true,
		"interface-name": "en0", "geodata-loader": "memconservative", "etag-support": false, "global-ua": "clash.meta",
	}
	for k, v := range want {
		if !reflect.DeepEqual(m[k], v) {
			t.Errorf("%s = %#v, want %#v", k, m[k], v)
		}
	}
	// PAC fields never reach the core; ipv6 comes through Managed, not here.
	for _, k := range []string{"pacMode", "pac-mode", "pac"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s leaked into the config", k)
		}
	}
	prof := m["profile"].(map[string]any)
	if prof["store-fake-ip"] != true || prof["store-selected"] != true {
		t.Errorf("profile: %v", prof)
	}
	tun := m["tun"].(map[string]any)
	wantTun := map[string]any{
		"strict-route": true, "mtu": 1500, "endpoint-independent-nat": true,
		"route-address": []any{"0.0.0.0/1", "128.0.0.0/1"}, "route-exclude-address": []any{"10.0.0.0/8"},
		"dns-hijack": []any{"any:53"}, "device": "utun1800", "include-interface": []any{"en0"}, "exclude-interface": []any{"en1"},
		"enable": true, "auto-route": true,
	}
	for k, v := range wantTun {
		if !reflect.DeepEqual(tun[k], v) {
			t.Errorf("tun.%s = %#v, want %#v", k, tun[k], v)
		}
	}
	d := m["dns"].(map[string]any)
	wantDNS := map[string]any{
		"listen": "127.0.0.1:1053", "prefer-h3": true, "cache-algorithm": "arc",
		"default-nameserver": []any{"223.5.5.5"}, "fallback": []any{"https://1.1.1.1/dns-query"},
		"fallback-filter": map[string]any{"geoip": true, "geoip-code": "CN", "ipcidr": []any{"240.0.0.0/4"}, "domain": []any{"+.google.com"}},
		"enable":          true,
	}
	for k, v := range wantDNS {
		if !reflect.DeepEqual(d[k], v) {
			t.Errorf("dns.%s = %#v, want %#v", k, d[k], v)
		}
	}
	sn := m["sniffer"].(map[string]any)
	if sn["force-dns-mapping"] != false || sn["enable"] != true {
		t.Errorf("sniffer: %v", sn)
	}
	// Everything is attributed to the Lanflow layer.
	for _, l := range out.Lines {
		for _, k := range []string{"port:", "socks-port:", "authentication:", "unified-delay:", "global-ua:"} {
			if strings.HasPrefix(l.Text, k) && l.Source != layerLanflow {
				t.Errorf("%q attributed to %q", l.Text, l.Source)
			}
		}
	}
}

// TestComposeAdvancedPortsNeverCollide: the plain ports are dropped when
// they equal the mixed port or each other, and the subscription's ports
// stay stripped.
func TestComposeAdvancedPortsNeverCollide(t *testing.T) {
	out, err := Compose(Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 17890}, Advanced: model.AdvancedSettings{HTTPPort: 17890, SocksPort: 17890}})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	if _, ok := m["port"]; ok {
		t.Error("port equal to the mixed port must not be emitted")
	}
	if _, ok := m["socks-port"]; ok {
		t.Error("socks-port equal to the mixed port must not be emitted")
	}
	out, _ = Compose(Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 17890}, Advanced: model.AdvancedSettings{HTTPPort: 17901, SocksPort: 17901}})
	m = decode(t, out.YAML)
	if m["port"] != 17901 {
		t.Errorf("port = %v", m["port"])
	}
	if _, ok := m["socks-port"]; ok {
		t.Error("socks-port equal to port must not be emitted")
	}
	for _, k := range []string{"redir-port", "tproxy-port"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s should stay stripped", k)
		}
	}
}

// TestComposeAdvancedDNSFollowsSubscription: the DNS extras land in the
// subscription's dns section when Lanflow does not take DNS over.
func TestComposeAdvancedDNSFollowsSubscription(t *testing.T) {
	sub := testSub + "dns:\n  enable: true\n  nameserver: [223.5.5.5]\n"
	out, err := Compose(Input{Subscription: []byte(sub), Managed: Managed{MixedPort: 17890}, Advanced: model.AdvancedSettings{DNSListen: "0.0.0.0:53", DNSPreferH3: true}})
	if err != nil {
		t.Fatal(err)
	}
	d := decode(t, out.YAML)["dns"].(map[string]any)
	if d["listen"] != "0.0.0.0:53" || d["prefer-h3"] != true || !reflect.DeepEqual(d["nameserver"], []any{"223.5.5.5"}) {
		t.Errorf("dns: %v", d)
	}
}

// TestComposeAdvancedRouteExcludeMergesWithCoexist: the user's excluded
// ranges and the aTrust layer's end up in one list without duplicates.
func TestComposeAdvancedRouteExcludeMergesWithCoexist(t *testing.T) {
	out, err := Compose(Input{
		Subscription: []byte(testSub),
		Managed:      Managed{MixedPort: 17890, TUN: true},
		Advanced:     model.AdvancedSettings{TUNRouteExcludeAddress: []string{"10.0.0.0/8", "2.0.0.0/24"}},
		Coexist:      Coexist{Processes: []string{"aTrustAgent"}, ExcludeCIDRs: []string{"2.0.0.0/24", "172.30.0.0/16"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tun := decode(t, out.YAML)["tun"].(map[string]any)
	if got := tun["route-exclude-address"]; !reflect.DeepEqual(got, []any{"10.0.0.0/8", "2.0.0.0/24", "172.30.0.0/16"}) {
		t.Errorf("route-exclude-address = %v", got)
	}
}

// TestComposeAdvancedMihomoCheck runs a config with every 高级 field set
// through `mihomo -t`, so a key the core no longer knows is caught.
func TestComposeAdvancedMihomoCheck(t *testing.T) {
	bin := filepath.Join("..", "..", "bin", "mihomo")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("./bin/mihomo not present")
	}
	out, err := Compose(Input{
		Subscription: []byte(testSub),
		DNS:          model.DNSSettings{TakeOver: true},
		Sniffer:      model.SnifferSettings{Enable: true, TakeOver: true, TLS: true, ParsePureIP: true},
		Managed:      Managed{MixedPort: 17890, AllowLAN: true, TUN: true, TUNStack: "mixed", IPv6: true},
		Advanced:     fullAdvanced,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, src := range []string{filepath.Join("..", "..", "bin", "geo"), filepath.Join("..", "..", "resources", "geo")} {
		ents, _ := os.ReadDir(src)
		for _, e := range ents {
			if b, err := os.ReadFile(filepath.Join(src, e.Name())); err == nil {
				_ = os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600)
			}
		}
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, out.YAML, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := exec.Command(bin, "-d", dir, "-f", cfg, "-t").CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo -t failed: %v\n%s\n--- config ---\n%s", err, res, out.YAML)
	}
	// The core logs removed or unknown options at error level; none expected.
	if strings.Contains(string(res), "level=error") {
		t.Fatalf("mihomo -t reported an error:\n%s\n--- config ---\n%s", res, out.YAML)
	}
}
