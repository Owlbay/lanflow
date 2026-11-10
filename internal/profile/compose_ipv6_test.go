package profile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"lanflow/internal/model"
)

// The core's ipv6 switch stays on and the TUN always carries an inet6
// address, so IPv6 enters the TUN whatever Lanflow's IPv6 setting is; that
// setting decides AAAA answers and whether IPv6 destinations are refused.
func TestComposeIPv6Policy(t *testing.T) {
	subWithDNS := testSub + "dns:\n  enable: true\n  ipv6: true\n  nameserver: [1.1.1.1]\n"
	t.Run("off", func(t *testing.T) {
		out, err := Compose(Input{
			Subscription: []byte(subWithDNS),
			CustomRules:  []model.CustomRule{{Type: "DOMAIN", Payload: "custom.example", Target: "DIRECT"}},
			Managed:      Managed{MixedPort: 7890, TUN: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		m := decode(t, out.YAML)
		if m["ipv6"] != true {
			t.Errorf("ipv6 = %v, want true (Mihomo strips the TUN's inet6 address when it is off)", m["ipv6"])
		}
		tun, _ := m["tun"].(map[string]any)
		if a, _ := tun["inet6-address"].([]any); len(a) != 1 || a[0] != TUNInet6Address {
			t.Errorf("tun.inet6-address = %v, want [%s]", tun["inet6-address"], TUNInet6Address)
		}
		dns, _ := m["dns"].(map[string]any)
		if dns["ipv6"] != false || dns["enable"] != true {
			t.Errorf("the subscription's dns section must get ipv6: false, got %v", dns)
		}
		rules, _ := m["rules"].([]any)
		if len(rules) < 3 || rules[0] != "DOMAIN,custom.example,DIRECT" || rules[1] != ipv6RejectRule || rules[2] == ipv6RejectRule {
			t.Errorf("rules must be the custom rules, then the IPv6 reject rule, then the subscription's: %v", rules)
		}
		src := map[string]string{}
		for _, l := range out.Lines {
			if l.Source != "" {
				src[strings.TrimSpace(l.Text)] = l.Source
			}
		}
		if src["- "+ipv6RejectRule] != layerLanflow {
			t.Errorf("reject rule attributed to %q, want %q", src["- "+ipv6RejectRule], layerLanflow)
		}
	})
	t.Run("on", func(t *testing.T) {
		out, err := Compose(Input{Subscription: []byte(subWithDNS), Managed: Managed{MixedPort: 7890, TUN: true, IPv6: true}})
		if err != nil {
			t.Fatal(err)
		}
		m := decode(t, out.YAML)
		dns, _ := m["dns"].(map[string]any)
		if m["ipv6"] != true || dns["ipv6"] != true {
			t.Errorf("ipv6 on: top %v dns %v", m["ipv6"], dns["ipv6"])
		}
		if strings.Contains(string(out.YAML), "IP-CIDR6,::/0") {
			t.Errorf("no reject rule expected with IPv6 on:\n%s", out.YAML)
		}
		tun, _ := m["tun"].(map[string]any)
		if a, _ := tun["inet6-address"].([]any); len(a) != 1 {
			t.Errorf("tun.inet6-address = %v", tun["inet6-address"])
		}
	})
	t.Run("off with taken-over dns and no rules", func(t *testing.T) {
		out, err := Compose(Input{Subscription: []byte("proxies: []\n"), DNS: model.DNSSettings{TakeOver: true}, Managed: Managed{MixedPort: 7890}})
		if err != nil {
			t.Fatal(err)
		}
		m := decode(t, out.YAML)
		dns, _ := m["dns"].(map[string]any)
		rules, _ := m["rules"].([]any)
		if dns["ipv6"] != false || len(rules) != 1 || rules[0] != ipv6RejectRule {
			t.Errorf("dns %v rules %v", dns, rules)
		}
	})
}

// TestComposeIPv6MihomoCheck runs the IPv6-off and IPv6-on TUN configs
// through `mihomo -t`, so the inet6-address key and the IP-CIDR6 rule are
// what the bundled core accepts.
func TestComposeIPv6MihomoCheck(t *testing.T) {
	bin := filepath.Join("..", "..", "bin", "mihomo")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("./bin/mihomo not present")
	}
	for name, ipv6 := range map[string]bool{"tun-ipv6-off": false, "tun-ipv6-on": true} {
		t.Run(name, func(t *testing.T) {
			out, err := Compose(Input{
				Subscription: []byte(testSub),
				DNS:          model.DNSSettings{TakeOver: true},
				Managed:      Managed{MixedPort: 17890, TUN: true, TUNStack: "mixed", IPv6: ipv6},
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
			if strings.Contains(string(res), "level=error") {
				t.Fatalf("mihomo -t reported an error:\n%s\n--- config ---\n%s", res, out.YAML)
			}
		})
	}
}
