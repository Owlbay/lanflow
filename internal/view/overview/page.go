// Package overview is the 概览 page (docs/design.md §5.1): the switches,
// the exit map with its route list, the subscription and live-speed
// cards and the terminal proxy command.
package overview

import (
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	mu           sync.Mutex
	pendingCore  *bool // optimistic switch states while an action runs
	pendingProxy *bool
	pendingMode  *model.Mode
	probing      bool

	flows     map[string]*flow // exit map animation state by route group
	land      landCache
	ground    ground     // the graticule and land dots, drawn once per view (globe_ground.go)
	view      viewState  // projection glide
	dests     destState  // destination countries by route
	hits      []hitArc   // arcs of the last frame, for click testing
	hitOrigin [2]float32 // map box origin of the last frame
}

// New returns the page.
func New(e *env.Env) *Page { return &Page{e: e, flows: map[string]*flow{}} }

// ProxyCommand is the shell command that points a terminal at the proxy.
func ProxyCommand(port int, mac bool) string {
	if port <= 0 {
		port = 7890
	}
	if !mac {
		return fmt.Sprintf(`$env:HTTPS_PROXY="http://127.0.0.1:%d"; $env:HTTP_PROXY="http://127.0.0.1:%d"; $env:ALL_PROXY="socks5://127.0.0.1:%d"`, port, port, port)
	}
	return fmt.Sprintf("export https_proxy=http://127.0.0.1:%d http_proxy=http://127.0.0.1:%d all_proxy=socks5://127.0.0.1:%d", port, port, port)
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	k := p.e.Kit
	st := p.e.API.Status()
	k.Header(c, i18n.T("概览"), "", nil)
	if !st.HasSubscriptions {
		p.emptyState(c, st)
		return
	}
	// The whole page fits the window without scrolling (docs/design.md
	// §4): under the switches, the exit globe takes the left two thirds
	// and all the height; the routes, the subscription, the speed and the
	// terminal command stack on the right, the routes taking what the
	// others leave.
	ui.Column(c).Grow(1).Padding(0, 24, 20, 24).Gap(16).Clip().Children(func() {
		p.switches(c, st)
		ui.Row(c).Grow(1).Gap(16).AlignItems(ui.Stretch).Children(func() {
			p.exitCard(c, st)
			ui.Column(c).Grow(36).Basis(0).Gap(12).Children(func() {
				p.routeCard(c, st)
				p.subscriptionCard(c, st)
				p.speedCard(c, st)
				p.terminalRow(c, st)
			})
		})
	})
}

// emptyState is the first-run guide (no subscriptions yet).
func (p *Page) emptyState(c *ui.Context, st model.Status) {
	k := p.e.Kit
	ui.Column(c).Grow(1).Center().Gap(20).Children(func() {
		k.Empty(c, "subs", i18n.T("还没有订阅"), i18n.T("导入机场提供的订阅链接或 YAML 配置文件后即可开始使用。"), func() {
			if ui.PrimaryButton(c, i18n.T("导入订阅")).Clicked() {
				p.e.Navigate(env.Subs, "import-url")
			}
			if ui.Button(c, i18n.T("从文件导入")).Clicked() {
				p.e.Navigate(env.Subs, "import-file")
			}
		})
		if st.Core == model.CoreMissing {
			ui.Box(c).MaxWidth(520).Children(func() { p.missingBanner(c, st) })
		}
	})
}

// ---- switches ----

func (p *Page) switches(c *ui.Context, st model.Status) {
	t := c.Theme()
	k := p.e.Kit
	p.mu.Lock()
	pc, pp, pm := p.pendingCore, p.pendingProxy, p.pendingMode
	p.mu.Unlock()
	busy := st.Core == model.CoreStarting || st.Core == model.CoreMissing

	// Two columns of two rows (内核 / 系统代理 beside TUN / 模式): the
	// group stays 80 pt high, leaving the room to the exit map.
	k.Section(c, "", "", func() {
		ui.Row(c).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Dividers(1, t.Border).Children(func() { p.coreRows(c, st, pc, pp, busy) })
			ui.Box(c).Width(1).Background(t.Border).Shrink(0)
			ui.Column(c).Grow(1).Basis(0).Dividers(1, t.Border).Children(func() { p.modeRows(c, st, pm, busy) })
		})
	})
}

