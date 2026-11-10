package probe

import (
	"strings"
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

// The page follows the main group's node, probes it offline on 检测 and
// adds the labelled online sources on 在线检测.
func TestIPTab(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1200, 800)
	clickTab(t, tt, "IP 纯净度")
	for _, s := range []string{"检测", "泄漏检查", "IP 纯净度", "解锁检测", "地区探查", "出口信息 · 东京 03", "在浏览器中检查 IP 纯净度", "尚未检测。按「检测出口」读取这个出口的地址、位置、ASN 和类型。", "风险评估"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q: %q", s, tt.Texts())
		}
	}
	if err := tt.Click("检测出口"); err != nil {
		t.Fatal(err)
	}
	if len(r.toasts) != 0 {
		t.Fatalf("toasts: %q", r.toasts)
	}
	for _, s := range []string{"45.76.211.38", "Vultr AS20473", "机房", "广播", "NRT", "Cloudflare trace · GeoLite2-ASN / GeoIP", "重新检测", "在线检测"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q: %q", s, tt.Texts())
		}
	}
	if tt.HasText("来源 ip-api.com") {
		t.Fatal("no online source before 在线检测")
	}
	if err := tt.Click("在线检测"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"来源 ip-api.com", "来源 ippure.com", "42 / 100", "代理 / VPN", "广播 IP", "ippure.com 判定"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q: %q", s, tt.Texts())
		}
	}
	rep := f.Probe().IP["东京 03"]
	if len(rep.Risk) != 2 {
		t.Fatalf("report = %+v", rep)
	}
}

// 解锁检测 lists every service with its verdict and region.
func TestUnlockTab(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1200, 800)
	clickTab(t, tt, "解锁检测")
	if !tt.HasText("开始检测") {
		t.Fatalf("%q", tt.Texts())
	}
	if err := tt.Click("开始检测"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"ChatGPT", "Netflix", "仅自制剧", "可用", "YouTube Premium", "Bing", "国际版", "重新检测"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q: %q", s, tt.Texts())
		}
	}
	if rep := f.Probe().Unlock["东京 03"]; rep.Running || len(rep.Results) == 0 {
		t.Fatalf("report = %+v", rep)
	}
}

// 地区探查 lists the region's nodes with delay, exit and the 设为当前 action
// that selects the node in the main group.
func TestRegionTab(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1200, 800)
	clickTab(t, tt, "地区探查")
	if !tt.HasText("开始探查") || p.region != "HK" {
		t.Fatalf("region %q: %q", p.region, tt.Texts())
	}
	p.region, p.regionName = "JP", ""
	p.withUnlock = true
	tt.Frame()
	if err := tt.Click("开始探查"); err != nil {
		t.Fatal(err)
	}
	rep := f.Probe().Region
	if rep.Running || rep.Country != "JP" || len(rep.Nodes) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	for _, s := range []string{"东京 03", "东京 04", "45.76.211.38", "当前", "设为当前", "10 / 10 可用"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q: %q", s, tt.Texts())
		}
	}
	if err := tt.Click("设为当前 东京 04"); err != nil {
		t.Fatal(err)
	}
	if now := groupNow(f.Proxies(), "节点选择"); now != "东京 04" {
		t.Fatalf("main group now %q", now)
	}
	if len(r.toasts) == 0 || r.toasts[len(r.toasts)-1] != "已切换到「东京 04」" {
		t.Fatalf("toasts: %q", r.toasts)
	}
}

func groupNow(snap model.ProxySnapshot, group string) string {
	for _, g := range snap.Groups {
		if g.Name == group {
			return g.Now
		}
	}
	return ""
}

func TestSortRows(t *testing.T) {
	rows := []regionRow{
		{RegionNode: model.RegionNode{Node: "b", Delay: -1}},
		{RegionNode: model.RegionNode{Node: "a", Delay: 300}},
		{RegionNode: model.RegionNode{Node: "c", Delay: 0}},
		{RegionNode: model.RegionNode{Node: "d", Delay: 80}},
	}
	sortRows(rows, ui.SortOrder{Column: "delay"})
	if got := rows[0].Node + rows[1].Node + rows[2].Node + rows[3].Node; got != "dacb" {
		t.Errorf("by delay: %s", got)
	}
	sortRows(rows, ui.SortOrder{Column: "node", Descending: true})
	if got := rows[0].Node + rows[3].Node; got != "da" {
		t.Errorf("by node desc: %s", got)
	}
}

// With the core stopped only the direct exit can be probed.
func TestStopped(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	if err := f.StopCore(); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(p.View, 1200, 800)
	clickTab(t, tt, "IP 纯净度")
	if !tt.HasText("出口信息 · 直连（本机）") {
		t.Fatalf("%q", tt.Texts())
	}
	if err := tt.Click("检测出口"); err != nil {
		t.Fatal(err)
	}
	if rep := f.Probe().IP[model.ProbeDirect]; rep.IP != "116.227.38.12" {
		t.Fatalf("direct report = %+v", rep)
	}
}

