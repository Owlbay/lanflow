package proxies

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

func testEnv(api *fake.Fake) (*env.Env, *[]string) {
	var toasts []string
	e := &env.Env{API: api, Mac: true, Kit: env.Basic(),
		Navigate: func(env.Page, string) {},
		Arg:      func(env.Page) string { return "" },
		Toast:    func(msg string, _ bool) { toasts = append(toasts, msg) },
		Confirm:  func(_, _, _ string, _ bool, ok func()) { ok() },
		Redraw:   func() {},
	}
	e.Run = func(job func() error, ok string) {
		if err := job(); err != nil {
			toasts = append(toasts, err.Error())
		} else if ok != "" {
			toasts = append(toasts, ok)
		}
	}
	return e, &toasts
}

func TestFilterAndSort(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	px := api.Proxies()
	g := px.Groups[0]
	p.query = "香港"
	if n := p.filterNodes(g, px); len(n) != 2 {
		t.Errorf("search 香港: %v", n)
	}
	p.query, p.sort = "", sortDelay
	n := p.filterNodes(g, px)
	// The group 自动选择 shows (and sorts by) its fastest node, 东京 03;
	// the untested 英国 01 sorts last.
	if px.Proxies["自动选择"].Delay != px.Proxies["东京 03"].Delay || n[0] != "自动选择" || n[1] != "东京 03" || n[len(n)-1] != "英国 01" {
		t.Errorf("sort by delay: %v", n)
	}
	p.hideTimeout = true
	for _, x := range p.filterNodes(g, px) {
		if px.Proxies[x].Delay < 0 {
			t.Errorf("timeout node %s still shown", x)
		}
	}
}

func TestPickAndEmptyStates(t *testing.T) {
	api := fake.NewInstant()
	e, toasts := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 800, 600)
	if !tt.HasText("节点选择") || tt.HasText("GLOBAL") {
		t.Fatalf("groups: %q", tt.Texts())
	}
	if err := tt.Click("新加坡 02"); err != nil {
		t.Fatal(err)
	}
	if api.Proxies().Groups[0].Now != "新加坡 02" || len(*toasts) == 0 {
		t.Errorf("pick failed: now %q toasts %v", api.Proxies().Groups[0].Now, *toasts)
	}
	_ = api.SetMode(model.ModeGlobal)
	tt.Frame()
	if !tt.HasText("GLOBAL") {
		t.Errorf("GLOBAL missing in global mode")
	}
	_ = api.StopCore()
	tt.Frame()
	if !tt.HasText("内核未启动") || !tt.HasText("启动内核") {
		t.Errorf("stopped state: %q", tt.Texts())
	}
}

// TestOpenStateRemembered checks a group's expanded state is saved through
// the API and restored by a fresh page (docs/design.md §5.2 展开状态).
func TestOpenStateRemembered(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 800, 600)
	px := api.Proxies()
	if !p.isOpen(px.Groups[0], px) || p.isOpen(px.Groups[1], px) {
		t.Fatal("default: only the main group is open")
	}
	// 故障转移 is only a group header (no group lists it as a member).
	if err := tt.Click("故障转移"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !p.open["故障转移"] {
		t.Fatalf("open after click: %v", p.open)
	}
	if saved := api.UIState(); !saved.OpenGroups["故障转移"] || !saved.OpenGroups["节点选择"] {
		t.Fatalf("state not saved: %+v", saved)
	}
	// A fresh page (as after a restart) starts from the saved state.
	_ = api.SaveUIState(model.UIState{OpenGroups: map[string]bool{"节点选择": false, "流媒体": true}, ProxyGrid: true})
	q := New(e)
	ui.NewTester(q.View, 800, 600)
	if q.isOpen(px.Groups[0], px) || !q.isOpen(px.Groups[3], px) || q.view != viewGrid {
		t.Fatalf("restored: open=%v view=%d", q.open, q.view)
	}
}

// TestGridView checks the compact grid shows the same members, picks on
// click and remembers the choice of view.
func TestGridView(t *testing.T) {
	api := fake.NewInstant()
	e, toasts := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 900, 700)
	if !tt.HasText("列表") || !tt.HasText("网格") {
		t.Fatalf("view toggle missing: %q", tt.Texts())
	}
	if err := tt.Click("网格"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.view != viewGrid || !api.UIState().ProxyGrid {
		t.Fatalf("grid not chosen/saved: view=%d ui=%+v", p.view, api.UIState())
	}
	if !tt.HasText("新加坡 02") || !tt.HasText("Trojan") {
		t.Fatalf("grid cells: %q", tt.Texts())
	}
	if err := tt.Click("新加坡 02"); err != nil {
		t.Fatal(err)
	}
	if api.Proxies().Groups[0].Now != "新加坡 02" || len(*toasts) == 0 {
		t.Errorf("pick in grid failed: now %q toasts %v", api.Proxies().Groups[0].Now, *toasts)
	}
	if err := tt.Click("重新测速 香港 01"); err != nil {
		t.Fatal(err)
	}
}

