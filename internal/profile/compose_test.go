package profile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

const testSub = `port: 7890
socks-port: 7891
redir-port: 7892
tproxy-port: 7893
mixed-port: 7894
allow-lan: true
bind-address: 192.168.1.5
mode: global
log-level: debug
external-controller: 127.0.0.1:9090
external-controller-tls: 127.0.0.1:9443
external-controller-unix: /tmp/x.sock
external-controller-pipe: \\.\pipe\x
external-ui: ui
external-ui-url: https://example.com/ui.zip
external-ui-name: dashboard
secret: topsecret
proxies:
  - {name: 东京 01, type: ss, server: 1.1.1.1, port: 8388, cipher: aes-128-gcm, password: pw}
  - {name: 香港 01, type: ss, server: 1.1.1.2, port: 8388, cipher: aes-128-gcm, password: pw}
proxy-groups:
  - name: 节点选择
    type: select
    proxies: [自动选择, 东京 01, 香港 01, DIRECT]
  - name: 自动选择
    type: url-test
    url: https://www.gstatic.com/generate_204
    interval: 300
    proxies: [东京 01, 香港 01]
rules:
  - DOMAIN-SUFFIX,google.com,节点选择
  - GEOIP,CN,DIRECT
  - MATCH,节点选择
`

// decode parses YAML into a generic map for assertions.
func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("output is not valid YAML: %v\n%s", err, data)
	}
	return m
}

func topKeys(t *testing.T, data []byte) []string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for i := 0; i+1 < len(doc.Content[0].Content); i += 2 {
		keys = append(keys, doc.Content[0].Content[i].Value)
	}
	return keys
}

