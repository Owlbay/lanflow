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

// A subscription (and an override) trying every way of reaching the
// decrypting outbound named in docs/capture.md §3.3.
const captureSub = `
proxies:
  - {name: n1, type: socks5, server: 127.0.0.1, port: 1}
  - {name: LANFLOW-MITM-evil, type: http, server: 127.0.0.1, port: 2}
  - {name: chained, type: socks5, server: 127.0.0.1, port: 3, dialer-proxy: LANFLOW-MITM-abcd}
proxy-groups:
  - {name: sel, type: select, proxies: [n1, LANFLOW-MITM-abcd, DIRECT]}
  - {name: auto, type: url-test, include-all: true, url: "http://x/", interval: 300}
  - {name: filt, type: select, include-all-proxies: true, filter: "n", exclude-filter: "evil"}
listeners:
  - {name: back, type: http, listen: 127.0.0.1, port: 23456, proxy: LANFLOW-MITM-abcd}
  - {name: lanflow-mitm-up, type: http, listen: 0.0.0.0, port: 23457}
rule-providers:
  lanflow-mitm-sel: {type: inline, behavior: domain, payload: ["+.everything"]}
sub-rules:
  s1:
    - DOMAIN,a.com,LANFLOW-MITM-abcd
    - MATCH,DIRECT
rules:
  - DOMAIN,x.com,LANFLOW-MITM-abcd
  - SUB-RULE,(NETWORK,TCP),s1
  - MATCH,sel
`

func captureInput() Input {
	return Input{
		Subscription:   []byte(captureSub),
		GlobalOverride: "+rules:\n  - MATCH,LANFLOW-MITM-abcd\n",
		CustomRules:    []model.CustomRule{{Type: "DOMAIN", Payload: "custom.com", Target: "DIRECT"}},
		Managed:        Managed{MixedPort: 7890, Mode: model.ModeRule, ProbePort: 7999, IPv6: false},
		Coexist:        Coexist{CorpDomains: []string{"corp.example"}},
		Capture: Capture{
			AgentPort: 30001, UpPort: 30002, UpUser: "lanflow", UpPass: "uppw",
			Domains: []string{"api.example.com", "*.example.org", "+.example.net"},
			Domain:  CaptureSelector{Outbound: "LANFLOW-MITM-abcd", User: "lanflow", Pass: "pw"},
			Apps: []CaptureApp{{CaptureSelector: CaptureSelector{Outbound: "LANFLOW-MITM-abcd-A0", User: "app0", Pass: "pw0"},
				Rules: []string{"PROCESS-PATH-WILDCARD,/Applications/Foo.app/*"}}},
			Bypass: []string{"+.apple.com", "10.0.0.0/8", "ocsp.*"},
			Ports:  "80/443/8443",
		},
	}
}