// coreRows are the 内核 and 系统代理 rows of the switch group.
func (p *Page) coreRows(c *ui.Context, st model.Status, pc, pp *bool, busy bool) {
	t := c.Theme()
	k := p.e.Kit
	{
		// 内核
		coreOn := st.Core == model.CoreRunning || st.Core == model.CoreStarting
		if pc != nil {
			coreOn = *pc
		}
		row(c, i18n.T("内核"), func() {
			switch st.Core {
			case model.CoreRunning:
				k.Dot(c, "green")
				ui.Text(c, i18n.T("运行中")).FontSize(13).SingleLine().Shrink(0)
				meta := ""
				if st.CoreVersion != "" {
					meta = "· " + st.CoreVersion
				}
				if !st.StartedAt.IsZero() {
					meta += i18n.T(" · 已运行 ") + env.FmtUptime(time.Since(st.StartedAt))
				}
				ui.Text(c, meta).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
			case model.CoreStarting:
				ui.Spinner(c).Size(12, 12).Label(i18n.T("启动中"))
				ui.Text(c, i18n.T("启动中…")).FontSize(13)
			case model.CoreError:
				k.Dot(c, "red")
				ui.Text(c, i18n.T("内核异常")).FontSize(13).TextColor(t.Danger)
			case model.CoreMissing:
				k.Dot(c, "red")
				ui.Text(c, i18n.T("未找到内核")).FontSize(13).TextColor(t.Danger)
			default:
				k.Dot(c, "gray")
				ui.Text(c, i18n.T("未启动")).FontSize(13).TextColor(t.TextMuted)
			}
		}, func() {
			on := coreOn
			ui.Switch(c, &on).Label(i18n.T("内核开关")).Disabled(busy).OnChange(func() {
				p.setPending(&p.pendingCore, on)
				if on {
					p.run(func() error { return p.e.API.StartCore() }, i18n.T("内核已启动"), &p.pendingCore)
				} else {
					p.run(func() error { return p.e.API.StopCore() }, i18n.T("内核已停止"), &p.pendingCore)
				}
			})
		})
		if st.Core == model.CoreError {
			ui.Box(c).Padding(0, 12, 10, 12).Children(func() {
				msg := i18n.T("Mihomo 内核意外退出，自动重启也失败了。系统代理已自动关闭，避免断网。")
				if st.CoreError != "" {
					msg = i18n.Tf("Mihomo 内核意外退出：%s。系统代理已自动关闭，避免断网。", st.CoreError)
				}
				k.Banner(c, "error", msg, func() {
					if ui.Button(c, i18n.T("查看日志")).Clicked() {
						p.e.Navigate(env.Logs, "")
					}
					if ui.PrimaryButton(c, i18n.T("重新启动")).Clicked() {
						p.run(func() error { return p.e.API.RestartCore() }, i18n.T("内核已重启"), nil)
					}
				})
			})
		}
		if st.Core == model.CoreMissing {
			ui.Box(c).Padding(0, 12, 10, 12).Children(func() { p.missingBanner(c, st) })
		}

		// 系统代理
		proxyOn := st.Core == model.CoreRunning && st.SystemProxy
		if pp != nil {
			proxyOn = *pp
		}
		port := st.MixedPort
		if port == 0 {
			port = 7890
		}
		row(c, i18n.T("系统代理"), func() {
			ui.Text(c, fmt.Sprintf("127.0.0.1:%d", port)).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
		}, func() {
			on := proxyOn
			if st.ShowProxyHint && !on {
				// First-run bubble after the first import + start (docs/design.md §8.1).
				tip := ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(4, 10).Radius(8).Background(t.Accent).
					Cursor(ui.CursorPointer).Role(ui.RoleButton).Label(i18n.T("打开以让系统流量走代理，点击关闭提示")).Shrink(0)
				tip.Children(func() {
					ui.Text(c, i18n.T("打开以让系统流量走代理")).FontSize(12).TextColor(t.AccentText).SingleLine()
					ui.Box(c).TextColor(t.AccentText.Alpha(0.8)).Children(func() { k.Icon(c, "close", 11) })
				})
				if tip.Clicked() {
					p.e.Run(p.e.API.DismissProxyHint, "")
				}
				ui.Box(c).Width(0).Height(0).Margin(0, -3, 0, 0).Children(func() {
					ui.Box(c).Width(6).Height(6).Background(t.Accent).Rotate(45).Margin(0, 0, 0, -3)
				})
			}
			ui.Switch(c, &on).Label(i18n.T("系统代理开关")).Disabled(busy).OnChange(func() {
				p.setPending(&p.pendingProxy, on)
				msg := i18n.T("系统代理已关闭")
				if on {
					msg = i18n.T("系统代理已开启")
				}
				p.run(func() error { return p.e.API.SetSystemProxy(on) }, msg, &p.pendingProxy)
			})
		})
	}
}

