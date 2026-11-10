package view

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/subs"
)

func newTest(t *testing.T) (*fake.Fake, *Shell, *ui.Tester) {
	t.Helper()
	api := fake.NewInstant()
	s := New(api, Options{Mac: true, Sync: true})
	t.Cleanup(s.Close)
	s.pages[env.Subs].(*subs.Page).SetAsync(func(fn func()) { fn() }, func() (string, bool) { return "/tmp/test-config.yaml", true })
	tt := ui.NewTester(s.View, DefaultWidth, DefaultHeight)
	return api, s, tt
}

func click(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	if err := tt.Click(text); err != nil {
		t.Fatalf("click %q: %v\ntexts: %q", text, err, tt.Texts())
	}
}

func TestSidebarNavigation(t *testing.T) {
	_, s, tt := newTest(t)
	if s.Page() != env.Overview || !tt.HasText("出口地图") {
		t.Fatalf("starts on %s: %q", s.Page(), tt.Texts())
	}
	click(t, tt, "代理")
	if s.Page() != env.Proxies || !tt.HasText("节点选择") {
		t.Fatalf("after clicking 代理: page %s, texts %q", s.Page(), tt.Texts())
	}
	tt.Key(ui.Cmd, ui.Key3)
	if s.Page() != env.Subs || !tt.HasText("我的机场") {
		t.Fatalf("after ⌘3: page %s", s.Page())
	}
	tt.Key(ui.Cmd, ui.Key8)
	if s.Page() != env.Stats {
		t.Fatalf("after ⌘8: page %s", s.Page())
	}
	tt.Key(ui.Cmd, ui.Key9)
	if s.Page() != env.Probe {
		t.Fatalf("after ⌘9: page %s", s.Page())
	}
	tt.Key(ui.Cmd, ui.KeyComma)
	if s.Page() != env.Settings {
		t.Fatalf("after ⌘,: page %s", s.Page())
	}
	click(t, tt, "设置")
	if s.Page() != env.Settings {
		t.Fatalf("after clicking 设置: page %s", s.Page())
	}
	// The status bar goes back to the overview.
	click(t, tt, "运行中 · 规则")
	if s.Page() != env.Overview {
		t.Fatalf("status bar click: page %s", s.Page())
	}
	// The connection badge shows the count.
	if n := s.api.Status().Connections; n > 0 && !tt.HasText(strconv.Itoa(n)) {
		t.Errorf("no badge for %d connections", n)
	}
}

func TestOverviewSwitches(t *testing.T) {
	api, _, tt := newTest(t)
	if api.Status().Core != model.CoreRunning {
		t.Fatal("fake should start running")
	}
	click(t, tt, "内核开关")
	if st := api.Status(); st.Core != model.CoreStopped || st.SystemProxy {
		t.Fatalf("after turning the core off: %+v", st)
	}
	if !tt.HasText("未启动") || !tt.HasText("内核已停止") {
		t.Errorf("stopped state not shown: %q", tt.Texts())
	}
	// Turning the system proxy on starts the core first.
	click(t, tt, "系统代理开关")
	if st := api.Status(); st.Core != model.CoreRunning || !st.SystemProxy {
		t.Fatalf("after turning the system proxy on: %+v", st)
	}
	if !tt.HasText("系统代理已开启") {
		t.Errorf("toast missing: %q", tt.Texts())
	}
	// The overview switch area has 内核 / 系统代理 / TUN / 模式
	// (docs/design.md §5.1).
	if !tt.HasText("TUN 模式开关") {
		t.Errorf("TUN switch missing on the overview: %q", tt.Texts())
	}
}

