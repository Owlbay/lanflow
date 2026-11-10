package settings

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

// The override tab lists the entries in order with their switches, creates
// a new one by name (then opens its editor), and a subscription's 选择…
// dialog saves its selection.
func TestOverrideListToggleCreateAndSelect(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if err := tt.Click("覆写"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"全局覆写", "去广告规则", "实验：IPv6 DNS", "覆写列表", "订阅使用的覆写", "全局覆写 + 去广告规则", "仅全局覆写"} {
		if !tt.HasText(s) {
			t.Fatalf("missing %q in %q", s, tt.Texts())
		}
	}
	// Toggling the disabled entry's switch saves it enabled.
	clickRole(t, tt, ui.RoleSwitch, "启用 实验：IPv6 DNS")
	tt.Frame()
	for _, o := range f.OverrideList() {
		if o.ID == "o3" && !o.Enabled {
			t.Fatal("switch did not enable the entry")
		}
	}
	// 新建… asks for a name, creates the entry and opens its editor.
	if err := tt.Click("新建…"); err != nil {
		t.Fatal(err)
	}
	if !p.ovl.name.open || !tt.HasText("新建覆写") {
		t.Fatalf("name dialog: %v %q", p.ovl.name.open, tt.Texts())
	}
	p.ovl.name.text = "我的规则"
	tt.Frame()
	if err := tt.Click("创建"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	var created model.Override
	for _, o := range f.OverrideList() {
		if o.Name == "我的规则" {
			created = o
		}
	}
	if created.ID == "" || !created.Enabled || created.Global {
		t.Fatalf("entry not created: %+v", f.OverrideList())
	}
	if !p.ov.open || p.ov.key != created.ID || !tt.HasText("编辑覆写 · 我的规则") {
		t.Fatalf("editor should open for the new entry: open=%v key=%q", p.ov.open, p.ov.key)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	// 选择… for 备用机场 picks 我的规则 and saves the subscription.
	clickRole(t, tt, ui.RoleButton, "选择… 备用机场")
	if !p.ovl.sel.open || p.ovl.sel.subID != "s2" || !tt.HasText("选择覆写 · 备用机场") {
		t.Fatalf("selection dialog: %+v", p.ovl.sel)
	}
	clickRole(t, tt, ui.RoleCheckBox, "我的规则")
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	var s2 model.Subscription
	for _, s := range f.Subscriptions() {
		if s.ID == "s2" {
			s2 = s
		}
	}
	if len(s2.Overrides) != 1 || s2.Overrides[0] != created.ID {
		t.Fatalf("selection not saved: %v (toasts %q)", s2.Overrides, r.toasts)
	}
	if !tt.HasText("全局覆写 + 我的规则") {
		t.Fatalf("row should name the selection: %q", tt.Texts())
	}
	if strings.Contains(strings.Join(r.toasts, "|"), "失败") {
		t.Fatalf("toasts: %q", r.toasts)
	}
}

// 设置 › 代理 validates the delay test fields and saves them.
func TestDelaySettingsRows(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if err := tt.Click("代理"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("延迟测试") || !tt.HasText("切换节点时断开该组的连接") {
		t.Fatalf("delay section: %q", tt.Texts())
	}
	p.delayMs.text, p.delayMs.editing = "50", true
	p.commitDelayMs(f.Settings())
	if p.delayMsErr == "" || f.Settings().DelayTimeoutMs != 5000 {
		t.Fatalf("out-of-range timeout must be refused: err=%q ms=%d", p.delayMsErr, f.Settings().DelayTimeoutMs)
	}
	p.delayMs.text = "8000"
	p.commitDelayMs(f.Settings())
	tt.Frame()
	if p.delayMsErr != "" || f.Settings().DelayTimeoutMs != 8000 {
		t.Fatalf("timeout not saved: err=%q ms=%d", p.delayMsErr, f.Settings().DelayTimeoutMs)
	}
	p.delayURL.text = "ftp://x"
	p.commitDelayURL(f.Settings())
	if p.delayURLErr == "" {
		t.Fatal("non-http URL must be refused")
	}
	p.delayURL.text = "https://cp.cloudflare.com/generate_204"
	p.commitDelayURL(f.Settings())
	tt.Frame()
	if f.Settings().DelayURL != "https://cp.cloudflare.com/generate_204" {
		t.Fatalf("URL not saved: %q", f.Settings().DelayURL)
	}
	clickRole(t, tt, ui.RoleSwitch, "切换节点时断开该组的连接")
	tt.Frame()
	if !f.Settings().DisconnectOnSwitch {
		t.Fatal("switch did not save")
	}
}
