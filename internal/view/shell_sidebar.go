// The sidebar: the page groups, the status bar under them and the page
// shortcuts.

package view

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// sidebarGroups are the sidebar's groups of destinations (docs/design.md
// §4): daily use, what the core is doing, and diagnosis; 设置 sits apart
// at the bottom.
var sidebarGroups = [][]env.Page{
	{env.Overview, env.Proxies, env.Subs},
	{env.Conns, env.Logs, env.Rules},
	{env.Net, env.Stats, env.Probe},
}

// sidebar draws the groups of destinations.
func (s *Shell) sidebar(c *ui.Context, st model.Status, vib bool) {
	t := c.Theme()
	page := string(s.page)
	item := func(p env.Page) ui.Element {
		e := ui.SidebarItem(c, string(p), icons[pageIcons[string(p)]], env.Title(p))
		if p == env.Conns && st.Connections > 0 && st.Core == model.CoreRunning {
			e.Children(func() {
				ui.Badge(c, fmt.Sprint(st.Connections)).FontFeatures("tnum")
			})
		}
		return e
	}
	divider := func() {
		ui.Box(c).Height(1).Background(t.Border).Margin(6, 8).Shrink(0)
	}
	main := ui.Sidebar(c, &page, func() {
		for i, group := range sidebarGroups {
			if i > 0 {
				divider()
			}
			for _, p := range group {
				item(p)
			}
		}
	}).Grow(1).Label(i18n.T("页面"))
	if vib {
		main.Background(ui.Transparent)
	}
	if main.Changed() {
		s.navigate(env.Page(page), "")
	}
	divider()
	bottom := ui.Sidebar(c, &page, func() { item(env.Settings) }).Shrink(0).Label(i18n.T("更多"))
	if vib {
		bottom.Background(ui.Transparent)
	}
	if bottom.Changed() {
		s.navigate(env.Page(page), "")
	}
}

// statusBar is the sidebar's bottom status (docs/design.md §4 table).
func (s *Shell) statusBar(c *ui.Context, st model.Status) {
	t := c.Theme()
	k := s.env.Kit
	bar := env.ClickableColumn(c, i18n.T("状态：回到概览")).Padding(10, 18, 14, 18).Gap(3).Shrink(0)
	bar.Children(func() {
		ui.Box(c).Height(1).Background(t.Border).Margin(0, -8, 8, -8)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			switch st.Core {
			case model.CoreRunning:
				k.Dot(c, "green")
				ui.Text(c, i18n.Tf("运行中 · %s", env.ModeName(st.Mode))).FontSize(12).SingleLine()
			case model.CoreStarting:
				ui.Spinner(c).Size(12, 12).Label(i18n.T("启动中"))
				ui.Text(c, i18n.T("启动中…")).FontSize(12)
			case model.CoreError:
				k.Dot(c, "red")
				ui.Text(c, i18n.T("内核异常")).FontSize(12).TextColor(t.Danger)
			case model.CoreMissing:
				k.Dot(c, "red")
				ui.Text(c, i18n.T("未找到内核")).FontSize(12).TextColor(t.Danger)
			default:
				k.Dot(c, "gray")
				ui.Text(c, i18n.T("未启动")).FontSize(12).TextColor(t.TextMuted)
			}
		})
		if st.Core == model.CoreRunning {
			if st.SystemProxy {
				ui.Text(c, i18n.T("系统代理已开启")).FontSize(11).TextColor(t.TextMuted).Margin(0, 0, 0, 14)
			}
			// The active core, only when the version fits the 200 pt sidebar
			// on one line (release tags and Alpha hashes do; a long custom
			// build string would be cut, so it is left out).
			if v := st.CoreVersion; v != "" && len(v) <= 20 {
				ui.Text(c, "Mihomo "+v).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine().Margin(0, 0, 0, 14)
			}
			ui.Text(c, "↑ "+env.FmtSpeed(st.Up)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").Margin(0, 0, 0, 14)
			ui.Text(c, "↓ "+env.FmtSpeed(st.Down)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").Margin(0, 0, 0, 14)
		}
	})
	if bar.Clicked() {
		s.navigate(env.Overview, "")
	}
}

// shortcuts are ⌘1–⌘9 for the nine pages of the sidebar's groups and ⌘,
// for settings.
func (s *Shell) shortcuts(c *ui.Context) {
	for i, p := range env.Order[:9] {
		if c.Shortcut(ui.Cmd, ui.Key1+ui.Key(i)) {
			s.navigate(p, "")
		}
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		s.navigate(env.Settings, "")
	}
}