// modeRows are the TUN 模式 and 模式 rows of the switch group.
func (p *Page) modeRows(c *ui.Context, st model.Status, pm *model.Mode, busy bool) {
	t := c.Theme()
	{
		// TUN: next to the system proxy so it can be switched without a trip
		// to 设置 › 代理 (the first time it guides through the helper install).
		row(c, i18n.T("TUN 模式"), func() {
			desc := i18n.T("接管所有应用的流量")
			if !st.TUNAvailable {
				desc = i18n.T("首次开启需安装权限组件")
			}
			ui.Text(c, desc).FontSize(12).TextColor(t.TextMuted)
		}, func() {
			on := st.TUN
			ui.Switch(c, &on).Label(i18n.T("TUN 模式开关")).Disabled(busy).OnChange(func() { env.SetTUN(p.e, on) })
		})

		mode := st.Mode
		if pm != nil {
			mode = *pm
		}
		row(c, i18n.T("模式"), nil, func() {
			idx := modeIndex(mode)
			ui.Segmented(c, &idx, i18n.T("规则"), i18n.T("全局"), i18n.T("直连")).Label(i18n.T("模式")).OnChange(func() {
				m := modes[idx]
				p.mu.Lock()
				p.pendingMode = &m
				p.mu.Unlock()
				p.e.Run(func() error {
					err := p.e.API.SetMode(m)
					p.mu.Lock()
					p.pendingMode = nil
					p.mu.Unlock()
					return err
				}, i18n.Tf("已切换到%s模式", modeName(m)))
			})
		})
	}
}

var modes = []model.Mode{model.ModeRule, model.ModeGlobal, model.ModeDirect}

func modeIndex(m model.Mode) int {
	for i, x := range modes {
		if x == m {
			return i
		}
	}
	return 0
}

func modeName(m model.Mode) string {
	switch m {
	case model.ModeGlobal:
		return i18n.T("全局")
	case model.ModeDirect:
		return i18n.T("直连")
	default:
		return i18n.T("规则")
	}
}

// row is a settings row with a fixed-width label, middle content and a
// right-aligned control.
func row(c *ui.Context, label string, middle func(), control func()) {
	ui.Row(c).MinHeight(40).Padding(6, 12).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(13).MinWidth(72).Shrink(0).SingleLine()
		if middle != nil {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(1).Children(middle)
		}
		ui.Spacer(c)
		if control != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(control)
		}
	})
}

func (p *Page) setPending(field **bool, v bool) {
	p.mu.Lock()
	b := v
	*field = &b
	p.mu.Unlock()
}

// run runs job through Env.Run and clears the optimistic switch state
// once it is done.
func (p *Page) run(job func() error, ok string, field **bool) {
	p.e.Run(func() error {
		err := job()
		if field != nil {
			p.mu.Lock()
			*field = nil
			p.mu.Unlock()
		}
		return err
	}, ok)
}

// missingBanner tells that the core binary was not found.
func (p *Page) missingBanner(c *ui.Context, st model.Status) {
	dl := p.e.API.CoreDownload()
	if dl.Active {
		p.e.Kit.Banner(c, "info", env.CoreDownloadText(dl), func() {
			ui.Spinner(c).Label(i18n.T("正在下载内核")).Size(14, 14)
		})
		return
	}
	msg := i18n.T("未找到 Mihomo 内核：内核文件不存在或无法执行。可下载官方内核，或选择本机已有的内核文件。")
	if st.CoreError != "" {
		msg = i18n.T("未找到 Mihomo 内核：") + st.CoreError
	}
	if dl.Error != "" {
		msg = dl.Error
	}
	p.e.Kit.Banner(c, "error", msg, func() {
		if ui.Button(c, i18n.T("选择内核文件")).Clicked() {
			go p.pickCore()
		}
		if ui.PrimaryButton(c, i18n.T("下载内核")).Clicked() {
			p.e.Run(func() error { return p.e.API.DownloadCore("") }, i18n.T("Mihomo 内核已下载完成"))
		}
	})
}

// pickCore lets the user choose a core binary and saves it in settings.
func (p *Page) pickCore() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: i18n.T("选择 Mihomo 内核文件")})
	if err != nil || len(paths) == 0 {
		return
	}
	s := p.e.API.Settings()
	s.CorePath = paths[0]
	p.e.Run(func() error { return p.e.API.SaveSettings(s) }, i18n.T("已选择内核文件"))
}

