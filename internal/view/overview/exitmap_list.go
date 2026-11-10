// The list of routes beside the globe: 当前出口, then 其他路由.

package overview

import (
	"github.com/egoist/mygo/ui"

	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// routeCard is the list of routes beside the globe: 当前出口, then 其他路由.
func (p *Page) routeCard(c *ui.Context, st model.Status, rs []route) {
	t := c.Theme()
	run := st.Core == model.CoreRunning
	direct := st.Mode == model.ModeDirect
	p.mu.Lock()
	probing := p.probing
	p.mu.Unlock()
	ui.Scroll(c).Radius(10).Background(t.Surface).Border(1, t.Border).Clip().Grow(1).Shrink(1).MinHeight(96).Children(func() {
		ui.Column(c).Padding(10, 12).Gap(2).Children(func() {
			p.routeList(c, st, rs, run, direct, probing)
		})
	})
}

// pathNote is the line under a route about its path, and its colour. Only
// a contradiction (PathSuspicious: 「名称为台湾，实测出口在香港」) is a warning
// in orange (docs/design.md §5.1 名不副实提示); a relay (「经中转 · 入口 上海
// 电信 → 落地 洛杉矶」) or a chain (「3 跳链式代理」) is plain information in
// the muted colour (docs/exit-map.md §4A). Path notes of a measured exit
// only: a name-guessed relay is itself a guess.
func pathNote(ex model.ExitInfo, t *ui.Theme) (string, ui.Color) {
	if ex.PathNote == "" {
		return "", ui.Color{}
	}
	switch {
	case ex.Path == model.PathSuspicious:
		return ex.PathNote, t.Warning
	case ex.Source != model.LocateProbe:
		return "", ui.Color{}
	case ex.Path == model.PathRelay:
		return i18n.Tf("经中转 · %s", ex.PathNote), t.TextMuted
	default:
		return ex.PathNote, t.TextMuted
	}
}

// routeList is the right-hand list: 当前出口, then 其他路由.
func (p *Page) routeList(c *ui.Context, st model.Status, rs []route, run, direct, probing bool) {
	t := c.Theme()
	k := p.e.Kit
	switch {
	case !run:
		ui.Text(c, i18n.T("内核未启动")).FontSize(12).TextColor(t.TextMuted).Padding(20, 0).TextAlign(ui.Center)
		return
	case direct:
		ui.Column(c).Gap(4).Padding(20, 0).Children(func() {
			ui.Text(c, i18n.T("直连模式")).FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center)
			ui.Text(c, i18n.T("流量不经过节点，无出口可显示")).FontSize(11).TextColor(t.TextMuted).TextAlign(ui.Center)
		})
		return
	case len(rs) == 0:
		ui.Text(c, i18n.T("暂无路由")).FontSize(12).TextColor(t.TextMuted).Padding(20, 0).TextAlign(ui.Center)
		return
	}
	px := p.e.API.Proxies()
	for i, r := range rs {
		if i == 0 {
			ui.Text(c, i18n.T("当前出口")).FontSize(11).TextColor(t.TextMuted).Padding(0, 6, 2, 6)
		} else if i == 1 {
			ui.Text(c, i18n.T("其他路由")).FontSize(11).TextColor(t.TextMuted).Padding(10, 6, 2, 6)
		}
		rt := r
		col := routeColor(rt, t)
		item := env.ClickableColumn(c.Key("route:"+rt.Group), i18n.Tf("%s：%s，在代理页查看", rt.Group, rt.Node)).Padding(6).Gap(3).Radius(6)
		if item.Hovered() {
			item.Background(t.SurfaceHover)
		}
		item.Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(10, 10).Radius(2).Background(col).Shrink(0)
				name := rt.Node
				if rt.place != nil {
					if f := geo.Flag(rt.place.Country); f != "" {
						name = f + " " + name
					}
				}
				size := float32(13)
				if i > 0 {
					size = 12
				}
				ui.Text(c, name).FontSize(size).FontWeight(600).SingleLine().Grow(1)
				pr := px.Proxies[rt.Node]
				k.Delay(c, pr.Delay, pr.Testing)
			})
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				city := i18n.T("位置未知")
				if rt.place != nil {
					city = rt.place.City
				}
				ui.Text(c, rt.Group+" · "+city).FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1)
				switch {
				case rt.place == nil:
				case probing:
					k.Tag(c, i18n.T("实测中…"), "gray")
				case rt.Exit.Source == model.LocateProbe:
					k.Tag(c, i18n.T("实测"), "green")
				default:
					k.Tag(c, i18n.T("推测"), "gray")
				}
			})
			if note, col := pathNote(rt.Exit, t); note != "" {
				ui.Text(c, note).FontSize(11).TextColor(col)
			}
			ui.Text(c, i18n.Tf("%s · %d 个连接", env.FmtSpeed(rt.Speed), rt.Connections)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
		})
		if item.Clicked() {
			p.e.Navigate(env.Proxies, rt.Group)
		}
	}
}
