// Package netpage is the 网络 page (docs/design.md §5.7): the passive
// state of this machine and its connection, in three tabs. 概况: where
// the direct and the proxied traffic come out, the local network and the
// proxy status, each status linking to where it is configured; 连通性:
// site connectivity; DNS: the effective DNS and the query tool. It never
// calls a third-party service; the on-demand checks are on 检测.
package netpage

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

var dnsTypes = []string{"A", "AAAA", "CNAME", "HTTPS"}

// Tabs of the page.
const (
	tabOverview = iota
	tabConn
	tabDNS
)

// tabKeys are the stored names of the tabs (model.UIState.NetTab), by tab.
var tabKeys = []string{"overview", "connectivity", "dns"}

// tabFromKey is the tab stored as k; the first one for "" or a key it
// does not know.
func tabFromKey(k string) int {
	for i, v := range tabKeys {
		if v == k {
			return i
		}
	}
	return tabOverview
}

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	tab    int
	loaded bool // tab restored from API.UIState

	dnsName  string
	dnsType  string
	querying bool

	addOpen         bool
	addName, addURL string
	addErr          string
	addSession      int
	sites           ui.ListState

	// The effective DNS and sniffer settings (the service parses the
	// subscription's YAML for them) are read again only when the
	// settings or the status change.
	effStale   *env.Stale
	effDNS     model.EffectiveDNS
	effSniffer model.EffectiveSniffer
	snifferOn  bool

	mu     sync.Mutex // guards the fields below, written by jobs
	dnsRes *model.DNSResult
	dnsErr string
	toast  string
}

// New returns the page.
func New(e *env.Env) *Page {
	return &Page{e: e, dnsName: "www.google.com", dnsType: "A",
		effStale: env.Watch(e.API, app.TopicSettings, app.TopicStatus)}
}

// restore loads the remembered tab once.
func (p *Page) restore() {
	if p.loaded {
		return
	}
	p.loaded = true
	p.tab = tabFromKey(p.e.API.UIState().NetTab)
}

// persist remembers the tab across restarts.
func (p *Page) persist() {
	key := tabKeys[p.tab]
	p.e.SaveUIState("net", func(u *model.UIState) { u.NetTab = key })
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	e, t := p.e, c.Theme()
	p.flushToast()
	p.restore()
	st := e.API.Status()
	running := st.Core == model.CoreRunning
	sites := e.API.Sites()
	testing := false
	for _, s := range sites {
		if s.ViaProxy == -2 || s.Direct == -2 {
			testing = true
		}
	}
	ui.Column(c).Fill().Children(func() {
		e.Kit.Header(c, i18n.T("网络"), "", func() {
			if testing {
				ui.Row(c).Gap(6).Children(func() {
					ui.Spinner(c)
					ui.Text(c, i18n.T("测试中…")).TextColor(t.TextMuted)
				})
				return
			}
			ui.Button(c, "").Label(i18n.T("重新测试")).Tooltip(i18n.T("重新读取本机网络、公网地址和网站连通性")).Children(func() {
				e.Kit.Icon(c, "refresh", 13)
				ui.Text(c, i18n.T("重新测试"))
			}).OnClick(p.retest)
		})
		// Three tabs (docs/design.md §5.7). 概况: the exit cards on top,
		// the local network and the proxy status side by side below;
		// 连通性: the site table taking the whole height; DNS: the
		// effective DNS and the query tool.
		ui.Column(c).Grow(1).Padding(0, 24, 20, 24).Gap(12).Clip().Children(func() {
			if ui.Segmented(c.Key("tabs"), &p.tab, i18n.T("概况"), i18n.T("连通性"), "DNS").Label(i18n.T("网络分栏")).Changed() {
				p.persist()
			}
			switch p.tab {
			case tabConn:
				p.sitesSection(c, t, sites, running)
			case tabDNS:
				p.dnsTab(c, t, running)
			default:
				p.overviewTab(c, t, st)
			}
		})
	})
	p.addDialog(c, t)
}

