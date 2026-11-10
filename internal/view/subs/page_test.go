package subs

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

func testEnv(api app.API) (*env.Env, *[]string) {
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

func TestImportValidationAndSuccess(t *testing.T) {
	api := fake.NewInstant()
	e, toasts := testEnv(api)
	p := New(e)
	p.SetAsync(func(fn func()) { fn() }, nil)
	tt := ui.NewTester(p.View, 800, 600)
	p.imp = importDialog{open: true, use: true, url: "example.com"}
	tt.Frame()
	p.doImport()
	tt.Frame()
	if p.imp.phase != phaseError || !tt.HasText("请输入以 http:// 或 https:// 开头的订阅链接。") {
		t.Fatalf("validation: phase %d texts %q", p.imp.phase, tt.Texts())
	}
	p.imp.url, p.imp.name = "https://example.com/sub", "新机场"
	p.doImport()
	tt.Frame()
	if p.imp.phase != phaseDone || !p.imp.open {
		t.Fatalf("after import: phase %d open %v", p.imp.phase, p.imp.open)
	}
	tt.Advance(time.Second)
	if p.imp.open {
		t.Errorf("dialog still open")
	}
	subs := api.Subscriptions()
	if len(subs) != 4 || subs[3].Name != "新机场" || !subs[3].Current {
		t.Errorf("subs %+v", subs)
	}
	if len(*toasts) == 0 || (*toasts)[len(*toasts)-1] != "已导入并切换到 新机场" {
		t.Errorf("toasts %v", *toasts)
	}
}

func TestDeleteRefusesCurrent(t *testing.T) {
	api := fake.NewInstant()
	e, toasts := testEnv(api)
	p := New(e)
	p.AskDelete("s1")
	if len(api.Subscriptions()) != 3 {
		t.Fatalf("current subscription deleted")
	}
	if len(*toasts) != 1 || (*toasts)[0] != "请先切换到其他订阅" {
		t.Errorf("toasts %v", *toasts)
	}
	p.AskDelete("s3")
	if len(api.Subscriptions()) != 2 {
		t.Fatalf("subscription not deleted")
	}
}

func indexOf(texts []string, s string) int {
	for i, t := range texts {
		if t == s {
			return i
		}
	}
	return -1
}

func TestSortableColumns(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 900, 600)
	if err := tt.Click("名称"); err != nil {
		t.Fatal(err)
	}
	if p.sort.Column != "name" || p.sort.Descending {
		t.Fatalf("sort after clicking 名称: %+v", p.sort)
	}
	texts := tt.Texts()
	a, b, c := indexOf(texts, "备用机场"), indexOf(texts, "我的机场"), indexOf(texts, "本地测试")
	if a < 0 || b < 0 || c < 0 || !(a < b && b < c) {
		t.Fatalf("rows not sorted by name: %d %d %d in %q", a, b, c, texts)
	}
	if err := tt.Click("名称"); err != nil {
		t.Fatal(err)
	}
	if !p.sort.Descending {
		t.Fatalf("second click should reverse: %+v", p.sort)
	}
	texts = tt.Texts()
	if a, c := indexOf(texts, "备用机场"), indexOf(texts, "本地测试"); a < c {
		t.Fatalf("descending order wrong: %q", texts)
	}
	// Unknown values sort last whatever the direction.
	subs := api.Subscriptions()
	for _, desc := range []bool{false, true} {
		rows := sortSubs(subs, ui.SortOrder{Column: "expire", Descending: desc})
		if rows[2].ID != "s3" {
			t.Fatalf("desc=%v: file subscription without expiry should be last: %+v", desc, rows)
		}
		want := "s2"
		if desc {
			want = "s1"
		}
		if rows[0].ID != want {
			t.Fatalf("desc=%v: first row %s, want %s", desc, rows[0].ID, want)
		}
	}
	if rows := sortSubs(subs, ui.SortOrder{Column: "usage"}); rows[0].ID != "s1" || rows[2].ID != "s3" {
		t.Fatalf("usage order: %+v", rows)
	}
	if rows := sortSubs(subs, ui.SortOrder{}); rows[0].ID != "s1" || rows[1].ID != "s2" {
		t.Fatalf("no sort should keep the store order: %+v", rows)
	}
}

// invalidAPI makes every update fail the core's validation.
type invalidAPI struct{ *fake.Fake }

func (a *invalidAPI) UpdateSubscription(id string) error {
	return fmt.Errorf("「x」更新后的%w：bad。仍在使用上次的配置。", app.ErrInvalidConfig)
}