// ---- cards ----

// card is a rounded surface the overview's panels share.
func card(c *ui.Context) ui.Element {
	t := c.Theme()
	return ui.Column(c).Radius(10).Background(t.Surface).Border(1, t.Border).Padding(12, 14).Gap(8)
}

func cardTitle(c *ui.Context, title string, right func()) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).MinHeight(24).Children(func() {
		ui.Text(c, title).FontSize(13).FontWeight(600).SingleLine()
		ui.Spacer(c)
		if right != nil {
			right()
		}
	})
}

func (p *Page) subscriptionCard(c *ui.Context, st model.Status) {
	t := c.Theme()
	var cur *model.Subscription
	for _, s := range p.e.API.Subscriptions() {
		if s.Current {
			cp := s
			cur = &cp
			break
		}
	}
	cd := card(c).Shrink(0).Cursor(ui.CursorPointer).Label(i18n.T("当前订阅，打开订阅页")).Role(ui.RoleButton)
	cd.Children(func() {
		cardTitle(c, i18n.T("当前订阅"), func() { ui.Text(c, "›").TextColor(t.TextMuted) })
		name := "—"
		if cur != nil {
			name = cur.Name
		}
		ui.Text(c, name).FontSize(17).FontWeight(600).SingleLine()
		if cur == nil {
			ui.Text(c, i18n.T("尚未选择订阅")).FontSize(12).TextColor(t.TextMuted)
			return
		}
		// Traffic on the left, the expiry on the right: the expiry shows
		// even when the total traffic is unknown (docs/design.md §5.1 当前订阅).
		used := cur.Upload + cur.Download
		ui.Row(c).Gap(8).Children(func() {
			traffic := i18n.T("无流量信息")
			switch {
			case cur.Total > 0:
				traffic = fmt.Sprintf(i18n.T("已用 %s / %s GB"), env.FmtGB(used), env.FmtGB(cur.Total))
			case cur.Source == "file":
				traffic = i18n.T("本地文件 · 无流量信息")
			}
			ui.Text(c, traffic).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine().Shrink(1)
			ui.Spacer(c)
			if !cur.Expire.IsZero() {
				txt, col := expiryText(cur.Expire, time.Now(), t)
				ui.Text(c, txt).FontSize(12).TextColor(col).FontFeatures("tnum").SingleLine()
			}
		})
		if cur.Total > 0 {
			frac := float64(used) / float64(cur.Total)
			bar := ui.Progress(c, min(1, frac)).Label(i18n.T("已用流量"))
			if frac > 0.85 {
				bar.TextColor(t.Warning)
			}
		}
	})
	if cd.Clicked() {
		p.e.Navigate(env.Subs, "")
	}
}

// expiryText is the subscription's expiry label and colour: red once
// expired, orange within a week, muted otherwise.
func expiryText(expire, now time.Time, t *ui.Theme) (string, ui.Color) {
	switch {
	case expire.Before(now):
		return i18n.T("已过期"), t.Danger
	case expire.Sub(now) < 7*24*time.Hour:
		return i18n.T("到期 ") + expire.Format("2006-01-02"), t.Warning
	default:
		return i18n.T("到期 ") + expire.Format("2006-01-02"), t.TextMuted
	}
}

func (p *Page) speedCard(c *ui.Context, st model.Status) {
	t := c.Theme()
	run := st.Core == model.CoreRunning
	up, down := p.e.API.TrafficHistory()
	cd := card(c).Shrink(0).Gap(6)
	cd.Children(func() {
		cardTitle(c, i18n.T("实时速度"), func() {
			if !run {
				ui.Text(c, i18n.T("内核未启动")).FontSize(11).TextColor(t.TextMuted).Padding(1, 6).Radius(999).Background(t.TextMuted.Alpha(0.12))
				return
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				legend(c, i18n.T("下行"), t.Accent, false)
				legend(c, i18n.T("上行"), t.TextMuted, true)
			})
		})
		body := ui.Column(c).Gap(4)
		if !run {
			body.Opacity(0.5)
		}
		body.Children(func() {
			ui.Row(c).Gap(16).AlignItems(ui.Baseline).Children(func() {
				bigNumber(c, "↑", st.Up, run)
				bigNumber(c, "↓", st.Down, run)
			})
			speedChart(c, up, down, run)
			ui.Row(c).Gap(4).Children(func() {
				foot := "—"
				if run {
					foot = fmt.Sprintf(i18n.T("本次 ↑ %s ↓ %s · 连接 %d · 内存 %s"), env.FmtBytes(st.TotalUp), env.FmtBytes(st.TotalDown), st.Connections, env.FmtBytes(st.Memory))
				}
				ui.Text(c, foot).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
			})
		})
	})
}

