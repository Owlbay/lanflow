package settings

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

func TestATrustFormParse(t *testing.T) {
	f := atrustForm{
		corp:       "Corp.Example.COM\n\n+.intranet.example.net\ncorp.example.com",
		direct:     "chat.example.org",
		dns:        "10.1.0.53",
		exclude:    "198.18.0.0/16\n203.0.113.10",
		directCIDR: "10.0.0.0/8",
		always:     true,
	}
	cfg, errs := f.parse()
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	want := model.ATrustConfig{Always: true, CorpDomains: []string{"corp.example.com", "intranet.example.net"}, DirectDomains: []string{"chat.example.org"},
		CorpDNS: []string{"10.1.0.53"}, ExcludeCIDRs: []string{"198.18.0.0/16", "203.0.113.10/32"}, DirectCIDRs: []string{"10.0.0.0/8"}}
	if !slices.Equal(cfg.CorpDomains, want.CorpDomains) || !slices.Equal(cfg.ExcludeCIDRs, want.ExcludeCIDRs) || !slices.Equal(cfg.DirectCIDRs, want.DirectCIDRs) || !cfg.Always {
		t.Fatalf("parsed = %+v", cfg)
	}

	// line errors name the line and the value
	f.corp = "corp.example.com\nnot a domain"
	f.dns = "dns.example.com"
	f.directCIDR = "10.0.0.0/33"
	_, errs = f.parse()
	if !strings.Contains(errs[fieldCorp], "第 2 行") || !strings.Contains(errs[fieldCorp], "not a domain") {
		t.Errorf("corp error = %q", errs[fieldCorp])
	}
	if !strings.Contains(errs[fieldDNS], "第 1 行") || !strings.Contains(errs[fieldDirectCIDR], "10.0.0.0/33") {
		t.Errorf("errors = %v", errs)
	}

	// the pitfall: a range holding the company DNS cannot be excluded
	f = atrustForm{dns: "10.139.1.142", exclude: "10.0.0.0/8"}
	_, errs = f.parse()
	if !strings.Contains(errs[fieldExclude], "10.139.1.142") || !strings.Contains(errs[fieldExclude], "仅直连的网段") {
		t.Errorf("DNS-in-range error = %q", errs[fieldExclude])
	}
	f.directCIDR, f.exclude = "10.0.0.0/8", "172.16.0.0/12\n10.0.0.0/8"
	_, errs = f.parse()
	if errs[fieldDirectCIDR] == "" {
		t.Error("a range in both lists not reported")
	}
}

func TestATrustRowDesc(t *testing.T) {
	d := atrustRowDesc(model.ATrustState{}, model.ATrustConfig{})
	if !strings.Contains(d, "检测到深信服 aTrust 时") {
		t.Errorf("idle desc = %q", d)
	}
	d = atrustRowDesc(model.ATrustState{Running: true, CIDRs: []string{"10.20.0.0/16"}, Domains: []string{"corp.example.com"}, Gateways: []string{"203.0.113.10"}},
		model.ATrustConfig{Always: true, CorpDomains: []string{"a.example.com", "b.example.com"}, ExcludeCIDRs: []string{"198.18.0.0/16"}})
	for _, want := range []string{"1 个内网网段、1 个内网域名", "网关 203.0.113.10", "已配置 2 个域名、1 个网段", "未运行 aTrust 时也应用"} {
		if !strings.Contains(d, want) {
			t.Errorf("desc %q lacks %q", d, want)
		}
	}
}

func TestATrustDialogSaves(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if err := tt.Click("代理"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("配置…"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("aTrust 兼容配置") || !tt.HasText("不进入 TUN 的网段") {
		t.Fatalf("dialog missing: %q", tt.Texts())
	}
	clickRole(t, tt, ui.RoleTextField, "内网域名")
	tt.Type("corp.example.com\nbad domain")
	tt.Frame()
	if !tt.HasText("第 2 行不是有效的域名：bad domain") {
		t.Fatalf("validation missing: %q", tt.Texts())
	}
	clickRole(t, tt, ui.RoleButton, "保存")
	if len(f.Settings().ATrust.CorpDomains) != 0 {
		t.Fatal("saved with an invalid line")
	}
	clickRole(t, tt, ui.RoleTextField, "内网域名")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("corp.example.com")
	clickRole(t, tt, ui.RoleTextField, "公司 DNS")
	tt.Type("10.1.0.53")
	clickRole(t, tt, ui.RoleTextField, "仅直连的网段")
	tt.Type("10.0.0.0/8")
	tt.Frame()
	clickRole(t, tt, ui.RoleSwitch, "未运行 aTrust 时也应用")
	tt.Frame()
	clickRole(t, tt, ui.RoleButton, "保存")
	got := f.Settings().ATrust
	if !slices.Equal(got.CorpDomains, []string{"corp.example.com"}) || !slices.Equal(got.CorpDNS, []string{"10.1.0.53"}) || !slices.Equal(got.DirectCIDRs, []string{"10.0.0.0/8"}) || !got.Always {
		t.Fatalf("saved = %+v", got)
	}
	tt.Frame()
	if !slices.Contains(r.toasts, "aTrust 兼容配置已保存") {
		t.Fatalf("toasts = %q", r.toasts)
	}
	if !tt.HasText("已配置 1 个域名、1 个网段（未运行 aTrust 时也应用）") {
		t.Fatalf("row desc: %q", tt.Texts())
	}

	// an import fills the form for review without saving
	path := filepath.Join(t.TempDir(), "kit.js")
	if err := os.WriteFile(path, []byte(`const corpDomains = ["x.example.com"]; const atrustCIDRs = ["203.0.113.0/24"];`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("配置…"); err != nil {
		t.Fatal(err)
	}
	cfg, summary, err := e.API.ImportATrustConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.atrust.pending = &cfg
	p.toast = summary
	p.mu.Unlock()
	tt.Frame()
	if p.atrust.form.corp != "x.example.com" || p.atrust.form.exclude != "203.0.113.0/24" {
		t.Fatalf("form after import = %+v", p.atrust.form)
	}
	if got := f.Settings().ATrust; slices.Contains(got.CorpDomains, "x.example.com") {
		t.Fatal("import saved without confirmation")
	}
	clickRole(t, tt, ui.RoleButton, "取消")
}
