package settings

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/hotkey"
)

// chooseOption opens the select named label and picks option.
func chooseOption(t *testing.T, tt *ui.Tester, label, option string) {
	t.Helper()
	clickRole(t, tt, ui.RolePopUpButton, label)
	tt.Frame()
	if err := tt.Click(option); err != nil {
		t.Fatalf("option %q: %v (%q)", option, err, tt.Texts())
	}
	tt.Frame()
}

// typeInto focuses the text field named label, replaces its text and submits.
func typeInto(t *testing.T, tt *ui.Tester, label, text string) {
	t.Helper()
	clickRole(t, tt, ui.RoleTextField, label)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(text)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
}

// The shortcut list shows only bound actions, adds one through the select,
// refuses a combination another action uses, registers through the shell
// and keeps the old keys when the system refuses.
func TestShortcutActionsListAndConflict(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	var registered []string
	refuse := false
	hotkey.SetRegistrar(func(action, acc string) error {
		if refuse {
			return errors.New("无法注册快捷键 ⌥⌘Q：可能已被其他应用占用。仍使用原来的快捷键 ⌥⌘T。")
		}
		registered = append(registered, action+"="+acc)
		return nil
	})
	t.Cleanup(func() { hotkey.SetRegistrar(nil) })
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if tt.HasText("开关 TUN 模式") || !tt.HasText("开关系统代理") || !tt.HasText("添加快捷键") {
		t.Fatalf("initial rows: %q", tt.Texts())
	}
	chooseOption(t, tt, "添加快捷键", "开关 TUN 模式")
	if !tt.HasText("开关 TUN 模式") {
		t.Fatalf("TUN row not added: %q", tt.Texts())
	}
	// ⇧⌘S is the system proxy's.
	clickRole(t, tt, ui.RoleButton, "开关 TUN 模式")
	tt.Key(ui.Super|ui.Shift, ui.KeyS)
	tt.Frame()
	if !tt.HasText("⇧⌘S 已用于「开关系统代理」，请换一个组合键。") || f.Settings().ShortcutOf(model.ShortcutTUN) != "" {
		t.Fatalf("conflict not refused: %q %q", f.Settings().ShortcutOf(model.ShortcutTUN), tt.Texts())
	}
	// ⌥⌘T is free: registered, saved, toasted; the proxy keeps its keys.
	tt.Key(ui.Super|ui.Alt, ui.KeyT)
	tt.Frame()
	set := f.Settings()
	if set.ShortcutOf(model.ShortcutTUN) != "CmdOrCtrl+Alt+T" || set.GlobalShortcut != hotkey.Default {
		t.Fatalf("saved: tun=%q proxy=%q", set.ShortcutOf(model.ShortcutTUN), set.GlobalShortcut)
	}
	if len(registered) != 1 || registered[0] != "tun=CmdOrCtrl+Alt+T" {
		t.Fatalf("registered = %q", registered)
	}
	if len(r.toasts) == 0 || !strings.Contains(r.toasts[len(r.toasts)-1], "⌥⌘T") || !strings.Contains(r.toasts[len(r.toasts)-1], "开关 TUN 模式") {
		t.Fatalf("toasts = %q", r.toasts)
	}
	if tt.HasText("⇧⌘S 已用于") {
		t.Fatal("the conflict message stayed")
	}
	// The system refuses the next keys: the error shows, nothing is saved.
	refuse = true
	tt.Key(ui.Super|ui.Alt, ui.KeyQ)
	tt.Frame()
	if f.Settings().ShortcutOf(model.ShortcutTUN) != "CmdOrCtrl+Alt+T" || !tt.HasText("仍使用原来的快捷键 ⌥⌘T。") {
		t.Fatalf("refused keys: %q %q", f.Settings().ShortcutOf(model.ShortcutTUN), tt.Texts())
	}
	refuse = false
	// Clearing unregisters and keeps the row visible.
	tt.SetFocused(false)
	tt.SetFocused(true)
	tt.Frame()
	clickRole(t, tt, ui.RoleButton, "开关 TUN 模式")
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
	if f.Settings().ShortcutOf(model.ShortcutTUN) != "" || registered[len(registered)-1] != "tun=" || !tt.HasText("开关 TUN 模式") {
		t.Fatalf("after clearing: %q %q", f.Settings().ShortcutOf(model.ShortcutTUN), registered)
	}
}

