package stats

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

type rec struct {
	toasts, confirms []string
}

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
		Confirm: func(title, body, okLabel string, danger bool, onOK func()) {
			r.confirms = append(r.confirms, title)
			onOK()
		},
		Redraw: func() {},
	}
	return e, r
}

func indexOf(texts []string, s string) int {
	for i, t := range texts {
		if t == s {
			return i
		}
	}
	return -1
}

// TestPageRendersTotalsChartAndTable: the page over the fake shows the
// five cards, the chart's legend and the device table, sorted biggest
// first with a share for each row.
func TestPageRendersTotalsChartAndTable(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1200, 760)
	tt.Frame()
	for _, s := range []string{"统计", "上传", "下载", "总计", "请求数", "设备数", "今天（按小时）", "本机", "192.168.1.8", "%"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q; texts: %q", s, tt.Texts())
		}
	}
	st := f.TrafficStats(model.StatsToday)
	if !tt.HasText(env.FmtBytes(st.Totals.Download)) || !tt.HasText("3") {
		t.Errorf("totals not shown: %q", tt.Texts())
	}
	texts := tt.Texts()
	if a, b := indexOf(texts, "本机"), indexOf(texts, "192.168.1.8"); a < 0 || b < 0 || a > b {
		t.Errorf("rows should come biggest first: %d %d", a, b)
	}
	// Ascending by name puts 127.0.0.1 (本机) after the LAN addresses.
	p.sort = ui.SortOrder{Column: "name"}
	tt.Frame()
	texts = tt.Texts()
	if a, b := indexOf(texts, "本机"), indexOf(texts, "192.168.1.8"); a < b {
		t.Errorf("name sort not applied: %d %d", a, b)
	}
}

// TestDimensionSwitchAndSearch: the segmented control changes the table's
// dimension, 域名 can show full hosts, and the search filters rows.
func TestDimensionSwitchAndSearch(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1200, 760)
	tt.Frame()
	if err := tt.Click("域名"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("googlevideo.com") || tt.HasText("i.ytimg.com") {
		t.Fatalf("domain dimension: %q", tt.Texts())
	}
	p.hosts = true
	tt.Frame()
	if !tt.HasText("i.ytimg.com") {
		t.Errorf("full hosts not shown: %q", tt.Texts())
	}
	p.hosts = false
	p.query = "git"
	tt.Frame()
	if !tt.HasText("github.com") || tt.HasText("googlevideo.com") {
		t.Errorf("search did not filter: %q", tt.Texts())
	}
	p.query = "nothing-like-this"
	tt.Frame()
	if !tt.HasText("没有匹配“nothing-like-this”的项目") {
		t.Errorf("no empty search state: %q", tt.Texts())
	}
	p.query = ""
	for i, dim := range dims {
		p.dim = i
		tt.Frame()
		if len(tt.Texts()) == 0 {
			t.Errorf("dimension %s rendered nothing", dim)
		}
	}
	if !tt.HasText("Socks5") {
		t.Errorf("inbound dimension: %q", tt.Texts())
	}
	p.dim = indexOf(dimLabels, "地区")
	tt.Frame()
	if !tt.HasText("🇺🇸 美国") || !tt.HasText("未知") {
		t.Errorf("country names: %q", tt.Texts())
	}
}

// TestRangesAndReset: switching the range changes the chart's title and
// the totals; 重置统计 asks first, then empties the page.
func TestRangesAndReset(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1200, 760)
	tt.Frame()
	today := f.TrafficStats(model.StatsToday).Totals.Download
	if err := tt.Click("本月"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.rng != 2 || !tt.HasText("最近 30 天") || f.TrafficStats(model.StatsMonth).Totals.Download == today {
		t.Errorf("month range: rng=%d texts %q", p.rng, tt.Texts())
	}
	if err := tt.Click("全部"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "自 20") {
		t.Errorf("全部 should say since when: %q", tt.Texts())
	}
	if err := tt.Click("重置统计"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(r.confirms) != 1 || r.confirms[0] != "重置统计？" {
		t.Fatalf("no confirmation: %v", r.confirms)
	}
	if len(r.toasts) != 1 || r.toasts[0] != "统计已重置" {
		t.Fatalf("toasts: %v", r.toasts)
	}
	if !tt.HasText("暂无统计数据") || tt.HasText("192.168.1.8") {
		t.Errorf("page not empty after the reset: %q", tt.Texts())
	}
}

// TestEnglish: the page in English shows no Chinese apart from data.
func TestEnglish(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1200, 760)
	tt.Frame()
	if !tt.HasText("Statistics") || !tt.HasText("This Mac") || !tt.HasText("Devices") {
		t.Errorf("English texts missing: %q", tt.Texts())
	}
	p.dim = indexOf(dimLabels, "代理|统计")
	tt.Frame()
	if !tt.HasText("Proxy") {
		t.Errorf("segment label should use the context key: %q", tt.Texts())
	}
	p.dim = indexOf(dimLabels, "地区")
	tt.Frame()
	if !tt.HasText("🇺🇸 US") {
		t.Errorf("English country label: %q", tt.Texts())
	}
}
