package probe

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// ---- 泄漏检查 ----

// leakTab checks whether this machine's traffic bypasses the proxy: DNS,
// IPv6 and WebRTC, plus the exit IP's type. It is about the machine, not
// a chosen exit, so the header's 出口 is hidden here.
func (p *Page) leakTab(c *ui.Context, t *ui.Theme, st model.Status) {
	e := p.e
	running := st.Core == model.CoreRunning
	lk := e.API.LeakCheck()
	note := i18n.T("按需运行，不会自动检查")
	if !running {
		note = i18n.T("需要内核运行")
	} else if !lk.CheckedAt.IsZero() {
		note = i18n.Tf("上次检查 %s", lk.CheckedAt.Format("15:04:05"))
	}
	ui.Column(c).Grow(1).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Text(c, i18n.T("泄漏检查")).Bold().SingleLine().Shrink(0)
			ui.Text(c, note).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
			ui.Spacer(c)
			ui.TextInput(c.Key("leakdomain"), &p.leakDomain).Placeholder(i18n.T("附加对比的域名（可选）")).Font("monospace").Width(240).Shrink(1).
				Label(i18n.T("附加对比的域名（可选）")).Tooltip(i18n.T("系统解析器、强制 IPv6 的直连请求和直接发出的 STUN（UDP）各测一次，与内核和代理出口比较；并列出出口 IP 的归属与三路 DNS 结果"))
			if lk.Checking {
				ui.Spinner(c).Size(12, 12).Label(i18n.T("检查中…"))
				ui.Text(c, i18n.T("检查中…")).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(0)
				return
			}
			label := i18n.T("开始检查")
			if len(lk.Items) > 0 {
				label = i18n.T("重新检查")
			}
			domain := strings.TrimSpace(p.leakDomain)
			ui.PrimaryButton(c, label).Disabled(!running).Tooltip(i18n.T("检查 DNS、IPv6 和 WebRTC 是否绕过代理，以及出口 IP 的类型")).OnClick(func() {
				e.Run(func() error { return e.API.RunLeakCheckWith(domain) }, "")
			})
		})
		if lk.Error != "" {
			e.Kit.Banner(c, "error", lk.Error, nil)
		}
		if len(lk.Items) == 0 {
			switch {
			case lk.Checking:
				e.Kit.Empty(c, "probe", i18n.T("检查中…"), i18n.T("正在比较系统解析器、内核和代理出口看到的地址。"), nil)
			case !running:
				e.Kit.Empty(c, "probe", i18n.T("内核未启动"), i18n.T("启动内核后可以检查 DNS、IPv6 和 WebRTC 是否绕过代理。"), nil)
			default:
				e.Kit.Empty(c, "probe", i18n.T("尚未检查"), i18n.T("检查 DNS、IPv6 和 WebRTC 是否绕过代理，以及出口 IP 的类型")+i18n.T("。"), nil)
			}
			return
		}
		// One line per item, each opening its details on a click; the
		// list scrolls inside the box.
		ui.Column(c).Grow(1).Radius(10).Background(t.Surface).Border(1, t.Border).Clip().Children(func() {
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Dividers(1, t.Border).Children(func() {
					for _, it := range lk.Items {
						p.leakRow(c, t, it, st.TUN)
					}
				})
			})
		})
	})
}

// leakFix is the Settings tab (settings tabKeys) where what a leak item
// found can be fixed, "" when it is fine or nothing in Lanflow fixes it.
// The DNS items' advice is TUN or 自动设置系统 DNS (both in 设置 › 代理);
// IPv6 needs TUN turned on (代理), or with TUN already on, its IPv6 and
// route settings (高级). WebRTC is a browser matter beyond TUN and IP 类型
// a property of the node: no link.
func leakFix(it model.LeakItem, tun bool) string {
	if it.Status != model.LeakWarn && it.Status != model.LeakFail {
		return ""
	}
	switch it.Name {
	case "DNS", model.LeakItemDNSCmp:
		return "proxy"
	case "IPv6":
		if tun {
			return "advanced"
		}
		return "proxy"
	}
	return ""
}

// leakRow draws one result as a line (status dot, name, state, summary)
// that opens the structured rows and the explanation under it, with a
// link to the fix at its end when there is one.
func (p *Page) leakRow(c *ui.Context, t *ui.Theme, it model.LeakItem, tun bool) {
	e := p.e
	dot, word := "gray", i18n.T("未检查")
	switch it.Status {
	case model.LeakOK:
		dot, word = "green", i18n.T("正常")
	case model.LeakWarn:
		dot, word = "orange", i18n.T("部分")
	case model.LeakFail:
		dot, word = "red", i18n.T("泄漏")
	case model.LeakSkipped:
		dot, word = "gray", i18n.T("跳过")
	}
	name := it.Title
	if name == "" {
		name = it.Name
	}
	open := p.leakOpen[it.Name]
	if open == nil {
		open = new(bool)
		p.leakOpen[it.Name] = open
	}
	ui.Column(c.Key(it.Name)).Children(func() {
		var parts ui.CollapsibleParts
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(0, 12, 0, 0).Children(func() {
			parts = ui.CollapsibleBase(c.Key("coll"), open)
			parts.Trigger.Grow(1).Gap(8).PaddingX(12).MinHeight(34).AlignItems(ui.Center).Label(name + " · " + word).Children(func() {
				ui.Box(c).TextColor(t.TextMuted).Children(func() { e.Kit.Icon(c, "chevron", 12) }).Rotate(90 * parts.Progress())
				e.Kit.Dot(c, dot)
				ui.Text(c, name).Bold().FontSize(13).SingleLine().Shrink(0)
				ui.Text(c, word).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(0)
				ui.Text(c, it.Summary).FontSize(12).Grow(1).Shrink(1).SingleLine()
			})
			if key := leakFix(it, tun); key != "" {
				place := env.SettingsPlace(key)
				e.GoLink(c, i18n.Tf("前往%s", place), i18n.Tf("%s：前往%s", name, place), env.Settings, key)
			}
		})
		parts.Panel(func() {
			ui.Column(c).Gap(4).Padding(0, 12, 10, 40).Children(func() {
				if len(it.Rows) > 0 {
					ui.Column(c).Gap(2).Children(func() {
						for _, r := range it.Rows {
							ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
								ui.Text(c, r.Label).FontSize(11).TextColor(t.TextMuted).Width(120).Shrink(0)
								ui.Text(c, r.Value).FontSize(11).Font("monospace").Grow(1).Selectable()
							})
						}
					})
				}
				ui.Text(c, it.Summary).FontSize(12).Selectable()
				if it.Detail != "" {
					ui.Text(c, it.Detail).FontSize(11).TextColor(t.TextMuted).Selectable()
				}
			})
		})
	})
}