func legend(c *ui.Context, label string, col ui.Color, dashed bool) {
	t := c.Theme()
	ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(12, 2).Draw(func(p *ui.Painter, r ui.Rect) {
			if dashed {
				p.Line(r.X, r.Y+1, r.X+4, r.Y+1, 1.5, col)
				p.Line(r.X+7, r.Y+1, r.X+11, r.Y+1, 1.5, col)
			} else {
				p.Line(r.X, r.Y+1, r.X+r.W, r.Y+1, 1.5, col)
			}
		})
		ui.Text(c, label).FontSize(11).TextColor(t.TextMuted)
	})
}

func bigNumber(c *ui.Context, arrow string, v int64, run bool) {
	t := c.Theme()
	ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
		ui.Text(c, arrow).FontSize(13).TextColor(t.TextMuted)
		txt := "—"
		if run {
			txt = env.FmtSpeed(v)
		}
		ui.Text(c, txt).FontSize(22).FontFeatures("tnum").SingleLine()
	})
}

// speedChart draws the last 60 seconds: down as an area + line, up as a
// dashed line scaled to half the height.
func speedChart(c *ui.Context, up, down []int64, run bool) {
	t := c.Theme()
	ui.Box(c).Height(48).Draw(func(p *ui.Painter, r ui.Rect) {
		for _, f := range []float32{.25, .5, .75} {
			p.Line(r.X, r.Y+r.H*f, r.X+r.W, r.Y+r.H*f, 1, t.Border)
		}
		if !run || len(down) < 2 {
			return
		}
		var maxD, maxU int64 = 1, 1
		for _, v := range down {
			maxD = max(maxD, v)
		}
		for _, v := range up {
			maxU = max(maxU, v)
		}
		sd, su := float32(maxD)*1.15, float32(maxU)*1.6
		var line, area ui.Path
		n := len(down)
		for i, v := range down {
			x := r.X + r.W*float32(i)/float32(n-1)
			y := r.Y + r.H - r.H*float32(v)/sd
			if i == 0 {
				line.MoveTo(x, y)
				area.MoveTo(x, r.Y+r.H).LineTo(x, y)
			} else {
				line.LineTo(x, y)
				area.LineTo(x, y)
			}
		}
		area.LineTo(r.X+r.W, r.Y+r.H).Close()
		p.FillPath(&area, t.Accent.Alpha(0.12))
		p.StrokePath(&line, 1.6, t.Accent)
		if len(up) >= 2 {
			var ul ui.Path
			m := len(up)
			for i, v := range up {
				x := r.X + r.W*float32(i)/float32(m-1)
				y := r.Y + r.H - r.H*0.5*float32(v)/su
				if i == 0 {
					ul.MoveTo(x, y)
				} else {
					ul.LineTo(x, y)
				}
			}
			p.StrokePath(ul.Dash(0, 3, 3), 1.2, t.TextMuted)
		}
	})
	ui.Row(c).Children(func() {
		ui.Text(c, i18n.T("60 秒前")).FontSize(10).TextColor(t.TextMuted)
		ui.Spacer(c)
		ui.Text(c, i18n.T("现在")).FontSize(10).TextColor(t.TextMuted)
	})
}

// terminalRow shows the shell command with a copy button.
func (p *Page) terminalRow(c *ui.Context, st model.Status) {
	t := c.Theme()
	cmd := ProxyCommand(st.MixedPort, p.e.Mac)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Text(c, i18n.T("终端代理")).FontSize(12).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, cmd).Font("monospace").FontSize(11).SingleLine().Grow(1).Shrink(1).Basis(0).Padding(4, 8).Radius(6).
			Background(t.Surface).Border(1, t.Border).Selectable().Tooltip(cmd)
		b := ui.Button(c, "").Label(i18n.T("复制终端代理命令")).Tooltip(i18n.T("复制到剪贴板")).Children(func() {
			p.e.Kit.Icon(c, "copy", 13)
			ui.Text(c, i18n.T("复制")).SingleLine()
		})
		if b.Clicked() {
			c.WriteClipboard(cmd)
			p.e.Toast(i18n.T("已复制"), false)
		}
	})
}

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicTraffic, app.TopicProxies, app.TopicSubs, app.TopicConns, app.TopicExits, app.TopicSettings, app.TopicCoreDownload}
}