// TestProvidersSection checks the 代理集合 section lists providers and
// runs update / health check.
func TestProvidersSection(t *testing.T) {
	api := fake.NewInstant()
	e, toasts := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 900, 900)
	if !tt.HasText("代理集合") || tt.HasText("机场节点") {
		t.Fatalf("providers section should start collapsed: %q", tt.Texts())
	}
	if err := tt.Click("代理集合"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second) // let the panel finish opening
	tt.Frame()
	if !p.provOpen || !tt.HasText("机场节点") || !tt.HasText("自建节点") {
		t.Fatalf("providers not listed: %q", tt.Texts())
	}
	before := api.ProxyProviders()[0].UpdatedAt
	if err := tt.Click("更新 机场节点"); err != nil {
		t.Fatal(err)
	}
	if got := api.ProxyProviders()[0]; !got.UpdatedAt.After(before) || got.Updating {
		t.Fatalf("update: %+v", got)
	}
	if len(*toasts) == 0 || (*toasts)[len(*toasts)-1] != "已更新「机场节点」" {
		t.Fatalf("toasts: %v", *toasts)
	}
	if err := tt.Click("健康检查 自建节点"); err != nil {
		t.Fatal(err)
	}
	for _, n := range api.ProxyProviders()[1].Proxies {
		if pr := api.Proxies().Proxies[n]; pr.Testing || pr.Delay == 0 {
			t.Fatalf("health check left %s as %+v", n, pr)
		}
	}
	p.query = "香港"
	tt.Frame()
	if tt.HasText("代理集合") {
		t.Fatal("providers section should hide while searching")
	}
}

// TestSearchShortcut checks ⌘F focuses the search field.
func TestSearchShortcut(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 800, 600)
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Frame()
	if !tt.Focused("搜索节点") {
		t.Fatal("⌘F should focus the search field")
	}
}

// countingAPI counts TestAllDelays calls.
type countingAPI struct {
	*fake.Fake
	testedAll int
}

func (a *countingAPI) TestAllDelays() error { a.testedAll++; return nil }

// TestRefreshShortcut checks ⌘R tests every node while the core runs
// (docs/design.md §9 主窗口快捷键).
func TestRefreshShortcut(t *testing.T) {
	api := &countingAPI{Fake: fake.NewInstant()}
	e, _ := testEnv(api.Fake)
	e.API = api
	p := New(e)
	tt := ui.NewTester(p.View, 800, 600)
	tt.Key(ui.Cmd, ui.KeyR)
	tt.Frame()
	if api.testedAll != 1 {
		t.Fatalf("⌘R should test all once, got %d", api.testedAll)
	}
	if err := api.StopCore(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyR)
	tt.Frame()
	if api.testedAll != 1 {
		t.Fatalf("⌘R must do nothing while the core is stopped, got %d", api.testedAll)
	}
}

// TestSearchKeepsGroupsCollapsed: a search no longer opens every group;
// a collapsed group with matches says how many.
func TestSearchKeepsGroupsCollapsed(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 900, 2000)
	tt.Frame()
	px := api.Proxies()
	var closed string
	for _, g := range px.Groups {
		if !p.open[g.Name] && !g.Hidden && g.Name != "GLOBAL" && p.countMatches(g) > 0 {
			closed = g.Name
			break
		}
	}
	if closed == "" {
		t.Skip("every group is open")
	}
	p.query = "香港"
	tt.Frame()
	if p.open[closed] {
		t.Fatalf("searching opened %s", closed)
	}
	found := false
	for _, x := range tt.Texts() {
		found = found || strings.Contains(x, "个匹配")
	}
	if !found {
		t.Fatalf("no match count while searching: %q", tt.Texts())
	}
}

// TestBigGroupVirtual: a large group builds only the rows in view.
func TestBigGroupVirtual(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	g := model.ProxyGroup{Name: "大组", Type: model.GroupSelect}
	for i := 0; i < 300; i++ {
		g.All = append(g.All, fmt.Sprintf("节点 %03d", i))
	}
	g.Now = g.All[0]
	px := model.ProxySnapshot{Groups: []model.ProxyGroup{g}, MainGroup: g.Name}
	p.open[g.Name], p.openInit[g.Name] = true, true
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().Children(func() { p.group(c, g, g.All, len(g.All), px, groupNames(px)) })
	}, 800, 3000)
	tt.Frame()
	n := 0
	for _, x := range tt.Texts() {
		if strings.HasPrefix(x, "节点 ") {
			n++
		}
	}
	if n == 0 || n > 3*bigGroupRows {
		t.Fatalf("built %d of 300 rows", n)
	}
}

// TestDisplayPrefsRemembered: the sort order, 隐藏超时节点 and the 代理集合
// section survive a restart, and an unknown stored order falls back to the
// subscription's order.
func TestDisplayPrefsRemembered(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 900, 700)
	if err := tt.Click("排序"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("按名称"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("排序"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("隐藏超时节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("代理集合"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	u := api.UIState()
	if u.ProxySort != "name" || !u.ProxyHideTimeout || !u.ProxyProvidersOpen {
		t.Fatalf("not saved: %+v", u)
	}

	q := New(e) // as after a restart
	ui.NewTester(q.View, 900, 700)
	if q.sort != sortName || !q.hideTimeout || !q.provOpen {
		t.Fatalf("not restored: sort=%d hide=%v prov=%v", q.sort, q.hideTimeout, q.provOpen)
	}

	_ = api.SaveUIState(model.UIState{ProxySort: "bogus"})
	r := New(e)
	ui.NewTester(r.View, 900, 700)
	if r.sort != sortDefault || r.hideTimeout || r.provOpen {
		t.Fatalf("fallback: sort=%d hide=%v prov=%v", r.sort, r.hideTimeout, r.provOpen)
	}
}