func TestShortcutConflictAndWithShortcut(t *testing.T) {
	set := model.Settings{GlobalShortcut: "CmdOrCtrl+Shift+S"}
	set = set.WithShortcut(model.ShortcutTUN, "Ctrl+Alt+T")
	if hotkey.Conflict(set, model.ShortcutWindow, "shift+cmdorctrl+s") != model.ShortcutSystemProxy {
		t.Fatal("conflict with the proxy not found")
	}
	if hotkey.Conflict(set, model.ShortcutWindow, "Alt+Ctrl+T") != model.ShortcutTUN {
		t.Fatal("conflict with TUN not found")
	}
	if hotkey.Conflict(set, model.ShortcutTUN, "Ctrl+Alt+T") != "" || hotkey.Conflict(set, model.ShortcutWindow, "CmdOrCtrl+W") != "" {
		t.Fatal("false conflict")
	}
	set2 := set.WithShortcut(model.ShortcutTUN, "")
	if set2.ShortcutOf(model.ShortcutTUN) != "" || set.ShortcutOf(model.ShortcutTUN) != "Ctrl+Alt+T" {
		t.Fatal("WithShortcut shared its slice")
	}
	if set.WithShortcut(model.ShortcutSystemProxy, "").GlobalShortcut != "" {
		t.Fatal("the proxy action lives in GlobalShortcut")
	}
	for _, a := range model.ShortcutActions {
		if hotkey.ActionName(a) == a {
			t.Errorf("no name for %s", a)
		}
	}
}

