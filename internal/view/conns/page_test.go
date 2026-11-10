package conns

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/rules/ruleform"
)

type rec struct {
	toasts, confirms, nav []string
}

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

func TestColumnChooser(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if !tt.HasText("进程") || !tt.HasText("Safari") {
		t.Fatalf("process column missing: %q", tt.Texts())
	}
	if err := tt.RightClick("主机"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("进程"); err != nil {
		t.Fatalf("%v (menu %q)", err, tt.Menu())
	}
	if got := f.Settings().ConnHiddenColumns; len(got) != 1 || got[0] != "process" {
		t.Fatalf("hidden columns saved: %v", got)
	}
	tt.Frame()
	if tt.HasText("进程") || tt.HasText("Safari") {
		t.Fatalf("hidden column still shown: %q", tt.Texts())
	}
	if len(p.cols) != len(allColumns)-1 {
		t.Fatalf("%d columns shown", len(p.cols))
	}
	// Hiding a second one keeps the first hidden; 显示全部列 brings them back.
	if err := tt.RightClick("主机"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("时长"); err != nil {
		t.Fatal(err)
	}
	if got := f.Settings().ConnHiddenColumns; len(got) != 2 {
		t.Fatalf("hidden columns saved: %v", got)
	}
	if err := tt.RightClick("主机"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("显示全部列"); err != nil {
		t.Fatal(err)
	}
	if got := f.Settings().ConnHiddenColumns; len(got) != 0 {
		t.Fatalf("hidden columns after 显示全部列: %v", got)
	}
	tt.Frame()
	if !tt.HasText("进程") || !tt.HasText("时长") {
		t.Fatalf("columns not restored: %q", tt.Texts())
	}
	if len(r.toasts) != 0 {
		t.Fatalf("unexpected toasts: %q", r.toasts)
	}
}

func TestNavigationArgFiltersByRoute(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	arg := "流媒体"
	e.Arg = func(env.Page) string { a := arg; arg = ""; return a }
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if p.query != "流媒体" || !tt.HasText("i.ytimg.com:443") || tt.HasText("www.google.com:443") {
		t.Fatalf("query %q texts %q", p.query, tt.Texts())
	}
}

func TestSortByHostAndCloseOne(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if !tt.HasText("14 个活动连接") {
		t.Fatalf("subtitle missing: %q", tt.Texts())
	}
	if err := tt.Click("主机"); err != nil {
		t.Fatal(err)
	}
	if p.sort.Column != "host" || p.sort.Descending {
		t.Fatalf("sort after clicking the host header: %+v", p.sort)
	}
	texts := tt.Texts()
	a, b := indexOf(texts, "api.github.com:443"), indexOf(texts, "www.google.com:443")
	if a < 0 || b < 0 || a > b {
		t.Fatalf("rows not sorted by host: api.github at %d, www.google at %d", a, b)
	}
	if err := tt.Click("主机"); err != nil {
		t.Fatal(err)
	}
	if !p.sort.Descending {
		t.Fatalf("second click should reverse the order: %+v", p.sort)
	}

	// Choosing a row opens the detail panel; 断开此连接 closes it.
	if err := tt.Click("slack.com:443"); err != nil {
		t.Fatal(err)
	}
	if p.sel == "" || !tt.HasText("源地址") || !tt.HasText("规则载荷") {
		t.Fatalf("detail panel missing after choosing a row (sel=%q): %q", p.sel, tt.Texts())
	}
	if err := tt.Click("断开此连接"); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Connections() {
		if c.Host == "slack.com:443" {
			t.Fatal("connection still active after 断开此连接")
		}
	}
	if len(f.ClosedConnections()) != 1 || f.ClosedConnections()[0].Host != "slack.com:443" {
		t.Fatalf("closed list: %+v", f.ClosedConnections())
	}
	if p.sel != "" || tt.HasText("源地址") {
		t.Fatal("detail panel should close with its connection")
	}
	if len(r.toasts) != 0 {
		t.Fatalf("unexpected toasts: %q", r.toasts)
	}

	// The 已关闭 tab shows it.
	if err := tt.Click("已关闭"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("slack.com:443") || tt.HasText("www.google.com:443") {
		t.Fatalf("closed tab shows wrong rows: %q", tt.Texts())
	}
}

func TestAddRuleFromConnection(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if err := tt.Click("rr3---sn-i3b7kn7s.googlevideo.com:443"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("此域名始终走…"); err != nil {
		t.Fatal(err)
	}
	if !p.form.IsOpen() || !tt.HasText("添加规则") {
		t.Fatal("rule dialog did not open")
	}
	if got := p.form.Rule(); got.Type != "DOMAIN-SUFFIX" || got.Payload != "googlevideo.com" || got.Target != "流媒体" {
		t.Fatalf("prefilled rule: %+v", got)
	}
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	rules := f.CustomRules()
	if len(rules) != 3 || rules[0].Payload != "googlevideo.com" || rules[0].Target != "流媒体" {
		t.Fatalf("custom rules after save: %+v", rules)
	}
	if p.form.IsOpen() {
		t.Fatal("dialog should close after saving")
	}
	if len(r.toasts) == 0 || r.toasts[len(r.toasts)-1] != "规则已保存" {
		t.Fatalf("toasts: %q", r.toasts)
	}

	// 此程序始终走… prefills PROCESS-NAME, and is disabled without a process.
	if err := tt.Click("此程序始终走…"); err != nil {
		t.Fatal(err)
	}
	if got := p.form.Rule(); got.Type != "PROCESS-NAME" || got.Payload != "Google Chrome" {
		t.Fatalf("process rule: %+v", got)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("192.168.1.1:80"); err != nil {
		t.Fatal(err)
	}
	_ = tt.Click("此程序始终走…")
	if p.form.IsOpen() {
		t.Fatal("此程序始终走… should be disabled for a connection without a process")
	}
	if err := tt.Click("此域名始终走…"); err != nil {
		t.Fatal(err)
	}
	if got := p.form.Rule(); got.Type != "IP-CIDR" || got.Payload != "192.168.1.1/32" || got.Target != "节点选择" {
		t.Fatalf("IP rule: %+v", got)
	}
}

func TestSearchPauseAndEmptyStates(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if err := tt.Click("搜索连接"); err != nil {
		t.Fatal(err)
	}
	tt.Type("telegram")
	if !tt.HasText("telegram.org:443") || tt.HasText("www.google.com:443") {
		t.Fatalf("search did not filter: %q", tt.Texts())
	}
	tt.Type("zzz")
	if !tt.HasText("没有匹配“telegramzzz”的连接") {
		t.Fatalf("no-match state missing: %q", tt.Texts())
	}
	p.query = ""
	tt.Frame()

	if err := tt.Click("暂停"); err != nil {
		t.Fatal(err)
	}
	if !p.paused || !tt.HasText("继续") {
		t.Fatal("pause did not toggle")
	}
	if err := f.CloseAllConnections(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("www.google.com:443") {
		t.Fatal("paused view should keep the frozen rows")
	}
	if err := tt.Click("继续"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("暂无活动连接") {
		t.Fatalf("empty state missing: %q", tt.Texts())
	}

	if err := f.StopCore(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("内核未启动") {
		t.Fatalf("stopped state missing: %q", tt.Texts())
	}
}

func TestCloseAllAsks(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if err := tt.Click("全部断开"); err != nil {
		t.Fatal(err)
	}
	if len(r.confirms) != 1 || !strings.HasPrefix(r.confirms[0], "断开全部 14 个连接") {
		t.Fatalf("confirm: %q", r.confirms)
	}
	if len(f.Connections()) != 0 || len(f.ClosedConnections()) != 14 {
		t.Fatalf("connections after 全部断开: %d active, %d closed", len(f.Connections()), len(f.ClosedConnections()))
	}
	if len(r.toasts) != 1 || r.toasts[0] != "已断开全部连接" {
		t.Fatalf("toasts: %q", r.toasts)
	}
}

func TestSuffixOf(t *testing.T) {
	cases := map[string]string{
		"www.google.com:443":                    "google.com",
		"rr3---sn-i3b7kn7s.googlevideo.com:443": "googlevideo.com",
		"api.example.co.uk":                     "example.co.uk",
		"a.b.c.com.cn:80":                       "c.com.cn",
		"localhost":                             "localhost",
		"192.168.1.1:80":                        "192.168.1.1",
	}
	for in, want := range cases {
		if got := ruleform.SuffixOf(in); got != want {
			t.Errorf("SuffixOf(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ruleName(model.Connection{Rule: "DomainSuffix"}); got != "DOMAIN-SUFFIX" {
		t.Errorf("ruleName = %q", got)
	}
	if got := ruleName(model.Connection{Rule: "IPCIDR"}); got != "IPCIDR" {
		t.Errorf("ruleName(IPCIDR) = %q", got)
	}
}

func TestTopologyViewAndClickToList(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if err := tt.Click("拓扑"); err != nil {
		t.Fatal(err)
	}
	if p.view != 1 || !tt.HasText("拓扑选项") || tt.HasText("www.google.com:443") {
		t.Fatalf("topology view not shown: view=%d texts %q", p.view, tt.Texts())
	}
	g := p.topo.graph
	if g == nil || len(g.Paths) != 14 {
		t.Fatalf("graph not built: %+v", g)
	}
	if _, ok := tt.Find("网络拓扑"); !ok {
		t.Fatalf("topology box not labeled: %q", tt.Texts())
	}
	tt.Frame()
	if len(p.topo.layout.Boxes) != len(g.Nodes) {
		t.Fatalf("layout not kept after painting: %d boxes for %d nodes", len(p.topo.layout.Boxes), len(g.Nodes))
	}
	// Hovering a node bar lights it; clicking it shows its connections in the list.
	var box Box
	for _, b := range p.topo.layout.Boxes {
		if g.Nodes[b.Node].Label == "流媒体" {
			box = b
		}
	}
	if box.W == 0 {
		t.Fatal("no 流媒体 group node")
	}
	tt.Move(box.X+box.W/2, box.Y+box.H/2)
	tt.Frame()
	if p.topo.hover.node != box.Node {
		t.Fatalf("hover node %d, want %d", p.topo.hover.node, box.Node)
	}
	tt.ClickAt(box.X+box.W/2, box.Y+box.H/2)
	if p.view != 0 || p.query != "流媒体" {
		t.Fatalf("click did not switch to the filtered list: view=%d query=%q", p.view, p.query)
	}
	if !tt.HasText("i.ytimg.com:443") || tt.HasText("www.google.com:443") {
		t.Fatalf("list not filtered: %q", tt.Texts())
	}

	// 含已关闭 takes the closed connections in; 流量 weighs by bytes.
	if err := f.CloseConnection("c1"); err != nil {
		t.Fatal(err)
	}
	p.query = ""
	if err := tt.Click("拓扑"); err != nil {
		t.Fatal(err)
	}
	if len(p.topo.graph.Paths) != 13 {
		t.Fatalf("%d paths after closing one", len(p.topo.graph.Paths))
	}
	// 含已关闭 lives in the 拓扑选项 menu, which keeps the toolbar within
	// the minimum window width.
	if err := tt.Click("拓扑选项"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("含已关闭"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !p.topo.withClosed || len(p.topo.graph.Paths) != 14 {
		t.Fatalf("closed connections not included: %v, %d paths", p.topo.withClosed, len(p.topo.graph.Paths))
	}
	if err := tt.Click("流量"); err != nil {
		t.Fatal(err)
	}
	if p.topo.byTotal != 1 {
		t.Fatal("流量 not chosen")
	}
	// The search narrows the graph, and an empty match shows the empty state.
	p.query = "zzz"
	tt.Frame()
	if !tt.HasText("没有匹配“zzz”的连接") {
		t.Fatalf("empty state missing: %q", tt.Texts())
	}
}

func TestHaystackFindsSourceAndPayload(t *testing.T) {
	cn := model.Connection{Host: "a.com:443", Rule: "DomainSuffix", RulePayload: "a.com", Chains: []string{"G", "N"}, SourceIP: "10.0.0.2", SourcePort: "4000"}
	h := haystack(cn)
	for _, s := range []string{"DOMAIN-SUFFIX a.com", "10.0.0.2:4000", "G › N"} {
		if !strings.Contains(h, s) {
			t.Errorf("haystack %q lacks %q", h, s)
		}
	}
}