func clickTab(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	if err := tt.Click(name); err != nil {
		t.Fatal(err)
	}
}

// The header's 出口 shows only on the tabs that probe the chosen exit:
// 泄漏检查 is about this machine and 地区探查 picks its own nodes.
func TestTargetOnlyOnExitTabs(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	tt := ui.NewTester(New(e).View, 1200, 800)
	for _, c := range []struct {
		tab  string
		want bool
	}{{"泄漏检查", false}, {"IP 纯净度", true}, {"解锁检测", true}, {"地区探查", false}, {"泄漏检查", false}} {
		clickTab(t, tt, c.tab)
		if _, got := tt.Find("出口"); got != c.want {
			t.Errorf("%s: 出口 shown = %v, want %v", c.tab, got, c.want)
		}
	}
}

// The chosen tab is remembered (model.UIState.ProbeTab) and restored by a
// new page, as after a restart; 泄漏检查 comes first.
func TestTabPersists(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1200, 800)
	if p.tab != tabLeak || !tt.HasText("开始检查") {
		t.Fatalf("first tab: %d %q", p.tab, tt.Texts())
	}
	clickTab(t, tt, "地区探查")
	if got := f.UIState().ProbeTab; got != "region" {
		t.Fatalf("ProbeTab = %q", got)
	}
	p = New(e)
	tt = ui.NewTester(p.View, 1200, 800)
	if p.tab != tabRegion || !tt.HasText("开始探查") {
		t.Fatalf("region not restored: %d %q", p.tab, tt.Texts())
	}
	clickTab(t, tt, "解锁检测")
	p = New(e)
	ui.NewTester(p.View, 1200, 800)
	if p.tab != tabUnlock {
		t.Fatalf("unlock not restored: %d", p.tab)
	}
	if tabFromKey("") != tabLeak || tabFromKey("bogus") != tabLeak || tabFromKey("ip") != tabIP {
		t.Error("tabFromKey")
	}
}

// 泄漏检查 runs on demand and lists each item with its state and
// explanation.
func TestLeakCheckTab(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1200, 1000)
	if !tt.HasText("开始检查") || tt.HasText("正常") || tt.HasText("在浏览器中检查 IP 纯净度") {
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
	for _, s := range []string{"重新检查", "DNS", "IPv6", "部分", "正常", "仅遵守系统代理的应用由澜流解析域名", "本机没有可用的公网 IPv6",
		"WebRTC", "浏览器的 WebRTC 会暴露真实 IP（116.227.38.12）", "IP 类型", "出口 45.76.211.38 · AS20473 Vultr · 机房 IP",
		"DNS 对比", "系统解析器的结果与内核或公共 DNS 一致"} {
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

// An item that is not fine links to the Settings tab that fixes it: the
// partial DNS item (system proxy without TUN) to 设置 › 代理; the WebRTC
// item (a browser matter) and the items that pass get none.
func TestLeakFixLinks(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	tt := ui.NewTester(New(e).View, 1200, 1000)
	if err := tt.Click("开始检查"); err != nil {
		t.Fatal(err)
	}
	links := 0
	for _, s := range tt.Texts() {
		if strings.HasSuffix(s, "前往设置 › 代理") {
			links++
		}
	}
	if links != 1 || tt.HasText("前往设置 › DNS") || tt.HasText("前往设置 › 高级") {
		t.Fatalf("want one link (DNS), got %d: %q", links, tt.Texts())
	}
	if err := tt.Click("DNS：前往设置 › 代理"); err != nil {
		t.Fatal(err)
	}
	if len(r.nav) != 1 || r.nav[0] != "settings:proxy" {
		t.Fatalf("nav %q", r.nav)
	}
	// With TUN on every item passes: no links.
	if err := f.InstallTUNHelper(); err != nil {
		t.Fatal(err)
	}
	if err := f.SetTUN(true); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("重新检查"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("前往设置") {
		t.Fatalf("links on passing items: %q", tt.Texts())
	}
}

func TestLeakFix(t *testing.T) {
	warn := func(name string) model.LeakItem { return model.LeakItem{Name: name, Status: model.LeakWarn} }
	for _, c := range []struct {
		it   model.LeakItem
		tun  bool
		want string
	}{
		{warn("DNS"), false, "proxy"},
		{model.LeakItem{Name: model.LeakItemDNSCmp, Status: model.LeakFail}, true, "proxy"},
		{warn("IPv6"), false, "proxy"},
		{model.LeakItem{Name: "IPv6", Status: model.LeakFail}, true, "advanced"},
		{warn(model.LeakItemWebRTC), false, ""},
		{warn(model.LeakItemIPType), false, ""},
		{model.LeakItem{Name: "DNS", Status: model.LeakOK}, false, ""},
		{model.LeakItem{Name: "DNS", Status: model.LeakSkipped}, false, ""},
	} {
		if got := leakFix(c.it, c.tun); got != c.want {
			t.Errorf("leakFix(%s %v, tun=%v) = %q, want %q", c.it.Name, c.it.Status, c.tun, got, c.want)
		}
	}
}
