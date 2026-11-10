package profile

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// composedCoexist is the part of a composed config the coexist layer touches.
type composedCoexist struct {
	Rules []string `yaml:"rules"`
	DNS   struct {
		Range      string              `yaml:"fake-ip-range"`
		Filter     []string            `yaml:"fake-ip-filter"`
		Policy     map[string][]string `yaml:"nameserver-policy"`
		Nameserver []string            `yaml:"nameserver"`
		ProxyNS    []string            `yaml:"proxy-server-nameserver"`
	} `yaml:"dns"`
	Sniffer struct {
		Skip []string `yaml:"skip-domain"`
	} `yaml:"sniffer"`
	TUN struct {
		Device  string   `yaml:"device"`
		Exclude []string `yaml:"route-exclude-address"`
		Inet4   []string `yaml:"inet4-address"`
	} `yaml:"tun"`
}

func composeCoexist(t *testing.T, in Input) (composedCoexist, Output) {
	t.Helper()
	out, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	var cfg composedCoexist
	if err := yaml.Unmarshal(out.YAML, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg, out
}

func TestComposeCoexist(t *testing.T) {
	in := Input{
		Subscription: []byte(testSub),
		Managed:      Managed{MixedPort: 7890, TUN: true},
		Coexist:      Coexist{Processes: []string{"aTrustAgent"}, ExcludeCIDRs: []string{"2.0.0.0/24", "198.18.0.0/16"}, CorpDomains: []string{"corp.example.com"}},
	}
	cfg, out := composeCoexist(t, in)
	wantHead := []string{
		"PROCESS-NAME,aTrustAgent,DIRECT",
		"DOMAIN-SUFFIX,corp.example.com,DIRECT",
		"IP-CIDR,2.0.0.0/24,DIRECT,no-resolve",
		"IP-CIDR,198.18.0.0/16,DIRECT,no-resolve",
	}
	if len(cfg.Rules) < len(wantHead) || strings.Join(cfg.Rules[:4], "|") != strings.Join(wantHead, "|") {
		t.Fatalf("rules head = %v", cfg.Rules)
	}
	if cfg.DNS.Range != "198.19.0.1/16" {
		t.Errorf("fake-ip-range = %q, want moved off 198.18/16", cfg.DNS.Range)
	}
	if !hasItem(cfg.DNS.Filter, "+.corp.example.com") {
		t.Errorf("fake-ip-filter = %v", cfg.DNS.Filter)
	}
	if strings.Join(cfg.TUN.Exclude, ",") != "2.0.0.0/24,198.18.0.0/16" {
		t.Errorf("route-exclude-address = %v", cfg.TUN.Exclude)
	}
	if strings.Join(cfg.TUN.Inet4, ",") != "172.19.0.1/30" {
		t.Errorf("inet4-address = %v", cfg.TUN.Inet4)
	}
	if runtime.GOOS == "darwin" && cfg.TUN.Device != TUNDevice {
		t.Errorf("tun.device = %q, want %q", cfg.TUN.Device, TUNDevice)
	}
	// no company DNS, no direct domains: no policy, no UDP fallback
	if len(cfg.DNS.Policy) != 0 || len(cfg.DNS.ProxyNS) != 0 {
		t.Errorf("unexpected DNS policy %v / proxy nameservers %v", cfg.DNS.Policy, cfg.DNS.ProxyNS)
	}
	found := false
	for _, l := range out.Lines {
		if l.Source == layerCoexist && strings.Contains(l.Text, "PROCESS-NAME,aTrustAgent") {
			found = true
		}
	}
	if !found {
		t.Error("coexist rule not attributed to its layer")
	}

	// without TUN: rules and DNS only, TUN section untouched
	in.Managed.TUN = false
	in.Coexist.ExcludeCIDRs = []string{"10.20.0.0/16"}
	out2, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out2.YAML), "route-exclude-address") || strings.Contains(string(out2.YAML), "198.19.0.1") {
		t.Fatalf("unexpected TUN/range change without TUN or overlap:\n%s", out2.YAML)
	}

	// zero value: identical to no coexist
	base, _ := Compose(Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 7890}})
	zero, _ := Compose(Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 7890}, Coexist: Coexist{}})
	if string(base.YAML) != string(zero.YAML) {
		t.Fatal("zero Coexist changed the config")
	}
}

// fullCoexist is a layer with every list filled, as the app builds it from
// detection plus the user's configuration.
var fullCoexist = Coexist{
	Processes:     []string{"aTrustAgent"},
	ExcludeCIDRs:  []string{"198.18.0.0/16", "203.0.113.10/32"},
	DirectCIDRs:   []string{"10.0.0.0/8"},
	CorpDomains:   []string{"corp.example.com"},
	DirectDomains: []string{"chat.example.org"},
	CorpDNS:       []string{"10.1.0.53"},
	PublicDNS:     []string{"223.5.5.5", "119.29.29.29"},
	UDPFallback:   true,
}

