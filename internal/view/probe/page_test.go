package probe

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

type rec struct{ toasts []string }

func testEnv(f *fake.Fake) (*env.Env, *rec) {
	r := &rec{}
	e := &env.Env{API: f, Kit: env.Basic(), Mac: true,
		Navigate: func(env.Page, string) {},
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
	for _, s := range []string{"检测", "IP 纯净度", "解锁检测", "地区探查", "出口信息 · 东京 03", "尚未检测。按「检测出口」读取这个出口的地址、位置、ASN 和类型。", "风险评估"} {
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
	p.tab = tabUnlock
	tt := ui.NewTester(p.View, 1200, 800)
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
	p.tab = tabRegion
	tt := ui.NewTester(p.View, 1200, 800)
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
