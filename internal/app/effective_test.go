package app

import (
	"testing"

	"lanflow/internal/model"
)

const subWithDNS = `proxies: []
dns:
  enable: true
  listen: 0.0.0.0:1053
  ipv6: false
  enhanced-mode: fake-ip
  nameserver:
    - 223.5.5.5
    - https://doh.pub/dns-query
  proxy-server-nameserver: [https://dns.alidns.com/dns-query]
  nameserver-policy:
    geosite:cn: 223.5.5.5
    "+.example.com": [1.1.1.1, 8.8.8.8]
  fake-ip-filter:
    - "*.lan"
  respect-rules: true
  use-system-hosts: false
hosts:
  router.lan: 192.168.1.1
sniffer:
  enable: true
  parse-pure-ip: true
  sniff:
    TLS:
      ports: [443, 8443]
    http:
      ports: [80]
      override-destination: true
  skip-domain:
    - Mijia Cloud
`

// The parsed sections mirror what the DNS and 嗅探 tabs show.
func TestParseEffectiveSections(t *testing.T) {
	d, sn := parseEffectiveSections([]byte(subWithDNS))
	if d.Source != model.EffectiveSubscription || !d.Enabled || d.Listen != "0.0.0.0:1053" || d.IPv6 {
		t.Fatalf("dns meta = %+v", d)
	}
	ds := d.Settings
	if ds.EnhancedMode != "fake-ip" || len(ds.Nameserver) != 2 || ds.Nameserver[0] != "223.5.5.5" || len(ds.ProxyServerNameserver) != 1 {
		t.Fatalf("dns servers = %+v", ds)
	}
	if len(ds.Policy) != 2 || ds.Policy[0] != (model.KV{Key: "geosite:cn", Value: "223.5.5.5"}) || ds.Policy[1] != (model.KV{Key: "+.example.com", Value: "1.1.1.1, 8.8.8.8"}) {
		t.Fatalf("policy = %+v", ds.Policy)
	}
	if !ds.RespectRules || ds.UseSystemHosts || len(ds.FakeIPFilter) != 1 || len(ds.Hosts) != 1 || ds.Hosts[0].Value != "192.168.1.1" {
		t.Fatalf("dns flags = %+v", ds)
	}
	if sn.Source != model.EffectiveSubscription {
		t.Fatalf("sniffer source = %q", sn.Source)
	}
	ss := sn.Settings
	if !ss.Enable || !ss.ParsePureIP || !ss.TLS || ss.TLSPorts != "443, 8443" || !ss.HTTP || ss.HTTPPorts != "80" || !ss.OverrideDestination || ss.QUIC {
		t.Fatalf("sniffer = %+v", ss)
	}
	if len(ss.SkipDomain) != 1 || ss.SkipDomain[0] != "Mijia Cloud" {
		t.Fatalf("skip-domain = %+v", ss.SkipDomain)
	}

	// No sections: Mihomo's defaults, flagged as such.
	d, sn = parseEffectiveSections([]byte("proxies: []\n"))
	if d.Source != model.EffectiveDefault || d.Enabled || d.Settings.EnhancedMode != "redir-host" || !d.Settings.UseSystemHosts {
		t.Fatalf("default dns = %+v", d)
	}
	if sn.Source != model.EffectiveDefault || sn.Settings.Enable || sn.Settings.TLS {
		t.Fatalf("default sniffer = %+v", sn)
	}
	// Garbage never panics.
	if d, _ := parseEffectiveSections([]byte("dns: [")); d.Source != model.EffectiveDefault {
		t.Fatalf("garbage = %+v", d)
	}
}

// EffectiveDNS follows the subscription (with its overrides applied) in
// 跟随订阅 mode and Lanflow's own settings once DNS is taken over; it
// tracks subscription switches and override saves.
func TestEffectiveDNSFollowsSubscriptionAndOverrides(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		cur, _ := h.store.Current()
		_ = h.store.SetContent(cur.ID, []byte(subWithDNS))
	})
	d := h.s.EffectiveDNS()
	if d.Source != model.EffectiveSubscription || d.Settings.EnhancedMode != "fake-ip" || len(d.Settings.Nameserver) != 2 {
		t.Fatalf("effective = %+v", d)
	}
	sn := h.s.EffectiveSniffer()
	if sn.Source != model.EffectiveSubscription || !sn.Settings.TLS || !sn.Settings.Enable {
		t.Fatalf("effective sniffer = %+v", sn)
	}
	// Lanflow's switch is always its own: turning sniffing off shows off.
	set := h.s.Settings()
	set.Sniffer.Enable = false
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if sn := h.s.EffectiveSniffer(); sn.Settings.Enable || !sn.Settings.TLS {
		t.Fatalf("sniffer after switch off = %+v", sn)
	}

	// A global override replaces the subscription's dns section.
	if err := h.s.SaveOverride("global", "dns:\n  enhanced-mode: redir-host\n  nameserver: [8.8.8.8]\n"); err != nil {
		t.Fatal(err)
	}
	d = h.s.EffectiveDNS()
	if d.Settings.EnhancedMode != "redir-host" || len(d.Settings.Nameserver) != 1 || d.Settings.Nameserver[0] != "8.8.8.8" {
		t.Fatalf("after override = %+v", d.Settings)
	}
	// respect-rules from the subscription survives the merge.
	if !d.Settings.RespectRules {
		t.Fatalf("merge lost respect-rules: %+v", d.Settings)
	}

	// Switching to a subscription without a dns section → defaults.
	other, _ := h.store.Put(model.Subscription{Name: "另一个", Source: "url", URL: "https://example.com/other"})
	_ = h.store.SetContent(other.ID, []byte("proxies: []\n"))
	if err := h.s.SaveOverride("global", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.s.UseSubscription(other.ID); err != nil {
		t.Fatal(err)
	}
	if d := h.s.EffectiveDNS(); d.Source != model.EffectiveDefault {
		t.Fatalf("other subscription = %+v", d)
	}

	// Taking over: Lanflow's own settings, whatever the subscription says.
	set = h.s.Settings()
	set.DNS.TakeOver = true
	set.DNS.Nameserver = []string{"https://1.1.1.1/dns-query"}
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if d := h.s.EffectiveDNS(); d.Source != model.EffectiveLanflow || !d.Enabled || d.Settings.Nameserver[0] != "https://1.1.1.1/dns-query" {
		t.Fatalf("taken over = %+v", d)
	}
}
