package settings

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/view/env"
)

type rec struct {
	toasts, confirms []string
	arg              string
	confirmAccept    bool
}

// testKit is the basic kit with row descriptions shown, as the shell's kit shows them.
type testKit struct{ env.Kit }

func (k testKit) Row(c *ui.Context, label, desc string, control func()) {
	ui.Row(c).Gap(10).Children(func() {
		ui.Column(c).Children(func() {
			ui.Text(c, label)
			if desc != "" {
				ui.Text(c, desc).FontSize(11)
			}
		})
		ui.Spacer(c)
		if control != nil {
			control()
		}
	})
}

func testEnv(f *fake.Fake) (*env.Env, *rec) {
	r := &rec{confirmAccept: true}
	e := &env.Env{API: f, Kit: testKit{env.Basic()}, Mac: true,
		Navigate: func(env.Page, string) {},
		Arg:      func(env.Page) string { a := r.arg; r.arg = ""; return a },
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
			if r.confirmAccept {
				onOK()
			}
		},
		Redraw: func() {},
	}
	return e, r
}

// clickRole clicks the center of the element assistive technology knows
// by role and name, for controls whose label text is also shown as text.
func clickRole(t *testing.T, tt *ui.Tester, role ui.Role, name string) {
	t.Helper()
	info, ok := tt.AccessByRole(role, name)
	if !ok {
		t.Fatalf("no %v named %q", role, name)
	}
	b := info.Bounds
	tt.ClickAt(b.X+b.W/2, b.Y+b.H/2)
}

func TestGeneralSwitchesApplyAtOnce(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	if !tt.HasText("开机时启动") || !tt.HasText("澜流 Lanflow") {
		t.Fatalf("general tab: %q", tt.Texts())
	}
	clickRole(t, tt, ui.RoleSwitch, "开机时启动")
	if !f.Settings().OpenAtLogin {
		t.Fatal("OpenAtLogin not saved")
	}
	clickRole(t, tt, ui.RoleSwitch, "开机时启动")
	if f.Settings().OpenAtLogin {
		t.Fatal("OpenAtLogin not cleared")
	}
	if len(r.toasts) != 0 {
		t.Fatalf("unexpected toasts: %q", r.toasts)
	}
}