// overviewTab is 概况: the two exit cards, then 本机网络 and 代理状态
// side by side. It scrolls when the window is too short for it.
func (p *Page) overviewTab(c *ui.Context, t *ui.Theme, st model.Status) {
	net := p.e.API.NetInfo()
	ui.Scroll(c.Key("overview")).Grow(1).Gap(16).Children(func() {
		ui.Row(c).Gap(16).AlignItems(ui.Stretch).Shrink(0).Children(func() {
			p.directCard(c, t, net)
			p.exitCard(c, t, st)
		})
		ui.Row(c).Gap(16).AlignItems(ui.Stretch).Shrink(0).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Children(func() { p.localSection(c, t, net) })
			ui.Column(c).Grow(1).Basis(0).Children(func() { p.proxySection(c, t, st) })
		})
	})
}

// dnsTab is the DNS tab: the DNS in effect, linking to Settings › DNS,
// and the query tool.
func (p *Page) dnsTab(c *ui.Context, t *ui.Theme, running bool) {
	p.effective()
	ui.Column(c).Grow(1).Gap(16).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(0, 4).Shrink(0).Children(func() {
			ui.Text(c, i18n.T("当前 DNS")).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(0)
			ui.Text(c, dnsStatusText(p.effDNS)).FontSize(12).SingleLine().Shrink(1)
			p.e.GoLink(c, i18n.T("更改"), i18n.Tf("更改%s", "DNS"), env.Settings, "dns")
		})
		p.dnsSection(c, t, running)
	})
}

// sectionTitle is a group heading: bold title, muted note and, right
// aligned, what tools builds.
func sectionTitle(c *ui.Context, t *ui.Theme, title, note string, tools func()) {
	ui.Row(c).Gap(6).AlignItems(ui.Baseline).Padding(0, 4).Shrink(0).Children(func() {
		ui.Text(c, title).Bold().SingleLine().Shrink(0)
		if note != "" {
			ui.Text(c, note).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
		}
		ui.Spacer(c)
		if tools != nil {
			ui.Row(c).Gap(6).AlignItems(ui.Baseline).Shrink(0).Children(tools)
		}
	})
}

// panel is a rounded surface holding compact key-value lines: the
// 本机网络 and 代理状态 groups (docs/design.md §5.7 wireframe). It fills
// the height of its column, so the two side by side end level.
func (p *Page) panel(c *ui.Context, t *ui.Theme, title string, body func()) {
	ui.Column(c).Gap(6).Grow(1).Children(func() {
		sectionTitle(c, t, title, "", nil)
		ui.Column(c).Grow(1).Padding(10, 14).Gap(6).Radius(10).Background(t.Surface).Border(1, t.Border).Children(body)
	})
}

// kvGrid lays key-value pairs out two per line: the labels as wide as
// their text, the values sharing the rest. body builds them with kvCell.
func kvGrid(c *ui.Context, body func()) {
	ui.Grid(c).ColumnTracks(ui.FitContent(), ui.Fr(1), ui.FitContent(), ui.Fr(1)).GapX(8).GapY(6).AlignItems(ui.Center).Children(body)
}

// kvCell is one pair of a kvGrid: the label and the value as two cells;
// extra, if given, follows the value in its cell.
func (p *Page) kvCell(c *ui.Context, t *ui.Theme, k, v string, extra func()) {
	ui.Text(c, k).FontSize(12).TextColor(t.TextMuted).SingleLine()
	ui.Row(c).Gap(6).AlignItems(ui.Center).MarginEnd(8).Children(func() {
		ui.Text(c, v).FontSize(12).FontFeatures("tnum").Selectable().SingleLine().Shrink(1)
		if extra != nil {
			extra()
		}
	})
}

func (p *Page) flushToast() {
	p.mu.Lock()
	msg := p.toast
	p.toast = ""
	p.mu.Unlock()
	if msg != "" {
		p.e.Toast(msg, false)
	}
}

// ---- exit cards ----

// retest refreshes everything on the page: network info and public IP
// (RefreshNetwork also re-measures the main exit), then the sites.
func (p *Page) retest() {
	e := p.e
	e.Run(func() error {
		if err := e.API.RefreshNetwork(); err != nil {
			return err
		}
		return e.API.TestSites()
	}, "")
}