func TestModeChange(t *testing.T) {
	api, _, tt := newTest(t)
	click(t, tt, "全局")
	if api.Status().Mode != model.ModeGlobal {
		t.Fatalf("mode %s after clicking 全局", api.Status().Mode)
	}
	if !tt.HasText("运行中 · 全局") {
		t.Errorf("status bar not updated: %q", tt.Texts())
	}
	click(t, tt, "直连")
	if api.Status().Mode != model.ModeDirect {
		t.Fatalf("mode %s after clicking 直连", api.Status().Mode)
	}
	if !tt.HasText("直连模式") {
		t.Errorf("direct-mode note missing on the map list: %q", tt.Texts())
	}
	click(t, tt, "代理")
	if !tt.HasText("当前为直连模式，节点选择不生效。") {
		t.Errorf("direct-mode banner missing on proxies: %q", tt.Texts())
	}
}

func TestProxySelection(t *testing.T) {
	api, s, tt := newTest(t)
	s.navigate(env.Proxies, "")
	tt.Frame()
	click(t, tt, "香港 01")
	if now := api.Proxies().Groups[0].Now; now != "香港 01" {
		t.Fatalf("节点选择 now %q after clicking 香港 01", now)
	}
	if !tt.HasText("已切换到 香港 01") {
		t.Errorf("toast missing: %q", tt.Texts())
	}
	// A URL-test group cannot be picked from: its rows are not buttons.
	if _, ok := tt.AccessByRole(ui.RoleButton, "自动选择"); !ok {
		t.Errorf("the group 自动选择 header should be a disclosure button")
	}
	// Search hides groups without matches.
	click(t, tt, "搜索节点")
	tt.Type("英国")
	if tt.HasText("故障转移") {
		t.Errorf("group without matches still shown: %q", tt.Texts())
	}
	if !tt.HasText("英国 01") {
		t.Errorf("match not shown: %q", tt.Texts())
	}
}

func TestSubscriptionImportDialog(t *testing.T) {
	api, s, tt := newTest(t)
	s.navigate(env.Subs, "import-url")
	tt.Frame()
	if !tt.HasText("从链接导入订阅") {
		t.Fatalf("dialog not open: %q", tt.Texts())
	}
	click(t, tt, "订阅链接")
	tt.Type("ftp://nope")
	click(t, tt, "开始导入")
	if !tt.HasText("请输入以 http:// 或 https:// 开头的订阅链接。") {
		t.Fatalf("validation error missing: %q", tt.Texts())
	}
	if len(api.Subscriptions()) != 3 {
		t.Fatalf("subscription added despite the error")
	}
	// A failing download keeps the dialog open with the error.
	click(t, tt, "订阅链接")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("https://example.com/fail")
	click(t, tt, "开始导入")
	if !tt.HasText("下载失败：HTTP 403。链接可能已失效，请在机场后台重新复制订阅链接") {
		t.Fatalf("download error missing: %q", tt.Texts())
	}
	// A good link imports and closes the dialog.
	click(t, tt, "订阅链接")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("https://example.com/sub")
	click(t, tt, "订阅名称")
	tt.Type("测试机场")
	click(t, tt, "开始导入")
	tt.Advance(time.Second)
	subs := api.Subscriptions()
	if len(subs) != 4 || subs[3].Name != "测试机场" || !subs[3].Current {
		t.Fatalf("import result: %+v", subs)
	}
	if tt.HasText("从链接导入订阅") {
		t.Errorf("dialog still open after a successful import")
	}
	if !tt.HasText("已导入并切换到 测试机场") {
		t.Errorf("toast missing: %q", tt.Texts())
	}
}

