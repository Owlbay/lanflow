package view

import (
	"errors"
	"strings"
	"testing"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// hasCJK reports whether s holds a Han character or CJK/fullwidth
// punctuation.
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

// englishChecker fails the test for any rendered text that still holds
// Chinese once the fake's data (names, places) is removed.
type englishChecker struct {
	t    *testing.T
	data []string
	seen map[string]bool
}

func (c *englishChecker) text(where, s string) {
	c.t.Helper()
	clean := s
	for _, d := range c.data {
		if d != "" {
			clean = strings.ReplaceAll(clean, d, "")
		}
	}
	if hasCJK(clean) && !c.seen[s] {
		c.seen[s] = true
		c.t.Errorf("%s: Chinese in English UI: %q", where, s)
	}
}

func (c *englishChecker) frame(where string, tt *ui.Tester) {
	c.t.Helper()
	tt.Frame()
	for _, s := range tt.Texts() {
		c.text(where, s)
	}
}

func (c *englishChecker) menu(where string, items []*mygo.MenuItem) {
	c.t.Helper()
	for _, it := range items {
		c.text(where, it.Label)
		c.text(where, it.Subtitle)
		c.menu(where, it.Submenu)
	}
}

// TestEnglishUIHasNoChinese renders every page, the settings tabs and the
// dialogs the fake backend can show in English and fails on any Chinese
// left in the chrome (docs/design.md §5.8 语言). Names the fake fabricates
// (nodes, groups, subscriptions, places) are data and are ignored.
func TestEnglishUIHasNoChinese(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	api, s, tt := newTest(t)
	// The language selector names Chinese in Chinese, by design.
	c := &englishChecker{t: t, data: append(api.DataStrings(), i18n.Autonym(i18n.ZH)), seen: map[string]bool{}}
	tt.SetTitleBar(ui.TitleBar{Height: 52})

	// Pages, with the core running.
	for _, p := range env.Order {
		s.navigate(p, "")
		c.frame("page "+string(p), tt)
	}
	// Pages with the core stopped (empty states, banners, status bar).
	if err := api.StopCore(); err != nil {
		t.Fatal(err)
	}
	for _, p := range env.Order {
		s.navigate(p, "")
		c.frame("page "+string(p)+" (core stopped)", tt)
	}
	if err := api.StartCore(); err != nil {
		t.Fatal(err)
	}
	// Direct and global mode notes.
	for _, m := range []model.Mode{model.ModeDirect, model.ModeGlobal} {
		if err := api.SetMode(m); err != nil {
			t.Fatal(err)
		}
		for _, p := range []env.Page{env.Overview, env.Proxies, env.Net} {
			s.navigate(p, "")
			c.frame("page "+string(p)+" in "+string(m), tt)
		}
	}
	if err := api.SetMode(model.ModeRule); err != nil {
		t.Fatal(err)
	}
	// Toasts.
	s.ToggleSystemProxy()
	c.frame("system proxy toast", tt)
	s.ToggleSystemProxy()
	c.frame("system proxy toast", tt)

	// Settings tabs and the dialogs behind them.
	for _, tab := range []string{"general", "proxy", "dns", "sniffer", "override", "kernel"} {
		s.navigate(env.Settings, tab)
		c.frame("settings "+tab, tt)
	}
	s.navigate(env.Settings, "override:global")
	c.frame("override editor", tt)
	click(t, tt, i18n.T("取消"))
	s.navigate(env.Settings, "override")
	tt.Frame()
	click(t, tt, i18n.T("查看…"))
	c.frame("runtime config dialog", tt)
	click(t, tt, i18n.T("关闭"))
	s.navigate(env.Settings, "general")
	tt.Frame()
	click(t, tt, i18n.T("导出…"))
	c.frame("export dialog", tt)
	click(t, tt, i18n.T("取消"))
	s.navigate(env.Settings, "proxy")
	tt.Frame()
	if info, ok := tt.AccessByRole(ui.RoleSwitch, i18n.T("TUN 模式（增强模式）")); ok {
		tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
		c.frame("helper install dialog", tt)
		click(t, tt, i18n.T("取消"))
	} else {
		t.Errorf("no TUN switch: %q", tt.Texts())
	}

	// Subscriptions: the import dialog, the row menu, view config, edit.
	s.navigate(env.Subs, "import-url")
	c.frame("import dialog", tt)
	click(t, tt, i18n.T("取消"))
	tt.Frame()
	click(t, tt, i18n.Tf("更多 %s", "备用机场"))
	for _, m := range tt.Menu() {
		c.text("subscription menu", m)
	}
	if err := tt.ChooseMenuItem(i18n.T("查看配置")); err != nil {
		t.Fatal(err)
	}
	c.frame("view config dialog", tt)
	click(t, tt, i18n.T("关闭"))
	tt.Frame()
	click(t, tt, i18n.Tf("更多 %s", "备用机场"))
	if err := tt.ChooseMenuItem(i18n.T("编辑…")); err != nil {
		t.Fatal(err)
	}
	c.frame("edit subscription dialog", tt)
	click(t, tt, i18n.T("取消"))
	tt.Frame()
	click(t, tt, i18n.Tf("更多 %s", "备用机场"))
	if err := tt.ChooseMenuItem(i18n.T("删除…")); err != nil {
		t.Fatal(err)
	}
	c.frame("delete confirmation", tt)
	click(t, tt, i18n.T("取消"))
	tt.Frame()
	click(t, tt, i18n.Tf("更多 %s", "备用机场"))
	if err := tt.ChooseMenuItem(i18n.T("使用此订阅")); err != nil {
		t.Fatal(err)
	}
	c.frame("invalid config dialog", tt)
	click(t, tt, i18n.T("好"))

	// Rules: the add form and the test dialog.
	s.navigate(env.Rules, "")
	tt.Frame()
	click(t, tt, i18n.T("添加规则"))
	c.frame("rule form", tt)
	click(t, tt, i18n.T("取消"))
	tt.Frame()
	click(t, tt, i18n.T("测试…"))
	c.frame("rule test dialog", tt)

	// Connections: the detail panel.
	s.navigate(env.Conns, "")
	tt.Frame()
	click(t, tt, "www.google.com:443")
	c.frame("connection detail", tt)

	// 检测: the leak results with their fix link, and the IP 纯净度 tab.
	s.navigate(env.Probe, "")
	tt.Frame()
	click(t, tt, i18n.T("开始检查"))
	c.frame("leak check results", tt)
	click(t, tt, i18n.T("IP 纯净度"))
	c.frame("probe IP tab", tt)

	// Network: the DNS tab and the add-site dialog on 连通性.
	s.navigate(env.Net, "")
	tt.Frame()
	click(t, tt, "DNS")
	c.frame("net DNS tab", tt)
	click(t, tt, i18n.T("连通性"))
	tt.Frame()
	click(t, tt, i18n.T("＋ 添加网站"))
	c.frame("add site dialog", tt)
	click(t, tt, i18n.T("取消"))
	tt.Frame()

	// Shell dialogs: quit confirmation, error detail, recovery.
	s.AskQuit(func() {})
	c.frame("quit confirmation", tt)
	click(t, tt, i18n.T("取消"))
	s.run(func() error { return errors.New(i18n.T("下载订阅失败：%v")) }, "")
	c.frame("error toast", tt)
	click(t, tt, i18n.T("查看详情"))
	c.frame("error detail", tt)
	click(t, tt, i18n.T("好"))
	s.recovery = true
	c.frame("recovery dialog", tt)
	click(t, tt, i18n.T("恢复为关闭"))
	tt.Frame()

	// Notices and system notifications.
	var notified []string
	s.notify = func(title, body string, _ func()) { notified = append(notified, title, body) }
	s.hiddenOnce, s.hiddenFlag = false, ""
	s.HiddenToTray()
	s.handleNotice(nil, app.Notice{Kind: "core-crash", Detail: "exit status 1"})
	s.systemNotice(app.Notice{Kind: "core-crash", Detail: "exit status 1", System: true})
	for _, n := range notified {
		c.text("notification", n)
	}
	c.frame("core crash toast", tt)

	// The tray menu and status line in every core state.
	tr := &tray{s: s}
	st := api.Status()
	for _, core := range []model.CoreState{model.CoreRunning, model.CoreStarting, model.CoreError, model.CoreMissing, model.CoreStopped} {
		st.Core = core
		c.text("tray status", statusLine(st))
	}
	px := api.Proxies()
	g, ok := mainGroup(px, st)
	c.menu("tray menu", tr.build(st, px, api.Subscriptions(), g, ok, "CmdOrCtrl+Shift+S").Items())

	// Formatting helpers and titles.
	for _, p := range env.Order {
		c.text("title", env.Title(p))
	}
	c.text("core download", env.CoreDownloadText(model.CoreDownload{Active: true, Phase: "下载", Version: "v1", Bytes: 1, Total: 2}))
	c.text("core download", env.CoreDownloadText(model.CoreDownload{Active: true, Phase: "启动"}))
	c.text("delay", env.FmtDelay(-1, false))
	for _, s := range []string{env.RuleTypeName("DOMAIN"), env.TargetName("DIRECT"), env.TargetName("REJECT"), env.ModeName(model.ModeRule)} {
		c.text("format", s)
	}
}

// TestLanguageSettingAppliesAtOnce switches the language through the
// settings and expects the next frame, the tray signature and the window
// title to follow without a restart.
func TestLanguageSettingAppliesAtOnce(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	api, s, tt := newTest(t)
	s.navigate(env.Settings, "general")
	tt.Frame()
	if !tt.HasText("语言") || !tt.HasText("简体中文") {
		t.Fatalf("language row missing: %q", tt.Texts())
	}
	set := api.Settings()
	set.Language = "en"
	if err := api.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if i18n.Lang() != i18n.EN {
		t.Fatalf("language not applied: %s", i18n.Lang())
	}
	if !tt.HasText("Language") || !tt.HasText("English") || tt.HasText("常规") {
		t.Fatalf("settings page not in English: %q", tt.Texts())
	}
	s.navigate(env.Overview, "")
	tt.Frame()
	if !tt.HasText("Overview") || !tt.HasText("Running · Rule") {
		t.Fatalf("overview not in English: %q", tt.Texts())
	}
	// The selector names each language in itself.
	if !tt.HasText("简体中文") {
		t.Skip("selector only on the settings page")
	}
}