func TestComposeCapture(t *testing.T) {
	out, err := compose(captureInput())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out.YAML, &doc); err != nil {
		t.Fatal(err)
	}
	rules := toStrings(doc["rules"])
	// order: coexist → capture (apps, then domains) → custom → IPv6 reject → subscription
	if !strings.HasPrefix(rules[0], "DOMAIN-SUFFIX,corp.example,DIRECT") {
		t.Fatalf("coexist not first: %v", rules[:3])
	}
	if !strings.HasPrefix(rules[1], "AND,((RULE-SET,lanflow-mitm-app-0),") || !strings.HasSuffix(rules[1], ",LANFLOW-MITM-abcd-A0") {
		t.Fatalf("app rule: %s", rules[1])
	}
	if !strings.HasPrefix(rules[2], "AND,((RULE-SET,lanflow-mitm-sel),(NOT,((IN-NAME,lanflow-mitm-up))),(NETWORK,TCP),(DST-PORT,80/443/8443),(OR,((SRC-IP-CIDR,127.0.0.0/8),(SRC-IP-CIDR,::1/128),(IN-TYPE,TUN))),(NOT,((IP-CIDR6,::/0,no-resolve))),(NOT,((RULE-SET,lanflow-mitm-bypass)))),LANFLOW-MITM-abcd") {
		t.Fatalf("domain rule: %s", rules[2])
	}
	if rules[3] != "DOMAIN,custom.com,DIRECT" || rules[4] != ipv6RejectRule {
		t.Fatalf("custom / ipv6: %v", rules[3:5])
	}
	for _, r := range rules[3:] {
		if strings.Contains(strings.ToUpper(r), "LANFLOW-MITM") {
			t.Fatalf("reference kept: %s", r)
		}
	}
	if !strings.Contains(strings.Join(rules, "\n"), "SUB-RULE,(NETWORK,TCP),s1") {
		t.Fatal("sub-rule reference dropped")
	}
	sub := doc["sub-rules"].(map[string]any)
	if s1 := toStrings(sub["s1"]); len(s1) != 1 || s1[0] != "MATCH,DIRECT" {
		t.Fatalf("sub-rules: %v", s1)
	}
	// proxies: ours present once, the impostor gone, dialer-proxy stripped
	var mitm int
	for _, p := range doc["proxies"].([]any) {
		m := p.(map[string]any)
		name := m["name"].(string)
		if name == "LANFLOW-MITM-evil" {
			t.Fatal("impostor proxy kept")
		}
		if strings.HasPrefix(name, "LANFLOW-MITM-abcd") {
			mitm++
			if m["server"] != "127.0.0.1" || m["port"] != 30001 {
				t.Fatalf("outbound %v", m)
			}
		}
		if name == "chained" && m["dialer-proxy"] != nil {
			t.Fatal("dialer-proxy kept")
		}
	}
	if mitm != 2 {
		t.Fatalf("outbounds %d", mitm)
	}
	for _, g := range doc["proxy-groups"].([]any) {
		m := g.(map[string]any)
		for _, p := range toStrings(m["proxies"]) {
			if strings.HasPrefix(p, "LANFLOW-MITM") {
				t.Fatalf("group %v keeps %s", m["name"], p)
			}
		}
		switch m["name"] {
		case "auto", probeGroupName:
			if m["exclude-filter"] != "^LANFLOW-MITM" {
				t.Fatalf("%v exclude-filter %v", m["name"], m["exclude-filter"])
			}
		case "filt":
			if m["exclude-filter"] != "evil`^LANFLOW-MITM" {
				t.Fatalf("filter appended wrongly: %v", m["exclude-filter"])
			}
		}
	}
	var ups int
	for _, l := range doc["listeners"].([]any) {
		m := l.(map[string]any)
		if m["name"] == "back" {
			t.Fatal("listener into the outbound kept")
		}
		if m["name"] == CaptureUpListener {
			ups++
			if m["listen"] != "127.0.0.1" || m["port"] != 30002 || m["udp"] != false {
				t.Fatalf("up listener %v", m)
			}
		}
	}
	if ups != 1 {
		t.Fatalf("up listeners %d", ups)
	}
	rp := doc["rule-providers"].(map[string]any)
	sel := rp["lanflow-mitm-sel"].(map[string]any)
	if got := toStrings(sel["payload"]); strings.Join(got, "|") != "DOMAIN,api.example.com|DOMAIN-WILDCARD,*.example.org|DOMAIN-SUFFIX,example.net" {
		t.Fatalf("sel payload %v", got)
	}
	if sel["behavior"] != "classical" {
		t.Fatal("impostor provider kept")
	}
	if got := toStrings(rp["lanflow-mitm-bypass"].(map[string]any)["payload"]); strings.Join(got, "|") != "DOMAIN-SUFFIX,apple.com|IP-CIDR,10.0.0.0/8|DOMAIN-WILDCARD,ocsp.*" {
		t.Fatalf("bypass payload %v", got)
	}
	if len(out.Warnings) < 7 {
		t.Fatalf("warnings: %v", out.Warnings)
	}
	// the runtime view attributes the layer
	var seen bool
	for _, l := range out.Lines {
		if l.Source == layerCapture {
			seen = true
		}
	}
	if !seen {
		t.Fatal("no line attributed to 抓包")
	}
	checkWithCore(t, out.YAML)
}

func TestComposeCaptureZero(t *testing.T) {
	in := captureInput()
	in.Capture = Capture{}
	out, err := compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out.YAML), "lanflow-mitm-up") || strings.Contains(strings.ToUpper(string(out.YAML)), "LANFLOW-MITM-ABCD") {
		t.Fatalf("zero capture still writes the layer or keeps references:\n%s", out.YAML)
	}
	checkWithCore(t, out.YAML)
}

func TestComposeCaptureIPv6On(t *testing.T) {
	in := captureInput()
	in.Managed.IPv6 = true
	in.Capture.IPv6 = true
	in.Capture.Bypass = nil
	in.Capture.Apps = nil
	out, err := compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out.YAML), "IP-CIDR6,::/0,no-resolve") || strings.Contains(string(out.YAML), "lanflow-mitm-bypass") {
		t.Fatal("IPv6 / empty bypass clauses written")
	}
	checkWithCore(t, out.YAML)
}

func toStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		out = append(out, x.(string))
	}
	return out
}

// checkWithCore runs `mihomo -t` on the config when ./bin/mihomo exists.
func checkWithCore(t *testing.T, y []byte) {
	t.Helper()
	bin, _ := filepath.Abs(filepath.Join("..", "..", "bin", "mihomo"))
	if _, err := os.Stat(bin); err != nil {
		t.Log("bin/mihomo not present: core validation skipped")
		return
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "c.yaml")
	os.WriteFile(f, y, 0o600)
	out, err := exec.Command(bin, "-t", "-d", dir, "-f", f).CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo -t: %v\n%s\n%s", err, out, y)
	}
}

func TestComposeCaptureForcesFindProcess(t *testing.T) {
	in := captureInput()
	in.Advanced.FindProcessMode = "off"
	out, err := compose(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out.YAML), "find-process-mode: strict") {
		t.Fatal("find-process-mode off kept with a program selection")
	}
	in.Capture.Apps = nil
	out, _ = compose(in)
	if !strings.Contains(string(out.YAML), "find-process-mode: \"off\"") && !strings.Contains(string(out.YAML), "find-process-mode: off") {
		t.Fatalf("find-process-mode changed without a program selection:\n%s", out.YAML)
	}
}
