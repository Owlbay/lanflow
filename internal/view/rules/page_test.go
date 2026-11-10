package rules

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/rules/ruleform"
)

type rec struct{ toasts, confirms []string }

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

// clickRole clicks the center of the element assistive technology knows
// by role and name, for controls whose text also shows elsewhere.
func clickRole(t *testing.T, tt *ui.Tester, role ui.Role, name string) {
	t.Helper()
	info, ok := tt.AccessByRole(role, name)
	if !ok {
		t.Fatalf("no %v named %q", role, name)
	}
	b := info.Bounds
	tt.ClickAt(b.X+b.W/2, b.Y+b.H/2)
}

func TestSectionsAndFilter(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	for _, s := range []string{"自定义规则", "openai.com", "域名后缀", "进程名", "订阅规则", "最近命中", "category-ads-all", "规则集", "anti-ad"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q in %q", s, tt.Texts())
		}
	}
	if !tt.HasText("我的机场 · 共 12 条") {
		t.Errorf("subscription note missing: %q", tt.Texts())
	}
	if err := tt.Click("自定义"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("最近命中") || !tt.HasText("openai.com") {
		t.Fatalf("自定义 filter: %q", tt.Texts())
	}
	if err := tt.Click("订阅"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("openai.com") || !tt.HasText("最近命中") {
		t.Fatalf("订阅 filter: %q", tt.Texts())
	}
	if err := tt.Click("全部"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("搜索规则"); err != nil {
		t.Fatal(err)
	}
	tt.Type("steam")
	if !tt.HasText("Steam") || tt.HasText("openai.com") || !tt.HasText("没有匹配“steam”的规则") {
		t.Fatalf("search: %q", tt.Texts())
	}
}

func TestSortAndDisable(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	if err := tt.Click("命中"); err != nil {
		t.Fatal(err)
	}
	if p.sort.Column != "hits" || p.sort.Descending {
		t.Fatalf("sort: %+v", p.sort)
	}
	if err := tt.Click("命中"); err != nil {
		t.Fatal(err)
	}
	if !p.sort.Descending {
		t.Fatalf("second click should sort descending: %+v", p.sort)
	}
	if err := tt.Click("停用"); err != nil {
		t.Fatal(err)
	}
	disabled := 0
	for _, r := range f.Rules() {
		if r.Disabled {
			disabled++
		}
	}
	if disabled != 1 || !tt.HasText("已临时停用") || !tt.HasText("恢复") {
		t.Fatalf("disable: %d disabled, texts %q", disabled, tt.Texts())
	}
	if err := tt.Click("恢复"); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.Rules() {
		if r.Disabled {
			t.Fatal("rule still disabled after 恢复")
		}
	}
}

func TestAddRuleValidation(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	if err := tt.Click("添加规则"); err != nil {
		t.Fatal(err)
	}
	if !p.form.IsOpen() {
		t.Fatal("dialog did not open")
	}
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("请填写规则内容") || !p.form.IsOpen() {
		t.Fatalf("empty payload should be rejected: %q", tt.Texts())
	}
	// IP 段 with a bad value.
	clickRole(t, tt, ui.RolePopUpButton, "类型")
	if err := tt.Click("IP 段"); err != nil {
		t.Fatal(err)
	}
	if p.form.Rule().Type != "IP-CIDR" {
		t.Fatalf("type after select: %+v", p.form.Rule())
	}
	clickRole(t, tt, ui.RoleTextField, "内容")
	tt.Type("10.0.0")
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("IP 段格式不正确") || !p.form.IsOpen() {
		t.Fatalf("bad CIDR should be rejected: %q", tt.Texts())
	}
	clickRole(t, tt, ui.RoleTextField, "内容")
	tt.Key(0, ui.KeyEnd)
	tt.Type(".0/8")
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	if p.form.IsOpen() {
		t.Fatalf("dialog should close after a valid save: %q", p.form.Error())
	}
	rules := f.CustomRules()
	if len(rules) != 3 || rules[0].Type != "IP-CIDR" || rules[0].Payload != "10.0.0.0/8" || rules[0].Target != "节点选择" {
		t.Fatalf("rules after save: %+v", rules)
	}
	if len(r.toasts) == 0 || r.toasts[len(r.toasts)-1] != "规则已保存" {
		t.Fatalf("toasts: %q", r.toasts)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		r    model.CustomRule
		want string
	}{
		{model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: "", Target: "DIRECT"}, "请填写规则内容"},
		{model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: "https://a.com", Target: "DIRECT"}, "域名格式不正确"},
		{model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: "a.com", Target: "DIRECT"}, ""},
		{model.CustomRule{Type: "IP-CIDR", Payload: "1.2.3.4/33", Target: "DIRECT"}, "IP 段格式不正确"},
		{model.CustomRule{Type: "IP-CIDR", Payload: "2001:db8::/32", Target: "DIRECT"}, ""},
		{model.CustomRule{Type: "PROCESS-NAME", Payload: "Steam", Target: "DIRECT"}, ""},
		{model.CustomRule{Type: "DOMAIN-KEYWORD", Payload: "a b", Target: "DIRECT"}, "关键字不能包含空格"},
	}
	for _, c := range cases {
		got := ruleform.Validate(c.r)
		if (c.want == "" && got != "") || (c.want != "" && !strings.HasPrefix(got, c.want)) {
			t.Errorf("Validate(%+v) = %q, want prefix %q", c.r, got, c.want)
		}
	}
}

func TestTestDialogAndMenu(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	if err := tt.Click("测试…"); err != nil {
		t.Fatal(err)
	}
	tt.Type("www.google.com")
	if err := tt.Click("测试"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("DOMAIN-SUFFIX,google.com") || !tt.HasText("走向 节点选择 › 东京 03") || !tt.HasText("规则 #") {
		t.Fatalf("test result: %q", tt.Texts())
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}

	// The ⋯ menu of a custom rule edits and deletes it.
	if err := tt.Click("⋯"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("编辑…"); err != nil {
		t.Fatal(err)
	}
	if !p.form.IsOpen() || p.form.Rule().Payload != "openai.com" || !tt.HasText("编辑规则") {
		t.Fatalf("edit: open=%v rule=%+v", p.form.IsOpen(), p.form.Rule())
	}
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	if len(r.confirms) != 1 || len(f.CustomRules()) != 1 || f.CustomRules()[0].Payload != "Steam" {
		t.Fatalf("delete: confirms %q, rules %+v", r.confirms, f.CustomRules())
	}
}

func TestStoppedCore(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	if err := f.StopCore(); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(p.View, 1000, 800)
	if !tt.HasText("内核未启动") || tt.HasText("订阅规则") {
		t.Fatalf("stopped state: %q", tt.Texts())
	}
	if err := tt.Click("启动内核"); err != nil {
		t.Fatal(err)
	}
	if f.Status().Core != model.CoreRunning || !tt.HasText("订阅规则") {
		t.Fatalf("after 启动内核: %v", f.Status().Core)
	}
}
