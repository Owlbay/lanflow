package netpage

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

// 泄漏检查 runs on demand and lists each item with its state and
// explanation; the button is live (no longer a placeholder).
func TestLeakCheckSection(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1600)
	if tt.HasText("即将推出") || !tt.HasText("开始检查") || tt.HasText("正常") {
		t.Fatalf("initial state: %q", tt.Texts())
	}
	if err := tt.Click("开始检查"); err != nil {
		t.Fatal(err)
	}
	if len(r.toasts) != 0 {
		t.Fatalf("toasts: %q", r.toasts)
	}
	lk := f.LeakCheck()
	if len(lk.Items) != 5 || lk.CheckedAt.IsZero() {
		t.Fatalf("result = %+v", lk)
	}
	// The five items as lines, and the button that hands the finer check
	// to the browser (never clicked here: it would open one).
	for _, s := range []string{"重新检查", "DNS", "IPv6", "部分", "正常", "仅遵守系统代理的应用由澜流解析域名", "本机没有可用的公网 IPv6",
		"WebRTC", "浏览器的 WebRTC 会暴露真实 IP（116.227.38.12）", "IP 类型", "出口 45.76.211.38 · AS20473 Vultr · 机房 IP",
		"DNS 对比", "系统解析器的结果与内核或公共 DNS 一致", "在浏览器中检查 IP 纯净度"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q", s)
		}
	}
	// The explanation and the structured rows open on a click of the line.
	if tt.HasText("请开启 TUN 模式") || tt.HasText("STUN 看到") {
		t.Fatalf("details shown before a click: %q", tt.Texts())
	}
	for _, line := range []string{"仅遵守系统代理的应用由澜流解析域名", "浏览器的 WebRTC 会暴露真实 IP（116.227.38.12）", "出口 45.76.211.38 · AS20473 Vultr · 机房 IP", "系统解析器的结果与内核或公共 DNS 一致"} {
		if err := tt.Click(line); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"请开启 TUN 模式", "STUN 看到", "家宽 / 运营商 IP", "系统解析器", "公共 DNS 223.5.5.5", "104.16.124.96, 104.16.123.96"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q after opening", s)
		}
	}
	// A domain typed into the field joins the comparison; the open
	// items stay open across a new run.
	p.leakDomain = "example.com"
	tt.Frame()
	if err := tt.Click("重新检查"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("example.com：93.184.215.14") {
		t.Fatalf("domain rows missing: %q", tt.Texts())
	}
}

// 代理状态 reports the effective DNS and sniffer sources (docs/design.md:463).
func TestProxyStatusReportsEffectiveDNS(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1600)
	if !tt.HasText("跟随订阅 · Fake-IP") || !tt.HasText("已开启 · 跟随订阅") {
		t.Fatalf("follow mode: %q", tt.Texts())
	}
	set := f.Settings()
	set.DNS.TakeOver, set.DNS.EnhancedMode = true, "redir-host"
	set.Sniffer.TakeOver = true
	if err := f.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("由澜流接管 · 真实 IP") || !tt.HasText("已开启 · 由澜流接管") {
		t.Fatalf("take-over: %q", tt.Texts())
	}
	if got := dnsStatusText(model.EffectiveDNS{Source: model.EffectiveDefault}); got != "跟随订阅（订阅未设置，使用内核默认值）" {
		t.Errorf("default = %q", got)
	}
	if got := dnsStatusText(model.EffectiveDNS{Source: model.EffectiveSubscription, Settings: model.DNSSettings{EnhancedMode: "fake-ip"}}); got != "跟随订阅 · 内置 DNS 未启用" {
		t.Errorf("disabled = %q", got)
	}
}