// Wi-Fi rules: 使用当前 Wi-Fi fills the SSID, 添加 saves the rule with the
// chosen subscription, duplicates are refused, 删除 removes the rule and
// 其他网络 sets the default.
func TestWiFiRulesSection(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if err := tt.Click("代理"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("按 Wi-Fi 切换订阅") || !tt.HasText("当前 Wi-Fi：Home-5G") {
		t.Fatalf("section missing: %q", tt.Texts())
	}
	if err := tt.Click("使用当前 Wi-Fi"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.f2.wifiSSID != "Home-5G" {
		t.Fatalf("SSID not filled: %q", p.f2.wifiSSID)
	}
	chooseOption(t, tt, "订阅", "备用机场")
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	set := f.Settings()
	if len(set.SSIDRules) != 1 || set.SSIDRules[0].SSID != "Home-5G" || set.SSIDRules[0].SubscriptionID != "s2" {
		t.Fatalf("rule = %+v", set.SSIDRules)
	}
	if len(r.toasts) == 0 || !strings.Contains(r.toasts[len(r.toasts)-1], "备用机场") {
		t.Fatalf("toasts = %q", r.toasts)
	}
	// The same network again is refused.
	typeInto(t, tt, "Wi-Fi 名称（SSID）", "home-5g")
	if !tt.HasText("Wi-Fi「Home-5G」已有规则。") || len(f.Settings().SSIDRules) != 1 {
		t.Fatalf("duplicate accepted: %q", tt.Texts())
	}
	// Default for other networks.
	chooseOption(t, tt, "其他网络", "我的机场")
	if f.Settings().SSIDDefaultSub != "s1" {
		t.Fatalf("default = %q", f.Settings().SSIDDefaultSub)
	}
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(f.Settings().SSIDRules) != 0 {
		t.Fatalf("rule not deleted: %+v", f.Settings().SSIDRules)
	}
}

// Without an SSID the section explains why and hides 使用当前 Wi-Fi.
func TestWiFiSectionWithoutSSID(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	e.API = noSSID{f}
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if err := tt.Click("代理"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("使用当前 Wi-Fi") || !tt.HasText("当前 Wi-Fi 名称不可用：macOS 需要定位权限才能读取 Wi-Fi 名称（系统设置 › 隐私与安全性 › 定位服务）") {
		t.Fatalf("empty SSID: %q", tt.Texts())
	}
}

type noSSID struct{ app.API }

func (noSSID) NetInfo() model.NetInfo {
	return model.NetInfo{Interface: "Ethernet (en5)", LANIP: "10.0.0.2"}
}

// Download mirror: a preset saves at once, 测速 lists every preset with
// its round trip, 使用 picks one, 直连 clears it.
func TestMirrorSection(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1200)
	if err := tt.Click("内核"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("下载镜像") || !tt.HasText("直连 GitHub") {
		t.Fatalf("section missing: %q", tt.Texts())
	}
	chooseOption(t, tt, "GitHub 镜像", "ghfast.top")
	if f.Settings().DownloadMirror != "https://ghfast.top/" {
		t.Fatalf("mirror = %q", f.Settings().DownloadMirror)
	}
	if len(r.toasts) == 0 || r.toasts[len(r.toasts)-1] != "下载镜像已改为 ghfast.top" {
		t.Fatalf("toasts = %q", r.toasts)
	}
	if err := tt.Click("测速"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("gh-proxy.com") || !tt.HasText("310 ms") || !tt.HasText("超时") {
		t.Fatalf("results: %q", tt.Texts())
	}
	if err := tt.Click("使用"); err != nil { // only gh-proxy.com offers 使用: ghfast is current, the last failed
		t.Fatal(err)
	}
	tt.Frame()
	if f.Settings().DownloadMirror != "https://gh-proxy.com/" {
		t.Fatalf("mirror = %q", f.Settings().DownloadMirror)
	}
	chooseOption(t, tt, "GitHub 镜像", "直连 GitHub")
	if f.Settings().DownloadMirror != "" {
		t.Fatalf("mirror = %q", f.Settings().DownloadMirror)
	}
	// Custom: a bad URL is refused, a good one saved.
	chooseOption(t, tt, "GitHub 镜像", "自定义…")
	typeInto(t, tt, "镜像地址", "mirror.example")
	if !tt.HasText("镜像地址须是以 http:// 或 https:// 开头的完整地址") || f.Settings().DownloadMirror != "" {
		t.Fatalf("bad mirror: %q", tt.Texts())
	}
	typeInto(t, tt, "镜像地址", "https://mirror.example/gh")
	if f.Settings().DownloadMirror != "https://mirror.example/gh/" {
		t.Fatalf("custom mirror = %q", f.Settings().DownloadMirror)
	}
}

// Backup: 配置… edits the server, the password goes through
// SetWebDAVPassword, 立即备份 and 管理备份… list, restore (confirmed) and
// delete (confirmed).
func TestBackupSection(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if !tt.HasText("备份与同步") || !tt.HasText("未配置") || !tt.HasText("还没有备份过") {
		t.Fatalf("section missing: %q", tt.Texts())
	}
	// Backing up without a server fails with the service's message.
	if err := tt.Click("立即备份"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(r.toasts) == 0 || r.toasts[len(r.toasts)-1] != "请先填写 WebDAV 服务器地址。" {
		t.Fatalf("toasts = %q", r.toasts)
	}
	if err := tt.Click("配置…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("未设置；保存在系统钥匙串中") {
		t.Fatalf("dialog: %q", tt.Texts())
	}
	typeInto(t, tt, "服务器地址", "dav.example.com")
	if !tt.HasText("WebDAV 地址须是以 http:// 或 https:// 开头的完整地址") || f.Settings().WebDAV.URL != "" {
		t.Fatalf("bad URL accepted: %q", tt.Texts())
	}
	typeInto(t, tt, "服务器地址", "https://dav.example.com/dav/")
	typeInto(t, tt, "用户名", "alice")
	typeInto(t, tt, "密码", "s3cret")
	typeInto(t, tt, "远程目录", "/backups/")
	chooseOption(t, tt, "保留份数", "5")
	chooseOption(t, tt, "自动备份", "每周")
	w := f.Settings().WebDAV
	if w.URL != "https://dav.example.com/dav/" || w.User != "alice" || w.Dir != "backups" || w.Keep != 5 || w.AutoBackup != model.WebDAVWeekly {
		t.Fatalf("saved = %+v", w)
	}
	if !f.WebDAVInfo().HasPassword || !tt.HasText("保存在系统钥匙串中") || tt.HasText("未设置；") {
		t.Fatalf("password: %+v %q", f.WebDAVInfo(), tt.Texts())
	}
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if r.toasts[len(r.toasts)-1] != "连接成功，远程目录可用" {
		t.Fatalf("toasts = %q", r.toasts)
	}
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("alice @ https://dav.example.com/dav/") {
		t.Fatalf("summary: %q", tt.Texts())
	}
	if err := tt.Click("立即备份"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if r.toasts[len(r.toasts)-1] != "已备份到 WebDAV" || !tt.HasText("上次备份：") {
		t.Fatalf("backup: %q %q", r.toasts, tt.Texts())
	}
	if err := tt.Click("管理备份…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("3 份备份") || !tt.HasText("lanflow-backup-20261108-213000.json") {
		t.Fatalf("list: %q", tt.Texts())
	}
	// Restore the newest (the first 恢复), confirmed.
	info, ok := tt.AccessByRole(ui.RoleButton, "恢复")
	if !ok {
		t.Fatalf("no 恢复 button: %q", tt.Texts())
	}
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	tt.Frame()
	if len(r.confirms) == 0 || !strings.HasPrefix(r.confirms[len(r.confirms)-1], "恢复 ") {
		t.Fatalf("confirms = %q", r.confirms)
	}
	if !strings.HasPrefix(r.toasts[len(r.toasts)-1], "已从 lanflow-backup-") {
		t.Fatalf("restore toast: %q", r.toasts)
	}
	// Delete the newest, confirmed: the list shrinks.
	info, ok = tt.AccessByRole(ui.RoleButton, "删除")
	if !ok {
		t.Fatal("no 删除 button")
	}
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	tt.Frame()
	if !strings.HasPrefix(r.confirms[len(r.confirms)-1], "删除 ") || !tt.HasText("2 份备份") {
		t.Fatalf("delete: %q %q", r.confirms, tt.Texts())
	}
	// A declined confirmation changes nothing.
	r.confirmAccept = false
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	tt.Frame()
	if !tt.HasText("2 份备份") {
		t.Fatalf("deleted without confirmation: %q", tt.Texts())
	}
}
