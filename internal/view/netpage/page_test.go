package netpage

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

type rec struct{ toasts, nav []string }

func testEnv(f *fake.Fake) (*env.Env, *rec) {
	r := &rec{}
	e := &env.Env{API: f, Kit: env.Basic(), Mac: true,
		Navigate: func(p env.Page, a string) { r.nav = append(r.nav, string(p)+":"+a) },
		Arg:      func(env.Page) string { return "" },
		Toast:    func(m string, bad bool) { r.toasts = append(r.toasts, m) },
		Run: func(job func() error, ok string) {
			if err := job(); err != nil {
				r.toasts = append(r.toasts, err.Error())
			} else if ok != "" {
				r.toasts = append(r.toasts, ok)
			}
		},
		Confirm: func(title, body, okLabel string, danger bool, onOK func()) { onOK() },
		Redraw:  func() {},
	}
	return e, r
}

func TestCardsAndSites(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1400)
	for _, s := range []string{"直连 · 本机公网", "116.227.38.12", "中国电信 AS4812", "代理出口 · 节点选择", "45.76.211.38", "东京 03", "Vultr AS20473", "NRT", "经中转", "入口 上海 · 中国电信（101.89.12.40） → 落地 东京", "网站连通性", "Google", "www.google.com", "本机网络", "Home-5G", "代理状态", "指向澜流 127.0.0.1:7890", "DNS 查询", "泄漏检查"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q", s)
		}
	}
	if err := tt.Click("在地图中查看"); err != nil {
		t.Fatal(err)
	}
	if len(r.nav) != 1 || r.nav[0] != "overview:" {
		t.Fatalf("nav: %q", r.nav)
	}
	if err := tt.Click("重新测试"); err != nil {
		t.Fatal(err)
	}
	for _, s := range f.Sites() {
		if s.ViaProxy <= 0 || s.Direct == -2 || s.Direct == 0 {
			t.Fatalf("sites not retested: %+v", s)
		}
	}
	if err := tt.Click("清除 DNS 缓存"); err != nil {
		t.Fatal(err)
	}
	if len(r.toasts) == 0 || r.toasts[len(r.toasts)-1] != "已清除 DNS 缓存" {
		t.Fatalf("toasts: %q", r.toasts)
	}
}

func TestDNSQuery(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1400)
	if err := tt.Click("查询"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("198.18.0.") || !tt.HasText("应用拿到的 Fake-IP") || !tt.HasText("真实解析结果") || !tt.HasText("142.250.196.68") {
		t.Fatalf("fake-ip result: %q", tt.Texts())
	}
	p.dnsName = "www.baidu.com"
	tt.Frame()
	if err := tt.Click("查询"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("110.242.68.3") || tt.HasText("真实解析结果") {
		t.Fatalf("real result: %q", tt.Texts())
	}
}

func TestAddSite(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1400)
	if err := tt.Click("＋ 添加网站"); err != nil {
		t.Fatal(err)
	}
	if !p.addOpen {
		t.Fatal("dialog did not open")
	}
	tt.Type("Netflix")
	p.addURL = "netflix.com"
	tt.Frame()
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("请输入以 http:// 或 https:// 开头的网址") || !p.addOpen {
		t.Fatalf("bad URL should be rejected: %q", tt.Texts())
	}
	p.addURL = "https://www.netflix.com"
	tt.Frame()
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	sites := f.Settings().Sites
	if p.addOpen || len(sites) != 8 || sites[7] != (model.Site{Name: "Netflix", URL: "https://www.netflix.com"}) {
		t.Fatalf("sites after add: open=%v %+v", p.addOpen, sites)
	}
	if len(r.toasts) == 0 || r.toasts[len(r.toasts)-1] != "已添加「Netflix」" {
		t.Fatalf("toasts: %q", r.toasts)
	}
}

func TestStoppedAndDirectMode(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	if err := f.SetMode(model.ModeDirect); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(p.View, 1000, 1400)
	if !tt.HasText("直连模式下流量不经过代理") || tt.HasText("45.76.211.38") {
		t.Fatalf("direct mode: %q", tt.Texts())
	}
	if err := f.StopCore(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("内核未启动") {
		t.Fatalf("stopped: %q", tt.Texts())
	}
	_ = tt.Click("查询")
	if p.dnsRes != nil || tt.HasText("NOERROR") {
		t.Fatal("查询 should be disabled while the core is stopped")
	}
}

// countDNS counts EffectiveDNS reads.
type countDNS struct {
	*fake.Fake
	n int
}

func (c *countDNS) EffectiveDNS() model.EffectiveDNS { c.n++; return c.Fake.EffectiveDNS() }

// TestEffectiveDNSCached: the effective DNS is read once, and again only
// after the settings change.
func TestEffectiveDNSCached(t *testing.T) {
	f := fake.NewInstant()
	api := &countDNS{Fake: f}
	e, _ := testEnv(f)
	e.API = api
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1400)
	tt.Frame()
	tt.Frame()
	if api.n != 1 {
		t.Fatalf("EffectiveDNS read %d times over three builds, want 1", api.n)
	}
	if err := f.SaveSettings(f.Settings()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if api.n != 2 {
		t.Fatalf("EffectiveDNS not read again after a settings change: %d", api.n)
	}
}