func TestPortErrorKeepsOldValue(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	if err := tt.Click("代理"); err != nil {
		t.Fatal(err)
	}
	clickRole(t, tt, ui.RoleTextField, "代理端口")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("7891")
	tt.Key(0, ui.KeyEnter)
	if !tt.HasText("端口 7891 已被其他程序占用") {
		t.Fatalf("port error missing: %q", tt.Texts())
	}
	if f.Settings().MixedPort != 7890 {
		t.Fatalf("port changed to %d", f.Settings().MixedPort)
	}
	if p.port.text != "7891" {
		t.Fatalf("the tried value should stay in the field, got %q", p.port.text)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("70000")
	tt.Key(0, ui.KeyEnter)
	if !tt.HasText("端口须为 1–65535 之间的数字") {
		t.Fatalf("range error missing: %q", tt.Texts())
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("7895")
	tt.Key(0, ui.KeyEnter)
	if f.Settings().MixedPort != 7895 || p.portErr != "" {
		t.Fatalf("port not saved: %d, err %q", f.Settings().MixedPort, p.portErr)
	}
	tt.Frame()
	if len(r.toasts) == 0 || r.toasts[len(r.toasts)-1] != "代理端口已改为 7895" {
		t.Fatalf("toasts: %q", r.toasts)
	}
}

func TestTUNHelperFlow(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	if err := tt.Click("代理"); err != nil {
		t.Fatal(err)
	}
	clickRole(t, tt, ui.RoleSwitch, "TUN 模式（增强模式）")
	if len(r.confirms) != 1 || r.confirms[0] != "安装权限组件" {
		t.Fatalf("confirms: %q", r.confirms)
	}
	if !f.TUNHelperInstalled() || !f.Status().TUN {
		t.Fatalf("helper installed=%v TUN=%v", f.TUNHelperInstalled(), f.Status().TUN)
	}
	if len(r.toasts) == 0 || !strings.Contains(r.toasts[len(r.toasts)-1], "TUN 模式已开启") {
		t.Fatalf("toasts: %q", r.toasts)
	}
	clickRole(t, tt, ui.RoleSwitch, "TUN 模式（增强模式）")
	if f.Status().TUN {
		t.Fatal("TUN should be off")
	}
}

func TestDNSFollowTakeOverApply(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if err := tt.Click("DNS"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("正在使用订阅「我的机场」中的 DNS 设置") || tt.HasText("有未应用的更改") {
		t.Fatalf("follow state: %q", tt.Texts())
	}
	if err := tt.Click("由澜流接管"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("正在使用订阅") || !tt.HasText("使用推荐设置") || !tt.HasText("有未应用的更改") {
		t.Fatalf("take-over state: %q", tt.Texts())
	}
	if err := tt.Click("使用推荐设置"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.dns.ns, "alidns") || len(p.dns.policy) != 2 {
		t.Fatalf("recommended settings not filled: %+v", p.dns)
	}
	if err := tt.Click("应用"); err != nil {
		t.Fatal(err)
	}
	d := f.Settings().DNS
	if !d.TakeOver || len(d.Policy) != 2 || !d.RespectRules || d.EnhancedMode != "fake-ip" {
		t.Fatalf("applied DNS: %+v", d)
	}
	tt.Frame()
	if tt.HasText("有未应用的更改") || p.dnsDirty() {
		t.Fatal("apply bar should go after applying")
	}
	if len(r.toasts) == 0 || r.toasts[len(r.toasts)-1] != "DNS 设置已应用" {
		t.Fatalf("toasts: %q", r.toasts)
	}

	// 放弃 reverts edits.
	if err := tt.Click("跟随订阅"); err != nil {
		t.Fatal(err)
	}
	if !p.dnsDirty() {
		t.Fatal("switching back should be a pending change")
	}
	if err := tt.Click("放弃"); err != nil {
		t.Fatal(err)
	}
	if p.dnsDirty() || !p.dns.takeOver {
		t.Fatal("放弃 should restore the applied settings")
	}
}

func TestSwitchingTabsWithUnappliedChangesAsks(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if err := tt.Click("嗅探"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("由澜流接管"); err != nil {
		t.Fatal(err)
	}
	if !p.snfDirty() || !tt.HasText("有未应用的更改") {
		t.Fatal("sniffer form should be dirty")
	}
	r.confirmAccept = false
	if err := tt.Click("常规"); err != nil {
		t.Fatal(err)
	}
	if len(r.confirms) != 1 || r.confirms[0] != "放弃未应用的更改？" || p.Tab() != "sniffer" {
		t.Fatalf("confirms %q, tab %q", r.confirms, p.Tab())
	}
	r.confirmAccept = true
	if err := tt.Click("常规"); err != nil {
		t.Fatal(err)
	}
	if p.Tab() != "general" || p.snfDirty() {
		t.Fatalf("after accepting: tab %q dirty %v", p.Tab(), p.snfDirty())
	}
	// Applying the sniffer form works too.
	if err := tt.Click("嗅探"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("由澜流接管"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("应用"); err != nil {
		t.Fatal(err)
	}
	if s := f.Settings().Sniffer; !s.TakeOver || !s.Enable || s.HTTPPorts != "80, 8080-8880" {
		t.Fatalf("applied sniffer: %+v", s)
	}
}

func TestOverrideEditorSaveError(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if err := tt.Click("覆写"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("3 行有效内容") || !tt.HasText("我的机场") || !tt.HasText("运行时配置") {
		t.Fatalf("override tab: %q", tt.Texts())
	}
	if err := tt.Click("编辑…"); err != nil {
		t.Fatal(err)
	}
	if !p.ov.open || p.ov.key != "global" || !tt.HasText("编辑全局覆写") {
		t.Fatalf("editor: open=%v key=%q", p.ov.open, p.ov.key)
	}
	p.SetOverrideText("rules:\n\t- DOMAIN,a.com,DIRECT\n")
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	if !p.ov.open || !tt.HasText("YAML 不允许使用 Tab 缩进") {
		t.Fatalf("tab error: open=%v texts=%q", p.ov.open, tt.Texts())
	}
	if f.Overrides().Global == "rules:\n\t- DOMAIN,a.com,DIRECT\n" {
		t.Fatal("a rejected override must not be saved")
	}
	// 预览差异 shows the preview, then a valid save closes the dialog.
	if err := tt.Click("预览差异"); err != nil {
		t.Fatal(err)
	}
	if !p.ov.showDiff || p.ov.diff == nil || !tt.HasText("返回编辑") {
		t.Fatal("diff view did not show")
	}
	if err := tt.Click("返回编辑"); err != nil {
		t.Fatal(err)
	}
	p.SetOverrideText("log-level: warning\n")
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	if p.ov.open || f.Overrides().Global != "log-level: warning\n" {
		t.Fatalf("save: open=%v global=%q", p.ov.open, f.Overrides().Global)
	}
	tt.Frame()
	if len(r.toasts) == 0 || !strings.HasPrefix(r.toasts[len(r.toasts)-1], "覆写已保存") {
		t.Fatalf("toasts: %q", r.toasts)
	}
	if !tt.HasText("1 行有效内容") {
		t.Fatalf("line count not updated: %q", tt.Texts())
	}

	// 运行时配置 lists lines with their sources.
	if err := tt.Click("查看…"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("mixed-port: 7890") || !tt.HasText("自定义规则") || !tt.HasText("全局覆写") {
		t.Fatalf("runtime config: %q", tt.Texts())
	}
	if err := tt.Click("复制"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tt.Clipboard(), "mixed-port: 7890\nmode: rule\n") {
		t.Fatalf("clipboard: %q", tt.Clipboard())
	}
}

func TestNavigationArgOpensOverride(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	r.arg = "override:s2"
	tt := ui.NewTester(p.View, 1000, 900)
	if p.Tab() != "override" || !p.ov.open || p.ov.key != "s2" || !tt.HasText("编辑覆写 · 备用机场") {
		t.Fatalf("arg handling: tab=%q open=%v key=%q", p.Tab(), p.ov.open, p.ov.key)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	r.arg = "kernel"
	tt.Frame()
	if p.Tab() != "kernel" || !tt.HasText("Mihomo 版本") || !tt.HasText("外部资源") || !tt.HasText("v1.19.33") {
		t.Fatalf("kernel tab: %q", tt.Texts())
	}
	if err := tt.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// the fake's stable channel has v1.19.34 while v1.19.33 runs
	if len(r.toasts) == 0 || !strings.HasPrefix(r.toasts[len(r.toasts)-1], "有新版本 v1.19.34") {
		t.Fatalf("toasts: %q", r.toasts)
	}
	tt.Frame()
	if !tt.HasText("更新到 v1.19.34") {
		t.Fatalf("update button missing: %q", tt.Texts())
	}
	if err := tt.Click("DAT"); err != nil {
		t.Fatal(err)
	}
	if f.Settings().Geo.Mode != "dat" {
		t.Fatalf("geo mode: %q", f.Settings().Geo.Mode)
	}
}

// TestGlobalShortcutRecorder checks the 全局快捷键 row: the recorder takes
// the next combination, 恢复默认 and 清除 work, and a shortcut the system
// refuses leaves the old one in place with the error under the row.
func TestGlobalShortcutRecorder(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	var registered []string
	refuse := false
	e.SetGlobalShortcut = func(acc string) error {
		if refuse {
			return errors.New("无法注册快捷键 ⌥⌘P：可能已被其他应用占用。仍使用原来的快捷键 ⇧⌘S。")
		}
		registered = append(registered, acc)
		return nil
	}
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if !tt.HasText("⇧⌘S") {
		t.Fatalf("default shortcut not shown: %q", tt.Texts())
	}
	// Record ⌥⌘P.
	clickRole(t, tt, ui.RoleButton, "开关系统代理") // the system-proxy row of the shortcut list
	tt.Frame()
	if !tt.HasText("按下组合键…") {
		t.Fatalf("recorder not focused: %q", tt.Texts())
	}
	tt.Key(ui.Super|ui.Alt, ui.KeyP)
	tt.Frame()
	if got := f.Settings().GlobalShortcut; got != "CmdOrCtrl+Alt+P" {
		t.Fatalf("saved shortcut = %q", got)
	}
	if len(registered) != 1 || registered[0] != "CmdOrCtrl+Alt+P" {
		t.Fatalf("registered = %q", registered)
	}
	if len(r.toasts) == 0 || !strings.Contains(r.toasts[len(r.toasts)-1], "⌥⌘P") {
		t.Fatalf("toasts = %q", r.toasts)
	}
	// Shift alone is refused before anything is registered.
	tt.Key(ui.Shift, ui.KeyQ)
	tt.Frame()
	if !tt.HasText("快捷键至少要包含 ⌘ / Ctrl / Alt 之一") || f.Settings().GlobalShortcut != "CmdOrCtrl+Alt+P" {
		t.Fatalf("shift-only: %q %q", f.Settings().GlobalShortcut, tt.Texts())
	}
	// The system refuses the keys: the old shortcut stays, the error shows.
	refuse = true
	tt.Key(ui.Super|ui.Shift, ui.KeyQ)
	tt.Frame()
	if f.Settings().GlobalShortcut != "CmdOrCtrl+Alt+P" {
		t.Fatalf("refused shortcut was saved: %q", f.Settings().GlobalShortcut)
	}
	if !tt.HasText("无法注册快捷键 ⌥⌘P：可能已被其他应用占用。仍使用原来的快捷键 ⇧⌘S。") {
		t.Fatalf("error not shown: %q", tt.Texts())
	}
	refuse = false
	// 恢复默认, then 清除.
	tt.SetFocused(false)
	tt.SetFocused(true)
	tt.Frame()
	if err := tt.Click("恢复默认"); err != nil {
		t.Fatalf("恢复默认: %v (%q)", err, tt.Texts())
	}
	tt.Frame()
	if f.Settings().GlobalShortcut != "CmdOrCtrl+Shift+S" || tt.HasText("无法注册快捷键") {
		t.Fatalf("after 恢复默认: %q %q", f.Settings().GlobalShortcut, tt.Texts())
	}
	if err := tt.Click("清除"); err != nil {
		t.Fatalf("清除: %v", err)
	}
	tt.Frame()
	if f.Settings().GlobalShortcut != "" || !tt.HasText("未设置") || tt.HasText("清除") {
		t.Fatalf("after 清除: %q %q", f.Settings().GlobalShortcut, tt.Texts())
	}
	if registered[len(registered)-1] != "" {
		t.Fatalf("clearing must unregister: %q", registered)
	}
}

type fakeUpdater struct {
	enabled bool
	up      *fakeUpdate // nil = up to date
	err     error
	checks  int
}

func (u *fakeUpdater) Enabled() bool { return u.enabled }
func (u *fakeUpdater) Check(context.Context) (Update, error) {
	u.checks++
	if u.err != nil {
		return nil, u.err
	}
	if u.up == nil {
		return nil, nil
	}
	return u.up, nil
}

type fakeUpdate struct {
	version    string
	installErr error
	installs   int
}

func (u *fakeUpdate) Version() string { return u.version }
func (u *fakeUpdate) Install(_ context.Context, progress func(done, total int64)) error {
	u.installs++
	progress(50, 100)
	progress(100, 100)
	return u.installErr
}

// TestAppUpdateDisabledState checks a build without an update source shows
// a calm disabled row instead of errors.
func TestAppUpdateDisabledState(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	p.SetUpdater(&fakeUpdater{enabled: false}, func() { t.Fatal("relaunched") })
	tt := ui.NewTester(p.View, 1000, 900)
	if !tt.HasText("此版本未配置更新源") || !tt.HasText("检查更新") {
		t.Fatalf("disabled state missing: %q", tt.Texts())
	}
	if tt.HasText("自动检查更新") {
		t.Fatal("the auto-check switch is pointless without a source")
	}
	if err := tt.Click("检查更新"); err == nil && len(r.toasts) > 0 {
		t.Fatalf("a disabled button must do nothing: %q", r.toasts)
	}
	if p.UpdatesEnabled() {
		t.Fatal("UpdatesEnabled should follow the updater")
	}
}

// TestAppUpdateFlow checks 检查更新 → 下载并重启 with progress and relaunch,
// the up-to-date and failure outcomes, and the background check.
func TestAppUpdateFlow(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	fu := &fakeUpdater{enabled: true}
	relaunched := 0
	p.SetUpdater(fu, func() { relaunched++ })
	tt := ui.NewTester(p.View, 1000, 1200) // tall enough for 关于 below 配置同步 and 备份与同步
	if !tt.HasText("每天启动时自动检查一次") || !tt.HasText("自动检查更新") {
		t.Fatalf("enabled state missing: %q", tt.Texts())
	}

	// Up to date.
	if err := tt.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if fu.checks != 1 || len(r.toasts) != 1 || r.toasts[0] != "已是最新版本" {
		t.Fatalf("up to date: checks=%d toasts=%q", fu.checks, r.toasts)
	}

	// Failure: the error is a toast, nothing else changes.
	fu.err = errors.New("dial tcp: timeout")
	if err := tt.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(r.toasts) != 2 || !strings.Contains(r.toasts[1], "检查更新失败") || !strings.Contains(r.toasts[1], "可稍后再试") {
		t.Fatalf("failure toasts = %q", r.toasts)
	}
	fu.err = nil

	// A newer version: 下载并重启 installs with progress and relaunches.
	up := &fakeUpdate{version: "0.2.0"}
	fu.up = up
	if err := tt.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("有新版本 0.2.0，当前为 "+AppVersion()) || !tt.HasText("下载并重启") {
		t.Fatalf("found state missing: %q", tt.Texts())
	}
	if err := tt.Click("下载并重启"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if up.installs != 1 || relaunched != 1 {
		t.Fatalf("installs=%d relaunched=%d", up.installs, relaunched)
	}
	if !tt.HasText("新版本已下载，重新打开澜流后生效") || !tt.HasText("重新启动") {
		t.Fatalf("installed state missing: %q", tt.Texts())
	}
	if err := tt.Click("重新启动"); err != nil {
		t.Fatal(err)
	}
	if relaunched != 2 {
		t.Fatalf("relaunched = %d", relaunched)
	}

	// Install failure keeps the app and says so.
	p2 := New(e)
	bad := &fakeUpdate{version: "0.3.0", installErr: errors.New("signature mismatch")}
	p2.SetUpdater(&fakeUpdater{enabled: true, up: bad}, func() { t.Fatal("relaunched after a failed install") })
	tt2 := ui.NewTester(p2.View, 1000, 900)
	if err := tt2.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	tt2.Frame()
	if err := tt2.Click("下载并重启"); err != nil {
		t.Fatal(err)
	}
	tt2.Frame()
	if !tt2.HasText("更新失败：signature mismatch。当前版本不受影响。") || !tt2.HasText("下载并重启") {
		t.Fatalf("install failure: %q", tt2.Texts())
	}

	// A quiet check only reports a found version through the callback.
	p3 := New(e)
	p3.SetUpdater(&fakeUpdater{enabled: true, up: &fakeUpdate{version: "0.4.0"}}, nil)
	before := len(r.toasts)
	var found string
	p3.CheckAppUpdate(true, func(v string) { found = v })
	if found != "0.4.0" || len(r.toasts) != before {
		t.Fatalf("quiet check: found=%q toasts=%q", found, r.toasts[before:])
	}
	p4 := New(e)
	p4.SetUpdater(&fakeUpdater{enabled: true, err: errors.New("offline")}, nil)
	p4.CheckAppUpdate(true, func(string) { t.Fatal("found on error") })
	if len(r.toasts) != before {
		t.Fatalf("quiet failure must stay quiet: %q", r.toasts[before:])
	}

	// The auto-check switch is stored inverted.
	clickRole(t, tt, ui.RoleSwitch, "自动检查更新")
	tt.Frame()
	if !f.Settings().DisableUpdateCheck {
		t.Fatal("the switch must store DisableUpdateCheck")
	}
	// An idle page explains the choice.
	p5 := New(e)
	p5.SetUpdater(&fakeUpdater{enabled: true}, nil)
	tt5 := ui.NewTester(p5.View, 1000, 900)
	if !tt5.HasText("不会自动检查，可随时手动检查") {
		t.Fatalf("auto check off: %q", tt5.Texts())
	}
}