func (p *Page) directCard(c *ui.Context, t *ui.Theme, n model.NetInfo) {
	e := p.e
	you := e.API.You()
	fetching := n.PublicIPStatus == "fetching"
	env.InfoCard(c, i18n.T("直连 · 本机公网"), func() {
		if you != nil && !fetching {
			e.Kit.Tag(c, i18n.T("实测"), "green")
		}
		if fetching {
			ui.Spinner(c).Size(12, 12).Label(i18n.T("正在获取"))
		} else {
			b := env.Clickable(ui.Box(c), i18n.T("重新获取公网地址")).Padding(2).Radius(4).TextColor(t.TextMuted).
				Tooltip(i18n.T("重新获取公网地址"))
			b.Children(func() { e.Kit.Icon(c, "refresh", 13) })
			if b.Clicked() {
				e.Run(e.API.RefreshNetwork, "")
			}
		}
	}, func() {
		if you == nil {
			ui.Text(c, "—").FontSize(20).TextColor(t.TextMuted)
			switch n.PublicIPStatus {
			case "failed":
				ui.Text(c, i18n.T("获取本机公网地址失败，稍后自动重试")).FontSize(11).TextColor(t.Warning)
				if n.PublicIPError != "" {
					ui.Text(c, n.PublicIPError).FontSize(11).TextColor(t.TextMuted).Selectable()
				}
			default:
				ui.Text(c, i18n.T("正在获取本机公网地址…")).FontSize(11).TextColor(t.TextMuted)
			}
			return
		}
		ui.Text(c, you.IP).FontSize(20).FontFeatures("tnum").Selectable()
		kvGrid(c, func() {
			p.kvCell(c, t, i18n.T("位置"), placeText(you.Place), nil)
			p.kvCell(c, t, i18n.T("运营商"), ispText(*you), nil)
		})
	})
}

func (p *Page) exitCard(c *ui.Context, t *ui.Theme, st model.Status) {
	e := p.e
	routes := e.API.ExitRoutes()
	var main *model.ExitRoute
	for i := range routes {
		if routes[i].Main {
			main = &routes[i]
			break
		}
	}
	if main == nil && len(routes) > 0 {
		main = &routes[0]
	}
	group := e.API.Proxies().MainGroup
	if st.Mode == model.ModeGlobal {
		group = "GLOBAL"
	}
	if main != nil {
		group = main.Group
	}
	title := i18n.T("代理出口")
	if group != "" {
		title += " · " + group
	}
	env.InfoCard(c, title, func() {
		if main != nil {
			link := env.LinkText(c, i18n.T("在地图中查看"), "").FontSize(11)
			if link.Clicked() {
				e.Navigate(env.Overview, "")
			}
		}
	}, func() {
		switch {
		case st.Core != model.CoreRunning:
			ui.Text(c, "—").FontSize(20).TextColor(t.TextMuted)
			ui.Text(c, i18n.T("内核未启动")).FontSize(11).TextColor(t.TextMuted)
			return
		case st.Mode == model.ModeDirect:
			ui.Text(c, "—").FontSize(20).TextColor(t.TextMuted)
			ui.Text(c, i18n.T("直连模式下流量不经过代理")).FontSize(11).TextColor(t.TextMuted)
			return
		case main == nil:
			ui.Text(c, "—").FontSize(20).TextColor(t.TextMuted)
			ui.Text(c, i18n.T("没有可用的代理组")).FontSize(11).TextColor(t.TextMuted)
			return
		}
		x := main.Exit
		ip := x.Exit.IP
		if ip == "" {
			ip = "—"
		}
		ui.Text(c, ip).FontSize(20).FontFeatures("tnum").Selectable()
		kvGrid(c, func() {
			p.kvCell(c, t, i18n.T("节点"), flagOf(x.Place)+main.Node, nil)
			p.kvCell(c, t, i18n.T("位置"), placeName(x.Place), func() {
				switch x.Source {
				case model.LocateProbe:
					e.Kit.Tag(c, i18n.T("实测"), "green")
				case model.LocateName:
					e.Kit.Tag(c, i18n.T("推测"), "gray")
				}
			})
			if x.Source == model.LocateProbe {
				p.kvCell(c, t, i18n.T("运营商"), ispText(x.Exit), nil)
				p.kvCell(c, t, i18n.T("CF 机房"), env.Dash(x.Colo), nil)
			}
		})
		p.pathRow(c, t, x)
	})
}