func TestComposeStripsAndManages(t *testing.T) {
	out, err := Compose(Input{
		Subscription: []byte(testSub),
		Managed:      Managed{MixedPort: 7890, AllowLAN: false, Mode: model.ModeRule, LogLevel: "info", IPv6: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	for _, k := range []string{"port", "socks-port", "redir-port", "tproxy-port", "external-controller",
		"external-controller-tls", "external-controller-unix", "external-controller-pipe",
		"external-ui", "external-ui-url", "external-ui-name", "secret"} {
		if _, ok := m[k]; ok {
			t.Errorf("key %q should be stripped", k)
		}
	}
	if strings.Contains(string(out.YAML), "topsecret") {
		t.Error("secret leaked into output")
	}
	if m["mixed-port"] != 7890 || m["allow-lan"] != false || m["mode"] != "rule" || m["log-level"] != "info" || m["ipv6"] != true {
		t.Errorf("managed fields wrong: %v", m)
	}
	if prof, _ := m["profile"].(map[string]any); prof["store-selected"] != true {
		t.Errorf("profile.store-selected missing: %v", m["profile"])
	}
	tun, _ := m["tun"].(map[string]any)
	if tun["enable"] != false || tun["stack"] != "mixed" || tun["auto-route"] != true || tun["auto-detect-interface"] != true || tun["strict-route"] != false {
		t.Errorf("tun section wrong: %v", tun)
	}
	if hj, _ := tun["dns-hijack"].([]any); len(hj) != 2 || hj[0] != "any:53" || hj[1] != "tcp://any:53" {
		t.Errorf("dns-hijack wrong: %v", tun["dns-hijack"])
	}
	if _, ok := m["dns"]; ok {
		t.Error("dns must not be added when TUN is off and DNS is not taken over")
	}
	if _, ok := m["listeners"]; ok {
		t.Error("no listeners expected without probe port")
	}
	if sn, _ := m["sniffer"].(map[string]any); sn["enable"] != false {
		t.Errorf("sniffer.enable should always be set: %v", m["sniffer"])
	}
	if len(out.Groups) != 2 || out.Groups[0] != "节点选择" || out.Groups[1] != "自动选择" {
		t.Errorf("groups = %v", out.Groups)
	}
	// Key order: subscription keys first, Lanflow keys appended.
	keys := topKeys(t, out.YAML)
	if keys[0] != "mixed-port" || keys[len(keys)-1] != "tun" {
		t.Errorf("unexpected key order: %v", keys)
	}
}

func TestComposeAllowLANBindAddress(t *testing.T) {
	out, err := Compose(Input{Subscription: []byte("proxies: [{name: a, type: ss, server: s, port: 1, cipher: aes-128-gcm, password: p}]\n"),
		Managed: Managed{MixedPort: 1, AllowLAN: true}})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	if m["allow-lan"] != true || m["bind-address"] != "*" {
		t.Errorf("allow-lan/bind-address wrong: %v %v", m["allow-lan"], m["bind-address"])
	}
}

func TestComposeDefaults(t *testing.T) {
	out, err := Compose(Input{Subscription: []byte("proxies: []\n")})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	if m["mixed-port"] != 7890 || m["mode"] != "rule" || m["log-level"] != "info" {
		t.Errorf("defaults wrong: %v", m)
	}
}

func TestComposeLayerOrder(t *testing.T) {
	out, err := Compose(Input{
		Subscription:   []byte(testSub),
		SubOverride:    "log-level: silent\nipv6: false\nrules+:\n  - DOMAIN,sub.example,DIRECT\n",
		GlobalOverride: "+rules:\n  - DOMAIN,global.example,DIRECT\ndns:\n  enable: true\n  nameserver: [1.1.1.1]\n",
		Managed:        Managed{MixedPort: 7890, LogLevel: "warning", IPv6: true}, // IPv6 on: no reject rule in front
	})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	if m["log-level"] != "warning" {
		t.Errorf("managed log-level must win over overrides, got %v", m["log-level"])
	}
	rules, _ := m["rules"].([]any)
	if len(rules) != 5 || rules[0] != "DOMAIN,global.example,DIRECT" || rules[4] != "DOMAIN,sub.example,DIRECT" {
		t.Errorf("rules order wrong: %v", rules)
	}
	// Both overrides contribute; dns comes from global override (not Lanflow).
	dns, _ := m["dns"].(map[string]any)
	if dns["enable"] != true {
		t.Errorf("dns from global override missing: %v", m["dns"])
	}
}

func TestComposeOverrideWinsOverSubscriptionButNotManaged(t *testing.T) {
	out, err := Compose(Input{
		Subscription:   []byte(testSub),
		SubOverride:    "mode: direct\nmixed-port: 1\n",
		GlobalOverride: "mode: global\n",
		Managed:        Managed{MixedPort: 7890, Mode: model.ModeRule},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	if m["mode"] != "rule" || m["mixed-port"] != 7890 {
		t.Errorf("managed fields must be applied last: %v %v", m["mode"], m["mixed-port"])
	}
}

func TestComposeOverrideErrorsNameLayerAndLine(t *testing.T) {
	_, err := Compose(Input{Subscription: []byte(testSub), SubOverride: "a: 1\n+rules: nope\n"})
	if err == nil || !strings.HasPrefix(err.Error(), "订阅覆写第 2 行") {
		t.Fatalf("got %v", err)
	}
	_, err = Compose(Input{Subscription: []byte(testSub), GlobalOverride: "x: [\n"})
	if err == nil || !strings.HasPrefix(err.Error(), "全局覆写第 ") {
		t.Fatalf("got %v", err)
	}
	_, err = Compose(Input{Subscription: []byte("- not a map\n")})
	if err == nil || !strings.HasPrefix(err.Error(), "订阅内容") {
		t.Fatalf("got %v", err)
	}
}

func TestComposeTUNDefaultDNS(t *testing.T) {
	t.Run("added when missing", func(t *testing.T) {
		out, err := Compose(Input{Subscription: []byte(testSub), Managed: Managed{TUN: true, TUNStack: "gvisor"}})
		if err != nil {
			t.Fatal(err)
		}
		m := decode(t, out.YAML)
		dns, _ := m["dns"].(map[string]any)
		if dns["enable"] != true || dns["ipv6"] != false || dns["enhanced-mode"] != "fake-ip" || dns["fake-ip-range"] != "198.18.0.1/16" {
			t.Errorf("default dns wrong: %v", dns)
		}
		ns, _ := dns["nameserver"].([]any)
		if len(ns) != 2 || ns[0] != "https://doh.pub/dns-query" || ns[1] != "https://dns.alidns.com/dns-query" {
			t.Errorf("default nameserver wrong: %v", ns)
		}
		tun, _ := m["tun"].(map[string]any)
		if tun["enable"] != true || tun["stack"] != "gvisor" {
			t.Errorf("tun wrong: %v", tun)
		}
	})
	t.Run("kept when subscription has dns", func(t *testing.T) {
		out, err := Compose(Input{Subscription: []byte(testSub + "dns:\n  enable: true\n  nameserver: [9.9.9.9]\n"), Managed: Managed{TUN: true}})
		if err != nil {
			t.Fatal(err)
		}
		dns, _ := decode(t, out.YAML)["dns"].(map[string]any)
		if ns, _ := dns["nameserver"].([]any); len(ns) != 1 || ns[0] != "9.9.9.9" {
			t.Errorf("subscription dns must be kept: %v", dns)
		}
	})
	t.Run("empty dns section counts as missing", func(t *testing.T) {
		out, err := Compose(Input{Subscription: []byte(testSub + "dns:\n"), Managed: Managed{TUN: true}})
		if err != nil {
			t.Fatal(err)
		}
		dns, _ := decode(t, out.YAML)["dns"].(map[string]any)
		if dns["enable"] != true {
			t.Errorf("default dns expected: %v", dns)
		}
	})
}

func TestComposeDNSTakeOver(t *testing.T) {
	out, err := Compose(Input{
		Subscription: []byte(testSub + "dns:\n  enable: false\n  fallback: [8.8.8.8]\n"),
		DNS: model.DNSSettings{
			TakeOver: true, EnhancedMode: "redir-host",
			Nameserver:            []string{"https://1.1.1.1/dns-query"},
			ProxyServerNameserver: []string{"223.5.5.5"},
			DirectNameserver:      []string{"system"},
			Policy:                []model.KV{{Key: "geosite:cn", Value: "223.5.5.5"}, {Key: "+.example.com", Value: "1.1.1.1"}},
			FakeIPFilter:          []string{"*.lan"},
			RespectRules:          true, UseSystemHosts: true,
			Hosts: []model.KV{{Key: "router.lan", Value: "192.168.1.1"}},
		},
		Managed: Managed{IPv6: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	dns, _ := m["dns"].(map[string]any)
	if dns["enable"] != true || dns["ipv6"] != true || dns["enhanced-mode"] != "redir-host" || dns["respect-rules"] != true || dns["use-system-hosts"] != true || dns["use-hosts"] != true {
		t.Errorf("dns wrong: %v", dns)
	}
	if _, ok := dns["fallback"]; ok {
		t.Error("take-over must replace the dns section wholesale")
	}
	if _, ok := dns["fake-ip-range"]; ok {
		t.Error("fake-ip-range must not be set in redir-host mode")
	}
	if _, ok := dns["fake-ip-filter"]; ok {
		t.Error("fake-ip-filter must not be set in redir-host mode")
	}
	pol, _ := dns["nameserver-policy"].(map[string]any)
	if pol["geosite:cn"] != "223.5.5.5" || pol["+.example.com"] != "1.1.1.1" {
		t.Errorf("policy wrong: %v", pol)
	}
	if ps, _ := dns["proxy-server-nameserver"].([]any); len(ps) != 1 || ps[0] != "223.5.5.5" {
		t.Errorf("proxy-server-nameserver wrong: %v", dns["proxy-server-nameserver"])
	}
	if ds, _ := dns["direct-nameserver"].([]any); len(ds) != 1 || ds[0] != "system" {
		t.Errorf("direct-nameserver wrong: %v", dns["direct-nameserver"])
	}
	hosts, _ := m["hosts"].(map[string]any)
	if hosts["router.lan"] != "192.168.1.1" {
		t.Errorf("hosts wrong: %v", m["hosts"])
	}
	// Policy order preserved in the text.
	if strings.Index(string(out.YAML), "geosite:cn") > strings.Index(string(out.YAML), "+.example.com") {
		t.Error("nameserver-policy order not preserved")
	}
}

func TestComposeSniffer(t *testing.T) {
	t.Run("enable only", func(t *testing.T) {
		out, err := Compose(Input{
			Subscription: []byte(testSub + "sniffer:\n  enable: false\n  force-domain: [+.v2ex.com]\n"),
			Sniffer:      model.SnifferSettings{Enable: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		sn, _ := decode(t, out.YAML)["sniffer"].(map[string]any)
		if sn["enable"] != true {
			t.Errorf("enable not applied: %v", sn)
		}
		if fd, _ := sn["force-domain"].([]any); len(fd) != 1 {
			t.Errorf("subscription sniffer details must be kept: %v", sn)
		}
	})
	t.Run("take over", func(t *testing.T) {
		out, err := Compose(Input{
			Subscription: []byte(testSub + "sniffer:\n  enable: false\n  force-domain: [+.v2ex.com]\n"),
			Sniffer: model.SnifferSettings{
				Enable: true, TakeOver: true, HTTP: true, TLS: true, QUIC: false,
				HTTPPorts: "80, 8080-8880", TLSPorts: "443,8443",
				OverrideDestination: true, ParsePureIP: true,
				ForceDomain: []string{"+.google.com"}, SkipDomain: []string{"Mijia Cloud"}, SkipDstAddress: []string{"192.168.0.0/16"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		sn, _ := decode(t, out.YAML)["sniffer"].(map[string]any)
		if sn["enable"] != true || sn["override-destination"] != true || sn["parse-pure-ip"] != true {
			t.Errorf("sniffer flags wrong: %v", sn)
		}
		sniff, _ := sn["sniff"].(map[string]any)
		httpc, _ := sniff["HTTP"].(map[string]any)
		if ports, _ := httpc["ports"].([]any); len(ports) != 2 || ports[0] != "80" || ports[1] != "8080-8880" {
			t.Errorf("HTTP ports wrong: %v", httpc)
		}
		if _, ok := sniff["TLS"]; !ok {
			t.Error("TLS missing")
		}
		if _, ok := sniff["QUIC"]; ok {
			t.Error("QUIC must be absent when disabled")
		}
		if fd, _ := sn["force-domain"].([]any); len(fd) != 1 || fd[0] != "+.google.com" {
			t.Errorf("force-domain wrong: %v", sn["force-domain"])
		}
		if sd, _ := sn["skip-domain"].([]any); len(sd) != 1 {
			t.Errorf("skip-domain wrong: %v", sn["skip-domain"])
		}
		if sa, _ := sn["skip-dst-address"].([]any); len(sa) != 1 {
			t.Errorf("skip-dst-address wrong: %v", sn["skip-dst-address"])
		}
	})
}

func TestComposeGeo(t *testing.T) {
	out, err := Compose(Input{
		Subscription: []byte(testSub),
		Geo: model.GeoSettings{
			GeoIP: "https://g/geoip.dat", GeoSite: "https://g/geosite.dat", MMDB: "https://g/geoip.metadb", ASN: "https://g/asn.mmdb",
			Mode: "dat", AutoUpdate: true, IntervalHours: 24,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	gx, _ := m["geox-url"].(map[string]any)
	if gx["geoip"] != "https://g/geoip.dat" || gx["geosite"] != "https://g/geosite.dat" || gx["mmdb"] != "https://g/geoip.metadb" || gx["asn"] != "https://g/asn.mmdb" {
		t.Errorf("geox-url wrong: %v", gx)
	}
	if m["geodata-mode"] != true || m["geo-auto-update"] != true || m["geo-update-interval"] != 24 {
		t.Errorf("geo flags wrong: %v %v %v", m["geodata-mode"], m["geo-auto-update"], m["geo-update-interval"])
	}
	// Zero settings add nothing.
	out, err = Compose(Input{Subscription: []byte(testSub)})
	if err != nil {
		t.Fatal(err)
	}
	m = decode(t, out.YAML)
	for _, k := range []string{"geox-url", "geodata-mode", "geo-auto-update", "geo-update-interval"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s must not be set for zero GeoSettings", k)
		}
	}
}

func TestComposeProbe(t *testing.T) {
	out, err := Compose(Input{
		Subscription: []byte(testSub + "listeners:\n  - name: lanflow-probe\n    type: http\n    port: 1\n  - name: other\n    type: socks\n    port: 2\n"),
		Managed:      Managed{ProbePort: 41234},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	ls, _ := m["listeners"].([]any)
	if len(ls) != 2 {
		t.Fatalf("stale probe listener must be replaced, got %v", ls)
	}
	probe, _ := ls[1].(map[string]any)
	if probe["name"] != "lanflow-probe" || probe["type"] != "http" || probe["listen"] != "127.0.0.1" || probe["port"] != 41234 || probe["proxy"] != "LANFLOW-PROBE" {
		t.Errorf("probe listener wrong: %v", probe)
	}
	gs, _ := m["proxy-groups"].([]any)
	last, _ := gs[len(gs)-1].(map[string]any)
	if last["name"] != "LANFLOW-PROBE" || last["type"] != "select" || last["include-all"] != true || last["hidden"] != true {
		t.Errorf("probe group wrong: %v", last)
	}
	if ps, _ := last["proxies"].([]any); len(ps) != 1 || ps[0] != "DIRECT" {
		t.Errorf("probe group proxies wrong: %v", last["proxies"])
	}
	for _, g := range out.Groups {
		if g == "LANFLOW-PROBE" {
			t.Error("probe group must be hidden from Groups")
		}
	}
	// Without a probe port, nothing is added (and a stale group is removed).
	out, err = Compose(Input{Subscription: []byte(testSub + "proxy-groups+:\n  - {name: LANFLOW-PROBE, type: select, proxies: [DIRECT]}\n")})
	if err == nil {
		// The `proxy-groups+` key is literal YAML here, not an override; make sure compose doesn't choke.
		m = decode(t, out.YAML)
		if _, ok := m["listeners"]; ok {
			t.Error("no listeners expected")
		}
	}
	out, err = Compose(Input{Subscription: []byte(testSub), GlobalOverride: "proxy-groups+:\n  - {name: LANFLOW-PROBE, type: select, proxies: [DIRECT]}\n"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out.YAML), "LANFLOW-PROBE") {
		t.Error("stale probe group must be removed when no probe port")
	}
}

func TestComposeCustomRules(t *testing.T) {
	t.Run("prepended and validated", func(t *testing.T) {
		out, err := Compose(Input{
			Subscription: []byte(testSub),
			SubOverride:  "+rules:\n  - DOMAIN,override.example,DIRECT\n",
			CustomRules: []model.CustomRule{
				{Type: "DOMAIN-SUFFIX", Payload: "openai.com", Target: "节点选择"},
				{Type: "process-name", Payload: "Telegram", Target: "香港 01"},
				{Type: "IP-CIDR", Payload: "10.0.0.0/8", Target: "DIRECT"},
				{Type: "DOMAIN-KEYWORD", Payload: "ads", Target: "REJECT"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		rules, _ := decode(t, out.YAML)["rules"].([]any)
		// Custom rules first, then (IPv6 off here) the IPv6 reject rule, then the subscription's.
		want := []any{"DOMAIN-SUFFIX,openai.com,节点选择", "PROCESS-NAME,Telegram,香港 01", "IP-CIDR,10.0.0.0/8,DIRECT", "DOMAIN-KEYWORD,ads,REJECT", ipv6RejectRule, "DOMAIN,override.example,DIRECT"}
		for i, w := range want {
			if rules[i] != w {
				t.Errorf("rules[%d] = %v, want %v", i, rules[i], w)
			}
		}
	})
	t.Run("rules created when absent", func(t *testing.T) {
		out, err := Compose(Input{
			Subscription: []byte("proxies: [{name: a, type: ss, server: s, port: 1, cipher: aes-128-gcm, password: p}]\n"),
			CustomRules:  []model.CustomRule{{Type: "MATCH", Payload: "x", Target: "a"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if rules, _ := decode(t, out.YAML)["rules"].([]any); len(rules) != 2 || rules[0] != "MATCH,x,a" || rules[1] != ipv6RejectRule {
			t.Errorf("rules = %v", rules)
		}
	})
	errCases := []struct {
		name  string
		rules []model.CustomRule
		want  string
	}{
		{"unknown target", []model.CustomRule{{Type: "DOMAIN", Payload: "a.com", Target: "不存在"}}, "自定义规则第 1 条「DOMAIN,a.com」的目标「不存在」不存在"},
		{"hint lists groups", []model.CustomRule{{Type: "DOMAIN", Payload: "a.com", Target: "x"}}, "节点选择、自动选择"},
		{"second rule", []model.CustomRule{{Type: "DOMAIN", Payload: "a.com", Target: "DIRECT"}, {Type: "DOMAIN", Payload: "b.com", Target: "LANFLOW-PROBE"}}, "第 2 条"},
		{"empty target", []model.CustomRule{{Type: "DOMAIN", Payload: "a.com"}}, "没有目标"},
		{"empty type", []model.CustomRule{{Payload: "a.com", Target: "DIRECT"}}, "缺少类型或内容"},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compose(Input{Subscription: []byte(testSub), CustomRules: tc.rules, Managed: Managed{ProbePort: 1}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestComposeLines(t *testing.T) {
	out, err := Compose(Input{
		Subscription:   []byte(testSub),
		SubOverride:    "unified-delay: true\nrules+:\n  - DOMAIN,sub.example,DIRECT\n",
		GlobalOverride: "+rules:\n  - DOMAIN,global.example,DIRECT\nlog-level: error\n",
		CustomRules:    []model.CustomRule{{Type: "DOMAIN", Payload: "custom.example", Target: "DIRECT"}},
		Managed:        Managed{MixedPort: 7890, ProbePort: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The annotated lines are exactly the YAML.
	var texts []string
	for _, l := range out.Lines {
		texts = append(texts, l.Text)
	}
	if strings.Join(texts, "\n")+"\n" != string(out.YAML) {
		t.Fatalf("Lines do not reproduce YAML:\n%s\n---\n%s", strings.Join(texts, "\n"), out.YAML)
	}
	src := map[string]string{}
	for _, l := range out.Lines {
		if l.Source != "" {
			src[strings.TrimSpace(l.Text)] = l.Source
		}
	}
	want := map[string]string{
		"mixed-port: 7890": layerLanflow,
		"allow-lan: false": layerLanflow,
		"mode: rule":       layerLanflow,
		"log-level: info":  layerLanflow, // managed wins over the global override
		"proxies:":         layerSub,
		"- {name: 东京 01, type: ss, server: 1.1.1.1, port: 8388, cipher: aes-128-gcm, password: pw}": layerSub,
		"proxy-groups:":                   layerSub,
		"- name: 节点选择":                    layerSub,
		"- name: LANFLOW-PROBE":           layerLanflow,
		"rules:":                          layerSub,
		"- DOMAIN,custom.example,DIRECT":  layerCustom,
		"- DOMAIN,global.example,DIRECT":  layerGlobal,
		"- DOMAIN-SUFFIX,google.com,节点选择": layerSub,
		"- DOMAIN,sub.example,DIRECT":     layerSubOverride,
		"unified-delay: true":             layerSubOverride,
		"ipv6: true":                      layerLanflow, // always on so the TUN keeps its inet6 address
		"- " + ipv6RejectRule:             layerLanflow, // Lanflow's IPv6 setting is off here
		"profile:":                        layerLanflow,
		"tun:":                            layerLanflow,
		"listeners:":                      layerLanflow,
		"- name: lanflow-probe":           layerLanflow,
		"sniffer:":                        layerLanflow,
	}
	for text, layer := range want {
		if got := src[text]; got != layer {
			t.Errorf("line %q: source %q, want %q", text, got, layer)
		}
	}
	// Continuation lines carry no source.
	for i, l := range out.Lines {
		if strings.HasPrefix(l.Text, "    ") && l.Source != "" {
			t.Errorf("line %d %q is a continuation but has source %q", i, l.Text, l.Source)
		}
	}
	if out.Lines[0].Source == "" {
		t.Error("first line must have a source")
	}
}

func TestComposeDanglingAlias(t *testing.T) {
	sub := "proxies:\n  - {name: a, type: ss, server: s, port: 1, cipher: aes-128-gcm, password: p}\ncommon: &c\n  url: https://x\nproxy-groups:\n  - name: G\n    type: url-test\n    <<: *c\n    proxies: [a]\n"
	out, err := Compose(Input{Subscription: []byte(sub), GlobalOverride: "common!: {url: https://y}\n"})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML) // must still parse
	gs, _ := m["proxy-groups"].([]any)
	g, _ := gs[0].(map[string]any)
	if g["url"] != "https://x" {
		t.Errorf("alias should resolve to the original value, got %v", g)
	}
}

// TestComposeMihomoCheck pipes composed configs through `mihomo -t` when the
// binary is available.
func TestComposeMihomoCheck(t *testing.T) {
	bin := filepath.Join("..", "..", "bin", "mihomo")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("./bin/mihomo not present")
	}
	cases := map[string]Input{
		"basic": {Subscription: []byte(testSub), Managed: Managed{MixedPort: 17890}},
		"full": {
			Subscription: []byte(testSub),
			CustomRules:  []model.CustomRule{{Type: "DOMAIN-SUFFIX", Payload: "openai.com", Target: "节点选择"}},
			DNS:          model.DNSSettings{TakeOver: true, Nameserver: []string{"https://doh.pub/dns-query"}, Policy: []model.KV{{Key: "geosite:cn", Value: "223.5.5.5"}}},
			Sniffer:      model.SnifferSettings{Enable: true, TakeOver: true, HTTP: true, TLS: true, QUIC: true, OverrideDestination: true},
			Geo:          model.GeoSettings{Mode: "db", AutoUpdate: true, IntervalHours: 24},
			Managed:      Managed{MixedPort: 17890, AllowLAN: true, TUN: true, TUNStack: "mixed", ProbePort: 17891, IPv6: true},
		},
		"coexist": {
			Subscription: []byte(testSub),
			Managed:      Managed{MixedPort: 17890, TUN: true},
			Coexist:      Coexist{Processes: []string{"aTrustAgent"}, ExcludeCIDRs: []string{"2.0.0.0/24", "198.18.0.0/16"}, CorpDomains: []string{"corp.example.com"}},
		},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := Compose(in)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			// Seed the work dir with the bundled geo databases so -t never
			// depends on downloading them (a flaky network made this fail).
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
			cmd := exec.Command(bin, "-d", dir, "-f", cfg, "-t")
			res, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("mihomo -t failed: %v\n%s\n--- config ---\n%s", err, res, out.YAML)
			}
		})
	}
}