func TestSubscriptionDeleteRefusesCurrent(t *testing.T) {
	api, s, tt := newTest(t)
	s.navigate(env.Subs, "")
	tt.Frame()
	page := s.pages[env.Subs].(*subs.Page)
	var current, other string
	for _, sub := range api.Subscriptions() {
		if sub.Current {
			current = sub.ID
		} else if sub.Source == "file" {
			other = sub.ID
		}
	}
	// The row menu disables deleting the current subscription: choosing
	// it does nothing.
	click(t, tt, "更多 我的机场")
	items := tt.Menu()
	if len(items) == 0 {
		t.Fatalf("row menu did not open")
	}
	_ = tt.ChooseMenuItem("删除…")
	tt.Frame()
	if tt.HasText("删除「我的机场」？") {
		t.Errorf("delete confirmation shown for the current subscription")
	}
	tt.CloseMenu()
	// Asked directly, the API refuses and the error is toasted.
	page.AskDelete(current)
	tt.Frame()
	click(t, tt, "删除")
	if !tt.HasText("请先切换到其他订阅") {
		t.Errorf("refusal toast missing: %q", tt.Texts())
	}
	if len(api.Subscriptions()) != 3 {
		t.Fatalf("current subscription deleted")
	}
	// Another one deletes after confirmation.
	page.AskDelete(other)
	tt.Frame()
	if !tt.HasText("删除「本地测试」？") {
		t.Fatalf("confirmation missing: %q", tt.Texts())
	}
	click(t, tt, "删除")
	if len(api.Subscriptions()) != 2 {
		t.Fatalf("subscription not deleted")
	}
}

func TestInvalidConfigNotice(t *testing.T) {
	api, s, tt := newTest(t)
	s.navigate(env.Subs, "")
	tt.Frame()
	click(t, tt, "更多 备用机场")
	if err := tt.ChooseMenuItem("使用此订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("配置无法使用") || !tt.HasText("line 42: proxy-groups[1]: proxy 'xx' not found") {
		t.Fatalf("invalid-config dialog missing: %q", tt.Texts())
	}
	if api.Status().CurrentSubscription != "s1" {
		t.Errorf("current subscription changed")
	}
	click(t, tt, "好")
	if tt.HasText("line 42") {
		t.Errorf("dialog still open")
	}
}

func TestFileDropImports(t *testing.T) {
	api, s, tt := newTest(t)
	s.importFiles([]string{"/tmp/x.txt", "/tmp/my.yaml"})
	tt.Frame()
	subs := api.Subscriptions()
	if len(subs) != 4 || subs[3].Name != "my.yaml" {
		t.Fatalf("dropped file not imported: %+v", subs)
	}
	if s.Page() != env.Subs {
		t.Errorf("page %s after a drop", s.Page())
	}
}

// TestDragRegionsTakeNoClick checks that a press on the page header's
// background (a window drag handle) reaches no control: the tester has
// no window to move, so it must simply change nothing.
func TestDragRegionsTakeNoClick(t *testing.T) {
	_, s, tt := newTest(t)
	// Between the title and the tools of the 概览 header.
	tt.ClickAt(600, 36)
	tt.Frame()
	if s.Page() != env.Overview || s.confirm != nil {
		t.Fatalf("a press on the header changed state: page %s", s.Page())
	}
	if _, ok := tt.Access("标题栏"); ok {
		t.Fatalf("the sidebar drag strip only exists under a hidden title bar")
	}
}

// Error toasts show a short line with 「查看详情」 that opens the full text
// (docs/design.md §6.5).
func TestErrorToastDetail(t *testing.T) {
	_, s, tt := newTest(t)
	long := "下载订阅失败：Get \"https://example.com/sub\": dial tcp: lookup example.com: no such host。请检查链接是否正确，或稍后再试。"
	s.run(func() error { return errors.New(long) }, "")
	tt.Frame()
	if tt.HasText(long) {
		t.Fatalf("the full error is on the toast: %q", tt.Texts())
	}
	if !tt.HasText("查看详情") {
		t.Fatalf("no 查看详情 action: %q", tt.Texts())
	}
	click(t, tt, "查看详情")
	tt.Frame()
	if !tt.HasText("错误详情") || !tt.HasText(long) {
		t.Fatalf("detail dialog missing: %q", tt.Texts())
	}
	click(t, tt, "好")
	if tt.HasText("错误详情") {
		t.Error("detail dialog still open")
	}
}