// pathRow is the exit card's 路径 line: the kind of path and, muted after
// it, the entry and landing or the note (exit-map.md 4A).
func (p *Page) pathRow(c *ui.Context, t *ui.Theme, x model.ExitInfo) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, i18n.T("路径")).FontSize(12).TextColor(t.TextMuted).Shrink(0)
		switch x.Path {
		case model.PathDirect:
			ui.Text(c, i18n.T("直连落地")).FontSize(12).SingleLine()
		case model.PathRelay:
			ui.Text(c, i18n.T("经中转")).FontSize(12).SingleLine().Shrink(0)
			if x.Entry != nil {
				entry := placeName(x.Entry.Place)
				if x.Entry.ISP != "" {
					entry += " · " + x.Entry.ISP
				}
				if x.Entry.IP != "" {
					entry += i18n.Tf("（%s）", x.Entry.IP)
				}
				text := i18n.Tf("入口 %s → 落地 %s", entry, placeName(x.Place))
				ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(text)
			}
		case model.PathChain:
			ui.Text(c, i18n.Tf("链式代理 · %d 跳", x.ChainHops)).FontSize(12).SingleLine()
		case model.PathSuspicious:
			ui.Text(c, i18n.T("⚠ 位置存疑")).FontSize(12).TextColor(t.Warning).SingleLine().Shrink(0)
			if x.PathNote != "" {
				ui.Text(c, x.PathNote).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(x.PathNote)
			}
		default:
			ui.Text(c, "—").FontSize(12).TextColor(t.TextMuted)
		}
	})
}

// ---- sites ----

var siteCols = []ui.TableColumn{
	{Title: "网站", ID: "name", Width: 100},
	{Title: "地址", ID: "url", MinWidth: 120},
	{Title: "经代理", ID: "proxy", Width: 76, Align: ui.End},
	{Title: "直连", ID: "direct", Width: 76, Align: ui.End},
}

func (p *Page) sitesSection(c *ui.Context, t *ui.Theme, sites []model.SiteCheck, running bool) {
	e := p.e
	// The table takes the height left in its column and scrolls inside.
	ui.Column(c).Gap(6).Grow(1).Basis(0).MinHeight(120).Children(func() {
		sectionTitle(c, t, i18n.T("网站连通性"), i18n.T("· 分别经代理和直连访问一次"), func() {
			add := env.LinkText(c, i18n.T("＋ 添加网站"), "")
			if add.Clicked() {
				p.addName, p.addURL, p.addErr = "", "https://", ""
				p.addSession++
				p.addOpen = true
			}
		})
		p.sites.Key = func(i int) any { return sites[i].URL }
		p.sites.CellMenu = func(m *ui.Menu, row, col int) {
			s := sites[row]
			if m.Item(i18n.Tf("删除「%s」", s.Name)).Danger().Chosen() {
				p.removeSite(s.URL)
			}
		}
		ui.Table(c.Key("sites"), &p.sites, env.I18nColumns(siteCols), len(sites), func(row, col int) {
			s := sites[row]
			switch siteCols[col].ID {
			case "name":
				ui.Text(c, s.Name).SingleLine()
			case "url":
				ui.Text(c, hostOf(s.URL)).TextColor(t.TextMuted).SingleLine()
			case "proxy":
				if !running {
					ui.Text(c, "—").TextColor(t.TextMuted)
				} else {
					e.Kit.Delay(c, max(s.ViaProxy, -1), s.ViaProxy == -2)
				}
			case "direct":
				e.Kit.Delay(c, max(s.Direct, -1), s.Direct == -2)
			}
		}).FillWidth().Grow(1).Radius(8).Border(1, t.Border).Label(i18n.T("网站连通性"))
	})
}

