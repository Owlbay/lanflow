package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

const sampleOverrideJS = `function main(config) {
  const atrustDns = [
    "10.1.0.53",
    "10.1.0.54", // 备用
    "not an ip"
  ];
  // DoH 会被掐断 // 见 https://example.com/doc
  const publicDns = ["https://doh.pub/dns-query", "223.5.5.5"];
  /* 排除
     多行注释 ] 带括号 */
  const atrustCIDRs = [
    "172.16.0.0/12",
    "198.18.0.0/16",
    '203.0.113.10/32',
    "203.0.113.10/32"
  ];
  const corpDomains = ["corp.example.com", "Intranet.Example.NET.", "+.corp.example.com", "bad domain"];
  const publicSaasDomains = ["chat.example.org"];
  const atrustDomains = [...corpDomains, ...publicSaasDomains];
  const ownRules = [
    ...atrustDomains.map(domain => ` + "`DOMAIN-SUFFIX,${domain},DIRECT`" + `),
    "IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
    "IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
    "IP-CIDR,198.18.0.0/16,DIRECT,no-resolve"
  ];
  config.rules = [...ownRules, ...(config.rules || [])];
  return config;
}
`

func TestImportATrustJS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "override.js")
	if err := os.WriteFile(path, []byte(sampleOverrideJS), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, summary, err := ReadATrustConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := model.ATrustConfig{
		CorpDomains:   []string{"corp.example.com", "intranet.example.net"},
		DirectDomains: []string{"chat.example.org"},
		CorpDNS:       []string{"10.1.0.53", "10.1.0.54"},
		ExcludeCIDRs:  []string{"172.16.0.0/12", "198.18.0.0/16", "203.0.113.10/32"},
		DirectCIDRs:   []string{"10.0.0.0/8"},
	}
	if !equalConfig(cfg, want) {
		t.Fatalf("imported = %+v\nwant %+v", cfg, want)
	}
	for _, s := range []string{"override.js", "内网域名 2 个", "直连域名 1 个", "公司 DNS 2 个", "网段 3 个", "仅直连的网段 1 个", "忽略了 2 项"} {
		if !strings.Contains(summary, s) {
			t.Errorf("summary %q lacks %q", summary, s)
		}
	}
}

// TestImportATrustKit reads a real override script when LANFLOW_ATRUST_JS
// points at one.
func TestImportATrustKit(t *testing.T) {
	path := os.Getenv("LANFLOW_ATRUST_JS")
	if path == "" {
		t.Skip("set LANFLOW_ATRUST_JS to an atrust-coexist.js to run")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("override script not present")
	}
	cfg, summary, err := ReadATrustConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(summary)
	if len(cfg.CorpDNS) != 2 || len(cfg.CorpDomains) < 10 || len(cfg.DirectDomains) < 5 || len(cfg.ExcludeCIDRs) < 5 {
		t.Fatalf("kit import = %+v", cfg)
	}
	if !slices.Contains(cfg.DirectCIDRs, "10.0.0.0/8") || slices.Contains(cfg.ExcludeCIDRs, "10.0.0.0/8") {
		t.Fatalf("10.0.0.0/8 must be DIRECT only: %+v", cfg)
	}
	for _, c := range []string{"172.16.0.0/12", "100.64.0.0/10", "198.18.0.0/16"} {
		if !slices.Contains(cfg.ExcludeCIDRs, c) || slices.Contains(cfg.DirectCIDRs, c) {
			t.Errorf("%s should be excluded once: %+v", c, cfg)
		}
	}
	for _, d := range cfg.CorpDomains {
		if slices.Contains(cfg.DirectDomains, d) {
			t.Errorf("%s in both domain lists", d)
		}
	}
	if strings.Contains(summary, "忽略") {
		t.Errorf("the kit should import cleanly: %s", summary)
	}
}

func TestATrustConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrust.yaml")
	cfg := model.ATrustConfig{Always: true, CorpDomains: []string{"corp.example.com"}, DirectDomains: []string{"chat.example.org"},
		CorpDNS: []string{"10.1.0.53"}, ExcludeCIDRs: []string{"198.18.0.0/16"}, DirectCIDRs: []string{"10.0.0.0/8"}}
	if err := WriteATrustConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, summary, err := ReadATrustConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !equalConfig(got, cfg) || !got.Always {
		t.Fatalf("round trip = %+v, want %+v (%s)", got, cfg, summary)
	}

	// the JSON of the model (as in a settings export) is read too
	jsonPath := filepath.Join(dir, "atrust.json")
	if err := os.WriteFile(jsonPath, []byte(`{"CorpDomains":["a.example.com"],"ExcludeCIDRs":["203.0.113.0/24","203.0.113.0/24"],"DirectCIDRs":["10.1.2.3"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, err = ReadATrustConfig(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if !equalConfig(got, model.ATrustConfig{CorpDomains: []string{"a.example.com"}, ExcludeCIDRs: []string{"203.0.113.0/24"}, DirectCIDRs: []string{"10.1.2.3/32"}}) {
		t.Fatalf("json import = %+v", got)
	}

	// the reference template itself imports (its example values)
	refPath := filepath.Join(dir, "reference.yaml")
	if err := os.WriteFile(refPath, []byte(ATrustReferenceYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, _, err := ReadATrustConfig(refPath)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Always || !slices.Contains(ref.CorpDomains, "corp.example.com") || !slices.Contains(ref.DirectCIDRs, "10.0.0.0/8") || !slices.Contains(ref.ExcludeCIDRs, "203.0.113.10/32") {
		t.Fatalf("reference import = %+v", ref)
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(ATrustReferenceYAML()), &m); err != nil {
		t.Fatalf("reference is not YAML: %v", err)
	}

	// nothing usable
	empty := filepath.Join(dir, "empty.yaml")
	_ = os.WriteFile(empty, []byte("foo: bar\n"), 0o600)
	if _, _, err := ReadATrustConfig(empty); err == nil {
		t.Fatal("empty file imported")
	}
	if _, _, err := ReadATrustConfig(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing file imported")
	}
}

func TestATrustNormalisers(t *testing.T) {
	for in, want := range map[string]string{"Corp.Example.COM.": "corp.example.com", "+.a.b": "a.b", "*.x.y": "x.y", "in.za": "in.za", "a_b.c": "a_b.c"} {
		if got, ok := NormDomain(in); !ok || got != want {
			t.Errorf("NormDomain(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "10.0.0.1", "a b", "-a.com", "a..b", "a.com/x"} {
		if _, ok := NormDomain(bad); ok {
			t.Errorf("NormDomain(%q) accepted", bad)
		}
	}
	for in, want := range map[string]string{"10.0.0.0/8": "10.0.0.0/8", "10.1.2.3": "10.1.2.3/32", "10.1.2.3/24": "10.1.2.0/24", "fd00::/8": "fd00::/8"} {
		if got, ok := NormCIDR(in); !ok || got != want {
			t.Errorf("NormCIDR(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "10.0.0.0/33", "example.com", "0.0.0.0"} {
		if _, ok := NormCIDR(bad); ok {
			t.Errorf("NormCIDR(%q) accepted", bad)
		}
	}
	if _, ok := NormIP("10.0.0.1"); !ok {
		t.Error("NormIP rejects a valid address")
	}
	for _, bad := range []string{"", "0.0.0.0", "10.0.0.0/8", "dns.example.com"} {
		if _, ok := NormIP(bad); ok {
			t.Errorf("NormIP(%q) accepted", bad)
		}
	}
}

func equalConfig(a, b model.ATrustConfig) bool {
	return slices.Equal(a.CorpDomains, b.CorpDomains) && slices.Equal(a.DirectDomains, b.DirectDomains) &&
		slices.Equal(a.CorpDNS, b.CorpDNS) && slices.Equal(a.ExcludeCIDRs, b.ExcludeCIDRs) && slices.Equal(a.DirectCIDRs, b.DirectCIDRs)
}