func TestUpdateFileSubscriptionAndViewConfig(t *testing.T) {
	api := fake.NewInstant()
	e, toasts := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 900, 600)
	if err := tt.Click("更多 本地测试"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("更新（重新读取文件）"); err != nil {
		t.Fatalf("%v (menu %q)", err, tt.Menu())
	}
	if len(*toasts) == 0 || (*toasts)[len(*toasts)-1] != "已重新读取「本地测试」" {
		t.Fatalf("toasts %v", *toasts)
	}
	// A link subscription updates as before.
	if err := tt.Click("更多 我的机场"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("更新"); err != nil {
		t.Fatal(err)
	}
	if (*toasts)[len(*toasts)-1] != "已更新「我的机场」" {
		t.Fatalf("toasts %v", *toasts)
	}

	// 查看配置 opens the read-only YAML viewer with a copy button.
	if err := tt.Click("更多 我的机场"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("查看配置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !p.raw.open || p.raw.loading || p.raw.state == nil || !tt.HasText("查看配置 · 我的机场") {
		t.Fatalf("raw dialog: %+v texts %q", p.raw, tt.Texts())
	}
	if err := tt.Click("复制"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tt.Clipboard(), "mixed-port: 7890") {
		t.Fatalf("clipboard %q", tt.Clipboard())
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	if p.raw.open {
		t.Fatal("raw dialog still open")
	}

	// A validation failure is the service's dialog, not a toast.
	e2, toasts2 := testEnv(&invalidAPI{fake.NewInstant()})
	p2 := New(e2)
	p2.update(model.Subscription{ID: "s1", Name: "我的机场", Source: "url"})
	if len(*toasts2) != 0 {
		t.Fatalf("validation failure must not toast: %v", *toasts2)
	}
}

// phaseAPI reports the import phase the service would.
type phaseAPI struct {
	*fake.Fake
	phase string
}

func (a *phaseAPI) Status() model.Status {
	st := a.Fake.Status()
	st.ImportPhase = a.phase
	return st
}

func TestImportShowsValidationStep(t *testing.T) {
	api := &phaseAPI{Fake: fake.NewInstant(), phase: model.ImportDownloading}
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 800, 600)
	p.imp = importDialog{open: true, use: true, url: "https://example.com/sub", phase: phaseLoading}
	tt.Frame()
	if p.imp.step != 0 || !tt.HasText("正在下载…") {
		t.Fatalf("downloading: step %d texts %q", p.imp.step, tt.Texts())
	}
	api.phase = model.ImportValidating
	tt.Frame()
	if p.imp.step != 1 || !tt.HasText("✓ 下载") || !tt.HasText("正在校验…") {
		t.Fatalf("validating: step %d texts %q", p.imp.step, tt.Texts())
	}
}

func TestDeepLinkPrefillsImportDialog(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	arg := env.ImportURLArg("https://example.com/sub?token=x", "机场 X")
	e.Arg = func(env.Page) string { a := arg; arg = ""; return a }
	p := New(e)
	tt := ui.NewTester(p.View, 800, 600)
	if !p.imp.open || p.imp.url != "https://example.com/sub?token=x" || p.imp.name != "机场 X" || p.imp.phase != phaseIdle {
		t.Fatalf("dialog after deep link: %+v", p.imp)
	}
	if len(api.Subscriptions()) != 3 {
		t.Fatal("a deep link must not import by itself")
	}
	if !tt.HasText("从链接导入订阅") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

func TestTableShowsStates(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 900, 600)
	for _, want := range []string{"我的机场", "当前", "32.5 / 200.0 GB", "2027-03-01", "已过期", "更新失败", "文件"} {
		if want == "已过期" {
			continue
		}
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
}

// countingAPI counts UpdateAllSubscriptions calls.
type countingAPI struct {
	*fake.Fake
	updatedAll int
}

func (a *countingAPI) UpdateAllSubscriptions() error { a.updatedAll++; return nil }

// TestRefreshShortcut checks ⌘R updates every subscription (docs/design.md
// §9 主窗口快捷键), and does nothing when there are none.
func TestRefreshShortcut(t *testing.T) {
	api := &countingAPI{Fake: fake.NewInstant()}
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 900, 600)
	tt.Key(ui.Cmd, ui.KeyR)
	tt.Frame()
	if api.updatedAll != 1 {
		t.Fatalf("⌘R should update all once, got %d", api.updatedAll)
	}
	for _, s := range api.Subscriptions() {
		if !s.Current {
			if err := api.DeleteSubscription(s.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyR)
	tt.Frame()
	if api.updatedAll != 2 {
		t.Fatalf("⌘R with one subscription should still update, got %d", api.updatedAll)
	}
}

// firstRun is the fake API before any subscription exists: Status says
// so until a file is imported.
type firstRun struct {
	app.API
	imported bool
}

func (f *firstRun) Status() model.Status {
	st := f.API.Status()
	st.HasSubscriptions = f.imported
	return st
}

func (f *firstRun) ImportSubscriptionFile(path string, use bool) (model.Subscription, error) {
	s, err := f.API.ImportSubscriptionFile(path, use)
	if err == nil {
		f.imported = true
	}
	return s, err
}

// TestImportFileFirstStartsCore: 「从文件导入」 of the first subscription ever
// starts the core and toasts 「已导入并启动」 (docs/design.md §8.1); with a
// subscription already there it toasts 「已导入 <file>」 and leaves the core
// alone.
func TestImportFileFirstStartsCore(t *testing.T) {
	inner := fake.NewInstant()
	if err := inner.StopCore(); err != nil {
		t.Fatal(err)
	}
	api := &firstRun{API: inner}
	e, toasts := testEnv(api)
	p := New(e)
	p.SetAsync(func(fn func()) { fn() }, func() (string, bool) { return "/tmp/first.yaml", true })
	p.importFile()
	if st := api.Status(); st.Core != model.CoreRunning {
		t.Fatalf("core %s after the first import, want running", st.Core)
	}
	if len(*toasts) != 1 || (*toasts)[0] != "已导入并启动" {
		t.Fatalf("toasts %v", *toasts)
	}
	if err := inner.StopCore(); err != nil {
		t.Fatal(err)
	}
	p.SetAsync(nil, func() (string, bool) { return "/tmp/second.yaml", true })
	p.importFile()
	if st := api.Status(); st.Core != model.CoreStopped {
		t.Fatalf("core %s after a later import, want stopped", st.Core)
	}
	if (*toasts)[len(*toasts)-1] != "已导入 second.yaml" {
		t.Fatalf("toasts %v", *toasts)
	}
	if ImportToast("/a/b.yaml", false) != "已导入 b.yaml" || ImportToast("/a/b.yaml", true) != "已导入并启动" {
		t.Error("ImportToast")
	}
}