func (p *Page) removeSite(url string) {
	e := p.e
	s := e.API.Settings()
	var keep []model.Site
	for _, x := range s.Sites {
		if x.URL != url {
			keep = append(keep, x)
		}
	}
	s.Sites = keep
	e.Run(func() error { return e.API.SaveSettings(s) }, i18n.T("已删除网站"))
}

func (p *Page) addDialog(c *ui.Context, t *ui.Theme) {
	if !p.addOpen {
		return
	}
	ui.Modal(c, &p.addOpen, func() {
		ui.Column(c.Key(p.addSession)).Width(380).Gap(12).Children(func() {
			ui.Text(c, i18n.T("添加网站")).FontSize(15).Bold()
			ui.Form(c, func() {
				ui.Field(c, i18n.T("名称"), func() {
					ui.TextInput(c.Key("name"), &p.addName).Placeholder(i18n.T("例如 Netflix")).Width(240).AutoFocus()
				})
				ui.Field(c, i18n.T("地址"), func() {
					if ui.TextInput(c.Key("url"), &p.addURL).Placeholder("https://www.netflix.com").Font("monospace").Width(240).Invalid(p.addErr != "").Submitted() {
						p.addSite()
					}
				})
			})
			if p.addErr != "" {
				ui.Text(c, p.addErr).FontSize(12).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, i18n.T("取消")).Clicked() {
					p.addOpen = false
				}
				ui.PrimaryButton(c, i18n.T("添加")).OnClick(p.addSite)
			})
		})
	})
}

func (p *Page) addSite() {
	e := p.e
	name, url := strings.TrimSpace(p.addName), strings.TrimSpace(p.addURL)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") || hostOf(url) == "" {
		p.addErr = i18n.T("请输入以 http:// 或 https:// 开头的网址")
		return
	}
	if name == "" {
		name = hostOf(url)
	}
	s := e.API.Settings()
	for _, x := range s.Sites {
		if x.URL == url {
			p.addErr = i18n.T("这个网址已经在列表中")
			return
		}
	}
	s.Sites = append(s.Sites, model.Site{Name: name, URL: url})
	p.addOpen = false
	e.Run(func() error { return e.API.SaveSettings(s) }, i18n.Tf("已添加「%s」", name))
}

// ---- local network & proxy status ----

func (p *Page) localSection(c *ui.Context, t *ui.Theme, n model.NetInfo) {
	p.panel(c, t, i18n.T("本机网络"), func() {
		ipv6 := n.IPv6
		if ipv6 == "" {
			ipv6 = i18n.T("未分配公网地址")
		}
		kvGrid(c, func() {
			p.kvCell(c, t, i18n.T("网络接口"), env.Dash(n.Interface), nil)
			p.kvCell(c, t, i18n.T("Wi-Fi 名称"), env.Dash(n.SSID), nil)
			p.kvCell(c, t, i18n.T("局域网 IP"), env.Dash(n.LANIP), nil)
			p.kvCell(c, t, i18n.T("网关"), env.Dash(n.Gateway), nil)
			p.kvCell(c, t, i18n.T("系统 DNS"), env.Dash(strings.Join(n.DNS, ", ")), nil)
			p.kvCell(c, t, "IPv6", ipv6, nil)
		})
	})
}

// effective reads the effective DNS and sniffer settings again when they
// may have changed.
func (p *Page) effective() {
	if !p.effStale.Take() {
		return
	}
	e := p.e
	p.effDNS = e.API.EffectiveDNS()
	p.snifferOn = e.API.Settings().Sniffer.Enable
	if p.snifferOn {
		p.effSniffer = e.API.EffectiveSniffer()
	}
}