func TestSplitError(t *testing.T) {
	short, full := splitError("第一行。\n第二行细节")
	if short != "第一行。" || full != "第一行。\n第二行细节" {
		t.Fatalf("split = %q / %q", short, full)
	}
	long := "内核启动失败：无法绑定端口 7890，可能被其他程序占用，也可能是上一个内核进程还没有退出。可在「日志」中查看详情，或在设置中更换端口后重新启动内核。"
	short, _ = splitError(long)
	if len([]rune(short)) > 66 || !strings.HasSuffix(short, "…") || !strings.HasPrefix(short, "内核启动失败") {
		t.Fatalf("long split = %q", short)
	}
	short, _ = splitError("短消息")
	if short != "短消息" {
		t.Fatalf("short split = %q", short)
	}
}

// The tray notice shows once ever: a marker file remembers it across launches.
func TestHiddenToTrayOnceEver(t *testing.T) {
	_, s, _ := newTest(t)
	dir := t.TempDir()
	s.hiddenFlag = filepath.Join(dir, "tray-notice-shown")
	shown := 0
	s.notify = func(title, body string, onClick func()) { shown++ }
	s.HiddenToTray()
	s.HiddenToTray()
	if shown != 1 {
		t.Fatalf("shown %d times in one run", shown)
	}
	if _, err := os.Stat(s.hiddenFlag); err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	// A new launch with the marker present says nothing.
	_, s2, _ := newTest(t)
	s2.hiddenFlag = s.hiddenFlag
	s2.notify = func(string, string, func()) { shown++ }
	s2.HiddenToTray()
	if shown != 1 {
		t.Fatalf("shown again in a later run: %d", shown)
	}
}

// Notices flagged System become system notifications while still toasting.
func TestSystemNoticeNotifies(t *testing.T) {
	api, s, tt := newTest(t)
	var titles []string
	s.notify = func(title, body string, onClick func()) { titles = append(titles, title) }
	api.PushNotice(app.Notice{Kind: "core-crash", Title: "Mihomo 内核意外退出", Detail: "exit status 2", System: true})
	api.PushNotice(app.Notice{Kind: "toast", Title: "订阅已更新"})
	tt.Frame()
	if len(titles) != 1 || titles[0] != "Mihomo 内核意外退出" {
		t.Fatalf("notifications = %q", titles)
	}
	if !tt.HasText("Mihomo 内核意外退出") || !tt.HasText("订阅已更新") {
		t.Fatalf("toasts missing: %q", tt.Texts())
	}
}

