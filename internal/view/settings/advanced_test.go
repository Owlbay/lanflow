package settings

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

func TestAdvancedFormRoundTrip(t *testing.T) {
	a := model.AdvancedSettings{
		HTTPPort: 7891, SocksPort: 7892, LANAllowedIPs: []string{"192.168.0.0/16"}, Authentication: []string{"u:p"},
		PACMode: true, PACPort: 11234, TUNStrictRoute: true, TUNMTU: 1500, TUNRouteExcludeAddress: []string{"10.0.0.0/8"},
		FindProcessMode: "strict", KeepAliveIdle: 600, GeodataLoader: "standard", DNSListen: "127.0.0.1:1053", DNSCacheAlgorithm: "arc",
		DNSFallbackFilter: model.DNSFallbackFilter{GeoIP: true, GeoIPCode: "CN", Domain: []string{"+.google.com"}}, SnifferNoForceDNSMapping: true,
	}
	got, err := advFormFrom(a).toSettings(7890)
	if err != nil {
		t.Fatal(err)
	}
	if advFormFrom(got) != advFormFrom(a) {
		t.Fatalf("round trip changed the settings:\n got %+v\nwant %+v", got, a)
	}
	if z, err := (advForm{}).toSettings(7890); err != nil || advFormFrom(z) != (advForm{}) {
		t.Fatalf("empty form: %+v %v", z, err)
	}
}

func TestAdvancedFormValidation(t *testing.T) {
	cases := []struct {
		name string
		f    advForm
		want string
	}{
		{"port range", advForm{httpPort: "70000"}, "HTTP 端口须为 1–65535"},
		{"port equals mixed", advForm{socksPort: "7890"}, "不能与代理端口 7890 相同"},
		{"ports equal", advForm{httpPort: "7891", socksPort: "7891"}, "不能相同"},
		{"bad cidr", advForm{lanAllowed: "192.168.0.0/16\nnope"}, "「nope」不是有效的 IP 或网段"},
		{"bad auth", advForm{auth: "justuser"}, "应写成 用户名:密码"},
		{"mtu", advForm{tunMTU: "100"}, "MTU须为 576–65535"},
		{"dns listen", advForm{dnsListen: "1053"}, "IP:端口"},
		{"keep alive", advForm{keepAliveInterval: "abc"}, "TCP 保活间隔须为"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.f.toSettings(7890)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

// TestAdvancedTabApplies: the tab renders, edits make the apply bar
// appear, invalid input is refused with a toast and kept, valid input is
// saved through SaveSettings.
func TestAdvancedTabApplies(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	// Two columns (≥1100 pt) keep every control of the tab within reach.
	tt := ui.NewTester(p.View, 1400, 900)
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"端口与局域网", "PAC 模式", "严格路由", "内核行为", "统一延迟", "DNS 高级", "嗅探高级"} {
		if !tt.HasText(s) {
			t.Fatalf("missing %q: %q", s, tt.Texts())
		}
	}
	if tt.HasText("有未应用的更改") {
		t.Fatal("no edits yet")
	}
	clickRole(t, tt, ui.RoleSwitch, "统一延迟")
	tt.Frame()
	if !p.advDirty() || !tt.HasText("有未应用的更改") {
		t.Fatal("switch should make the form dirty")
	}
	clickRole(t, tt, ui.RoleTextField, "HTTP 端口")
	tt.Type("7890")
	tt.Frame()
	if err := tt.Click("应用"); err != nil {
		t.Fatal(err)
	}
	if len(r.toasts) == 0 || !strings.Contains(r.toasts[len(r.toasts)-1], "不能与代理端口 7890 相同") {
		t.Fatalf("invalid port should toast: %q", r.toasts)
	}
	if f.Settings().Advanced.UnifiedDelay || p.adv.httpPort != "7890" {
		t.Fatal("nothing may be saved while the form is invalid, and the input stays")
	}
	clickRole(t, tt, ui.RoleTextField, "HTTP 端口")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("7891")
	tt.Frame()
	if err := tt.Click("应用"); err != nil {
		t.Fatal(err)
	}
	got := f.Settings().Advanced
	if !got.UnifiedDelay || got.HTTPPort != 7891 {
		t.Fatalf("applied: %+v", got)
	}
	tt.Frame()
	if p.advDirty() || tt.HasText("有未应用的更改") {
		t.Fatal("apply bar should go after applying")
	}
	if r.toasts[len(r.toasts)-1] != "高级设置已应用" {
		t.Fatalf("toasts: %q", r.toasts)
	}

	// 放弃 reverts, switching tabs with edits asks first.
	clickRole(t, tt, ui.RoleSwitch, "PAC 模式")
	tt.Frame()
	if !p.advDirty() {
		t.Fatal("dirty after toggling PAC")
	}
	r.confirmAccept = false
	if err := tt.Click("常规"); err != nil {
		t.Fatal(err)
	}
	if p.Tab() != "advanced" || len(r.confirms) != 1 {
		t.Fatalf("tab %q confirms %q", p.Tab(), r.confirms)
	}
	if err := tt.Click("放弃"); err != nil {
		t.Fatal(err)
	}
	if p.advDirty() || p.adv.pacMode {
		t.Fatal("放弃 should restore the applied settings")
	}
}

// TestAllowLANWarnsWithoutAuth: the 代理 tab warns when LAN access is on
// and no authentication is set, and the warning links to the 高级 tab.
func TestAllowLANWarnsWithoutAuth(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if err := tt.Click("代理"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("没有设置代理认证") {
		t.Fatal("no warning while LAN access is off")
	}
	clickRole(t, tt, ui.RoleSwitch, "允许局域网连接")
	tt.Frame()
	if !tt.HasText("没有设置代理认证") {
		t.Fatalf("warning missing: %q", tt.Texts())
	}
	if err := tt.Click("设置认证…"); err != nil {
		t.Fatal(err)
	}
	if p.Tab() != "advanced" {
		t.Fatalf("tab %q", p.Tab())
	}
	s := f.Settings()
	s.Advanced.Authentication = []string{"u:p"}
	if err := f.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("代理"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("没有设置代理认证") {
		t.Fatal("warning should go once authentication is set")
	}
}