func (p *Page) proxySection(c *ui.Context, t *ui.Theme, st model.Status) {
	e := p.e
	p.effective()
	p.panel(c, t, i18n.T("代理状态"), func() {
		// Label, value and a link to where it is configured, one line each.
		ui.Grid(c).ColumnTracks(ui.FitContent(), ui.Fr(1), ui.FitContent()).GapX(8).GapY(6).AlignItems(ui.Center).Children(func() {
			p.statusLine(c, t, i18n.T("系统代理"), func() {
				if st.SystemProxy {
					e.Kit.Dot(c, "green")
					statusText(c, i18n.Tf("指向澜流 127.0.0.1:%d", st.MixedPort))
				} else {
					e.Kit.Dot(c, "gray")
					ui.Text(c, i18n.T("未开启")).FontSize(12).TextColor(t.TextMuted)
				}
			}, env.Overview, "")
			port := i18n.Tf("%d（HTTP + SOCKS5）", st.MixedPort)
			if st.AllowLAN {
				port += " · " + i18n.T("允许局域网连接")
			}
			p.statusLine(c, t, i18n.T("监听端口"), func() { statusText(c, port) }, env.Settings, "proxy")
			p.statusLine(c, t, i18n.T("TUN 模式"), func() {
				if st.TUN {
					e.Kit.Dot(c, "green")
					statusText(c, i18n.T("已开启"))
				} else {
					e.Kit.Dot(c, "gray")
					ui.Text(c, i18n.T("未开启")).FontSize(12).TextColor(t.TextMuted)
				}
			}, env.Settings, "proxy")
			// 嗅探 and DNS report the effective config (docs/design.md §5.7): the
			// subscription's sections in 跟随订阅 mode, Lanflow's when taken over.
			snf := i18n.T("未开启")
			if p.snifferOn {
				snf = i18n.T("已开启") + " · " + sourceName(p.effSniffer.Source)
			}
			p.statusLine(c, t, i18n.T("嗅探"), func() { statusText(c, snf) }, env.Settings, "sniffer")
			p.statusLine(c, t, "DNS", func() { statusText(c, dnsStatusText(p.effDNS)) }, env.Settings, "dns")
			p.statusLine(c, t, i18n.T("控制接口"), func() { statusText(c, i18n.T("本机套接字，仅澜流可访问")) }, "", "")
			if a := st.ATrust; a.Running {
				p.statusLine(c, t, i18n.T("aTrust 兼容"), func() {
					e.Kit.Dot(c, "green")
					ui.Column(c).Gap(2).Shrink(1).Children(func() {
						statusText(c, atrustSummary(a))
						ui.Text(c, atrustDetail(a)).FontSize(11).TextColor(t.TextMuted).Selectable()
					})
				}, env.Settings, "proxy")
			}
		})
	})
}

// statusLine is one 代理状态 line as three grid cells: the label, the
// value value draws and the link to page (with arg) where it is
// configured; no link when page is "".
func (p *Page) statusLine(c *ui.Context, t *ui.Theme, k string, value func(), page env.Page, arg string) {
	ui.Text(c, k).FontSize(12).TextColor(t.TextMuted).SingleLine()
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(value)
	if page == "" {
		ui.Box(c)
		return
	}
	p.e.GoLink(c, i18n.T("更改"), i18n.Tf("更改%s", k), page, arg)
}

// statusText is a 代理状态 value.
func statusText(c *ui.Context, s string) {
	ui.Text(c, s).FontSize(12).FontFeatures("tnum").SingleLine().Shrink(1).Tooltip(s)
}

// atrustSummary is the 代理状态 line for a detected aTrust.
func atrustSummary(a model.ATrustState) string {
	return i18n.Tf("已启用 · 内网网段 %d 个 · 内网域名 %d 个", len(a.CIDRs), len(a.Domains))
}

// atrustDetail lists what aTrust 兼容 keeps out of Lanflow.
func atrustDetail(a model.ATrustState) string {
	var parts []string
	if len(a.CIDRs) > 0 {
		parts = append(parts, strings.Join(a.CIDRs, ", "))
	}
	if len(a.Domains) > 0 {
		parts = append(parts, strings.Join(a.Domains, ", "))
	}
	if len(a.Gateways) > 0 {
		parts = append(parts, i18n.Tf("网关 %s", strings.Join(a.Gateways, ", ")))
	}
	if len(parts) == 0 {
		return i18n.T("等待 aTrust 连接后读取内网网段和域名")
	}
	return strings.Join(parts, " · ")
}