func TestParseImportLink(t *testing.T) {
	u, name, ok := ParseImportLink("clash://install-config?url=https%3A%2F%2Fexample.com%2Fsub%3Ftoken%3Dabc&name=%E6%9C%BA%E5%9C%BA")
	if !ok || u != "https://example.com/sub?token=abc" || name != "机场" {
		t.Fatalf("clash link: %q %q %v", u, name, ok)
	}
	u, _, ok = ParseImportLink("lanflow://import?url=http://example.com/a.yaml")
	if !ok || u != "http://example.com/a.yaml" {
		t.Fatalf("lanflow link: %q %v", u, ok)
	}
	for _, bad := range []string{"lanflow://other?url=https://x.com", "clash://install-config?url=ftp://x.com", "https://example.com/sub", "clash://install-config"} {
		if _, _, ok := ParseImportLink(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A deep link opens the subscriptions page with the import arg set and
// imports nothing by itself.
func TestOpenImportLinkNavigates(t *testing.T) {
	api, s, tt := newTest(t)
	before := len(api.Subscriptions())
	s.OpenImportLink("lanflow://import?url=https://example.com/sub&name=X")
	tt.Frame()
	if s.Page() != env.Subs {
		t.Fatalf("page %s", s.Page())
	}
	if len(api.Subscriptions()) != before {
		t.Fatal("deep link imported by itself")
	}
}

// TestUpdateCheckDue checks the launch-time update check runs once a day,
// remembered in the data directory.
func TestUpdateCheckDue(t *testing.T) {
	_, s, _ := newTest(t)
	s.updateStamp = filepath.Join(t.TempDir(), "update-check")
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	if !s.updateCheckDue(now) {
		t.Fatal("first launch must check")
	}
	if s.updateCheckDue(now.Add(time.Hour)) {
		t.Fatal("an hour later must not check again")
	}
	if !s.updateCheckDue(now.Add(25 * time.Hour)) {
		t.Fatal("a day later must check")
	}
	if s.updateCheckDue(now.Add(26 * time.Hour)) {
		t.Fatal("the stamp must move with the last check")
	}
	s.updateStamp = ""
	if !s.updateCheckDue(now) || !s.updateCheckDue(now) {
		t.Fatal("without a stamp file every launch checks")
	}
}

// TestGlobalShortcutWithoutWindow checks the shell remembers the shortcut
// before the app runs, and that the settings page reaches it through Env.
func TestGlobalShortcutWithoutWindow(t *testing.T) {
	api, s, tt := newTest(t)
	if s.Env().SetGlobalShortcut == nil {
		t.Fatal("Env.SetGlobalShortcut not wired")
	}
	if err := s.Env().SetGlobalShortcut("CmdOrCtrl+Alt+P"); err != nil {
		t.Fatal(err)
	}
	if got := s.keys.get(model.ShortcutSystemProxy); got != "CmdOrCtrl+Alt+P" {
		t.Fatalf("hotkey = %q", got)
	}
	click(t, tt, "设置")
	if !tt.HasText("⇧⌘S") {
		t.Fatalf("settings should show the default shortcut: %q", tt.Texts())
	}
	set := api.Settings()
	set.GlobalShortcut = ""
	if err := api.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("未设置") {
		t.Fatalf("cleared shortcut: %q", tt.Texts())
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

// TestFileDropFirstImportStarts: the first subscription ever, dropped as a
// file, is used, starts the core and toasts 「已导入并启动」 (docs/design.md
// §8.1); later drops only toast 「已导入 <file>」.
func TestFileDropFirstImportStarts(t *testing.T) {
	inner := fake.NewInstant()
	if err := inner.StopCore(); err != nil {
		t.Fatal(err)
	}
	api := &firstRun{API: inner}
	s := New(api, Options{Mac: true, Sync: true})
	t.Cleanup(s.Close)
	tt := ui.NewTester(s.View, DefaultWidth, DefaultHeight)
	s.importFiles([]string{"/tmp/first.yaml"})
	tt.Frame()
	if st := api.Status(); st.Core != model.CoreRunning {
		t.Fatalf("core %s after the first import, want running", st.Core)
	}
	if !tt.HasText("已导入并启动") {
		t.Fatalf("toast missing: %q", tt.Texts())
	}
	s.importFiles([]string{"/tmp/second.yaml"})
	tt.Frame()
	if !tt.HasText("已导入 second.yaml") {
		t.Fatalf("second import toast: %q", tt.Texts())
	}
	subs := api.Subscriptions()
	if len(subs) != 5 || subs[3].Name != "first.yaml" || subs[4].Name != "second.yaml" {
		t.Fatalf("subscriptions: %+v", subs)
	}
}

// The recovery prompt goes when the question is gone (answered through
// ctl, or a new service process took the core over), and a reconnection
// to a new service process drops what the old one had asked.
func TestRecoveryPromptFollowsStatus(t *testing.T) {
	api, s, tt := newTest(t)
	api.SetPendingRecovery(true)
	api.PushNotice(app.Notice{Kind: "recovery", Title: "上次澜流未正常退出"})
	tt.Frame()
	if !tt.HasText("恢复为关闭") {
		t.Fatalf("recovery prompt not shown: %q", tt.Texts())
	}
	api.SetPendingRecovery(false)
	tt.Frame()
	tt.Frame()
	if tt.HasText("恢复为关闭") {
		t.Fatal("prompt still shown after the recovery was resolved elsewhere")
	}

	api.SetPendingRecovery(true)
	api.PushNotice(app.Notice{Kind: "recovery"})
	tt.Frame()
	s.ServiceConnection(true)
	api.SetPendingRecovery(false)
	tt.Frame()
	tt.Frame()
	if tt.HasText("恢复为关闭") || s.recovery {
		t.Fatal("prompt of the old service kept after reconnecting")
	}
}