func TestComposeCoexistLists(t *testing.T) {
	cfg, _ := composeCoexist(t, Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 7890, TUN: true}, Coexist: fullCoexist})

	// direct ranges go DIRECT but stay in the TUN; excluded ones leave it
	if strings.Join(cfg.TUN.Exclude, ",") != "198.18.0.0/16,203.0.113.10/32" {
		t.Errorf("route-exclude-address = %v", cfg.TUN.Exclude)
	}
	for _, r := range []string{"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve", "IP-CIDR,198.18.0.0/16,DIRECT,no-resolve", "DOMAIN-SUFFIX,chat.example.org,DIRECT", "DOMAIN-SUFFIX,corp.example.com,DIRECT"} {
		if !hasItem(cfg.Rules, r) {
			t.Errorf("rules lack %s: %v", r, cfg.Rules)
		}
	}
	// domain policy: company DNS for intranet names, public DNS for direct ones
	if got := cfg.DNS.Policy["+.corp.example.com"]; strings.Join(got, ",") != "10.1.0.53" {
		t.Errorf("policy for corp = %v", got)
	}
	if got := cfg.DNS.Policy["+.chat.example.org"]; strings.Join(got, ",") != "223.5.5.5,119.29.29.29" {
		t.Errorf("policy for direct = %v", got)
	}
	// both lists: no fake IP, no sniffing
	for _, d := range []string{"+.corp.example.com", "+.chat.example.org"} {
		if !hasItem(cfg.DNS.Filter, d) {
			t.Errorf("fake-ip-filter lacks %s: %v", d, cfg.DNS.Filter)
		}
		if !hasItem(cfg.Sniffer.Skip, d) {
			t.Errorf("sniffer skip-domain lacks %s: %v", d, cfg.Sniffer.Skip)
		}
	}
	// UDP fallback: public DNS appended to the nameservers
	for _, ns := range []string{"223.5.5.5", "119.29.29.29"} {
		if !hasItem(cfg.DNS.Nameserver, ns) || !hasItem(cfg.DNS.ProxyNS, ns) {
			t.Errorf("nameservers lack %s: %v / %v", ns, cfg.DNS.Nameserver, cfg.DNS.ProxyNS)
		}
	}

	// fallback off: the policy stays, the nameservers are untouched
	co := fullCoexist
	co.UDPFallback = false
	cfg, _ = composeCoexist(t, Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 7890, TUN: true}, Coexist: co})
	if hasItem(cfg.DNS.ProxyNS, "223.5.5.5") {
		t.Errorf("proxy-server-nameserver changed without the fallback: %v", cfg.DNS.ProxyNS)
	}
	if got := cfg.DNS.Policy["+.chat.example.org"]; len(got) == 0 {
		t.Errorf("policy for direct domains missing without the fallback")
	}

	// the layer sits on top of Lanflow's own DNS settings as well
	in := Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 7890, TUN: true}, Coexist: fullCoexist}
	in.DNS.TakeOver = true
	in.DNS.Nameserver = []string{"https://doh.pub/dns-query"}
	in.DNS.FakeIPFilter = []string{"*.lan"}
	in.Sniffer.TakeOver, in.Sniffer.Enable, in.Sniffer.TLS = true, true, true
	in.Sniffer.SkipDomain = []string{"Mijia Cloud"}
	cfg, _ = composeCoexist(t, in)
	if !hasItem(cfg.DNS.Nameserver, "https://doh.pub/dns-query") || !hasItem(cfg.DNS.Nameserver, "223.5.5.5") {
		t.Errorf("nameserver = %v", cfg.DNS.Nameserver)
	}
	if !hasItem(cfg.DNS.Filter, "*.lan") || !hasItem(cfg.DNS.Filter, "+.corp.example.com") {
		t.Errorf("fake-ip-filter = %v", cfg.DNS.Filter)
	}
	if !hasItem(cfg.Sniffer.Skip, "Mijia Cloud") || !hasItem(cfg.Sniffer.Skip, "+.chat.example.org") {
		t.Errorf("skip-domain = %v", cfg.Sniffer.Skip)
	}
}

// TestComposeCoexistMihomoCheck runs the full layer through `mihomo -t`
// when the binary is available.
func TestComposeCoexistMihomoCheck(t *testing.T) {
	bin := filepath.Join("..", "..", "bin", "mihomo")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("./bin/mihomo not present")
	}
	in := Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 17890, TUN: true, TUNStack: "mixed"}, Coexist: fullCoexist}
	in.DNS.TakeOver = true
	in.DNS.Nameserver = []string{"https://doh.pub/dns-query"}
	in.Sniffer.TakeOver, in.Sniffer.Enable, in.Sniffer.TLS, in.Sniffer.OverrideDestination = true, true, true, true
	out, err := Compose(in)
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
}

func hasItem(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