// sourceName words an effective-settings source.
func sourceName(src string) string {
	switch src {
	case model.EffectiveLanflow:
		return i18n.T("由澜流接管")
	case model.EffectiveDefault:
		return i18n.T("跟随订阅（订阅未设置，使用内核默认值）")
	}
	return i18n.T("跟随订阅")
}

// dnsStatusText is the 代理状态 DNS line: source and resolution mode.
func dnsStatusText(d model.EffectiveDNS) string {
	if d.Source == model.EffectiveDefault {
		return sourceName(d.Source)
	}
	mode := i18n.T("真实 IP")
	if d.Settings.EnhancedMode == "fake-ip" {
		mode = "Fake-IP"
	}
	if d.Source == model.EffectiveSubscription && !d.Enabled {
		mode = i18n.T("内置 DNS 未启用")
	}
	return sourceName(d.Source) + " · " + mode
}

// ---- DNS query ----

func (p *Page) dnsSection(c *ui.Context, t *ui.Theme, running bool) {
	e := p.e
	p.mu.Lock()
	res, errText := p.dnsRes, p.dnsErr
	p.mu.Unlock()
	ui.Column(c).Gap(6).Shrink(0).Children(func() {
		sectionTitle(c, t, i18n.T("DNS 查询"), i18n.T("经内核的解析器"), func() {
			flush := func(label string, job func() error, ok string) {
				b := env.LinkText(c, label, "")
				if !running {
					b.TextColor(t.TextMuted).Cursor(ui.CursorDefault).Disabled(true)
				} else if b.Clicked() {
					e.Run(job, ok)
				}
			}
			flush(i18n.T("清除 DNS 缓存"), e.API.FlushDNSCache, i18n.T("已清除 DNS 缓存"))
			ui.Text(c, "·").FontSize(12).TextColor(t.TextMuted)
			flush(i18n.T("清除 Fake-IP 缓存"), e.API.FlushFakeIPCache, i18n.T("已清除 Fake-IP 缓存"))
		})
		box := ui.Column(c).Radius(10).Background(t.Surface).Border(1, t.Border).Clip().Dividers(1, t.Border)
		box.Children(func() { p.dnsBox(c, t, running, res, errText) })
	})
}

// dnsBox is the DNS 查询 group: the query row and the last result.
func (p *Page) dnsBox(c *ui.Context, t *ui.Theme, running bool, res *model.DNSResult, errText string) {
	{
		ui.Row(c).Gap(8).PaddingX(12).MinHeight(40).AlignItems(ui.Center).Children(func() {
			in := ui.TextInput(c.Key("dns"), &p.dnsName).Placeholder(i18n.T("域名")).Font("monospace").Grow(1).Label(i18n.T("域名"))
			if in.Submitted() {
				p.query()
			}
			ui.Select(c.Key("dnstype"), &p.dnsType, dnsTypes).Label(i18n.T("记录类型"))
			label := i18n.T("查询")
			if p.querying {
				label = i18n.T("查询中…")
			}
			ui.PrimaryButton(c, label).Disabled(!running || p.querying || strings.TrimSpace(p.dnsName) == "").OnClick(p.query).
				Tooltip(i18n.T("查询经由内核的解析器；Fake-IP 模式下同时显示应用实际拿到的地址和解析到的真实地址"))
		})
		switch {
		case errText != "":
			ui.Text(c, errText).FontSize(12).TextColor(t.Danger).Padding(6, 12)
		case res != nil:
			p.dnsResult(c, t, *res)
		}
	}
}

func (p *Page) dnsResult(c *ui.Context, t *ui.Theme, r model.DNSResult) {
	ui.Column(c).Gap(4).Padding(8, 12).Selectable().Children(func() {
		head := fmt.Sprintf("%s  %s", r.Name, r.Type)
		if r.Status != "" {
			head += "  " + r.Status
		}
		ui.Text(c, head).Font("monospace").FontSize(12).TextColor(t.TextMuted)
		list := func(answers []string) {
			for _, a := range answers {
				ui.Text(c, a).Font("monospace").FontSize(12).FontFeatures("tnum")
			}
		}
		switch {
		case r.Status == "NXDOMAIN":
			ui.Text(c, i18n.T("域名不存在（NXDOMAIN）")).FontSize(12)
		case len(r.AppAnswers) > 0 && r.FakeIP:
			// Fake-IP mode: what the app sees next to what it really is
			// (docs/design.md §5.7 DNS 查询).
			ui.Row(c).Gap(24).AlignItems(ui.Start).Children(func() {
				ui.Column(c).Gap(2).Children(func() {
					ui.Text(c, i18n.T("应用拿到的 Fake-IP")).FontSize(11).TextColor(t.TextMuted)
					list(r.AppAnswers)
				})
				ui.Column(c).Gap(2).Children(func() {
					ui.Text(c, i18n.T("真实解析结果")).FontSize(11).TextColor(t.TextMuted)
					if len(r.Answers) == 0 {
						ui.Text(c, i18n.T("没有记录")).FontSize(12)
					}
					list(r.Answers)
				})
			})
			ui.Text(c, i18n.T("应用拿到的是内核分配的虚拟地址；连接建立时内核按规则分流，需要时再用真实地址连接。")).FontSize(11).TextColor(t.TextMuted)
		case len(r.Answers) == 0:
			ui.Text(c, i18n.T("没有记录")).FontSize(12)
		default:
			list(r.Answers)
			if r.FakeIP {
				ui.Text(c, i18n.T("这是 Fake-IP：应用拿到的是内核分配的虚拟地址，连接建立时内核再按规则解析真实地址并分流。")).FontSize(11).TextColor(t.TextMuted)
			} else if len(r.AppAnswers) > 0 && !sameAnswers(r.AppAnswers, r.Answers) {
				ui.Text(c, i18n.Tf("应用通过%s拿到的结果：", appSourceName(r.AppSource))).FontSize(11).TextColor(t.TextMuted)
				list(r.AppAnswers)
			}
		}
	})
}

func appSourceName(src string) string {
	if src == "listener" {
		return i18n.T("内核 DNS 服务")
	}
	return i18n.T("系统 DNS（TUN 接管）")
}

func sameAnswers(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			return false
		}
	}
	return true
}

func (p *Page) query() {
	e := p.e
	name := strings.TrimSpace(p.dnsName)
	if name == "" || p.querying {
		return
	}
	qtype := p.dnsType
	p.querying = true
	p.mu.Lock()
	p.dnsErr, p.dnsRes = "", nil
	p.mu.Unlock()
	e.Run(func() error {
		res, err := e.API.DNSQuery(name, qtype)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.querying = false
		if err != nil {
			p.dnsErr = err.Error()
		} else {
			p.dnsRes = &res
		}
		return nil
	}, "")
}

// ---- helpers ----

// flagOf is the flag of a place's country and a space, "" when unknown.
func flagOf(pl *model.Place) string {
	if pl == nil {
		return ""
	}
	return env.FlagPrefix(pl.Country)
}

func placeName(pl *model.Place) string {
	if pl == nil {
		return i18n.T("未知")
	}
	if pl.City != "" {
		return pl.City
	}
	return pl.Country
}

func placeText(pl *model.Place) string {
	if pl == nil {
		return i18n.T("未知")
	}
	return flagOf(pl) + placeName(pl)
}

func ispText(ep model.EndpointInfo) string {
	switch {
	case ep.ISP == "" && ep.ASN == "":
		return "—"
	case ep.ISP == "":
		return ep.ASN
	case ep.ASN == "":
		return ep.ISP
	}
	return ep.ISP + " " + ep.ASN
}

func hostOf(url string) string {
	u := url
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return u
}

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicNet, app.TopicSettings, app.TopicExits, app.TopicProxies}
}
