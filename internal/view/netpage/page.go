// Package netpage is the 网络 page (docs/design.md §5.7): where the direct
// and the proxied traffic come out, site connectivity, the local network,
// the proxy status and the DNS query tool.
package netpage

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

var dnsTypes = []string{"A", "AAAA", "CNAME", "HTTPS"}

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	dnsName  string
	dnsType  string
	querying bool

	leakDomain string           // optional domain for the DNS comparison of 泄漏检查
	leakOpen   map[string]*bool // 泄漏检查 items whose details are expanded, by name

	addOpen         bool
	addName, addURL string
	addErr          string
	addSession      int
	sites           ui.ListState

	mu     sync.Mutex // guards the fields below, written by jobs
	dnsRes *model.DNSResult
	dnsErr string
	toast  string
}

// New returns the page.
func New(e *env.Env) *Page {
	return &Page{e: e, dnsName: "www.google.com", dnsType: "A", leakOpen: map[string]*bool{}}
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	e, t := p.e, c.Theme()
	p.flushToast()
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
		// Two columns that fit the window without page scrolling
		// (docs/design.md §4 / §5.7): the exit cards on top; below, the
		// site table and the DNS query on the left, the local network,
		// the proxy status and the leak check on the right. The site
		// table and the leak results are the only parts that scroll.
		ui.Column(c).Grow(1).Padding(4, 24, 20, 24).Gap(16).Clip().Children(func() {
			ui.Row(c).Gap(16).AlignItems(ui.Stretch).Shrink(0).Children(func() {
				p.directCard(c, t)
				p.exitCard(c, t, st)
			})
			ui.Row(c).Gap(16).Grow(1).AlignItems(ui.Stretch).Children(func() {
				ui.Column(c).Grow(1).Basis(0).Gap(16).Children(func() {
					p.sitesSection(c, t, sites, running)
					p.dnsSection(c, t, running)
				})
				ui.Column(c).Grow(1).Basis(0).Gap(16).Children(func() {
					p.localSection(c, t)
					p.proxySection(c, t, st)
					p.leakSection(c, t, running)
				})
			})
		})
	})
	p.addDialog(c, t)
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
// 本机网络 and 代理状态 groups (docs/design.md §5.7 wireframe).
func (p *Page) panel(c *ui.Context, t *ui.Theme, title string, body func()) {
	ui.Column(c).Gap(6).Shrink(0).Children(func() {
		sectionTitle(c, t, title, "", nil)
		ui.Column(c).Padding(10, 14).Gap(6).Radius(10).Background(t.Surface).Border(1, t.Border).Children(body)
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

func (p *Page) card(c *ui.Context, t *ui.Theme, title string, right func(), body func()) {
	ui.Column(c).Grow(1).Basis(0).Padding(14).Gap(10).Radius(10).Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, title).Bold().FontSize(13).Grow(1)
			if right != nil {
				right()
			}
		})
		body()
	})
}

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

func (p *Page) directCard(c *ui.Context, t *ui.Theme) {
	e := p.e
	you := e.API.You()
	n := e.API.NetInfo()
	fetching := n.PublicIPStatus == "fetching"
	p.card(c, t, i18n.T("直连 · 本机公网"), func() {
		if you != nil && !fetching {
			e.Kit.Tag(c, i18n.T("实测"), "green")
		}
		if fetching {
			ui.Spinner(c).Size(12, 12).Label(i18n.T("正在获取"))
		} else {
			b := ui.Box(c).Padding(2).Radius(4).Cursor(ui.CursorPointer).TextColor(t.TextMuted).
				Role(ui.RoleButton).Label(i18n.T("重新获取公网地址")).Tooltip(i18n.T("重新获取公网地址"))
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
	p.card(c, t, title, func() {
		if main != nil {
			link := ui.Text(c, i18n.T("在地图中查看")).FontSize(11).TextColor(t.Accent).Cursor(ui.CursorPointer)
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
			p.kvCell(c, t, i18n.T("节点"), flag(x.Place)+main.Node, nil)
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
				p.kvCell(c, t, i18n.T("CF 机房"), dash(x.Colo), nil)
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
				text := fmt.Sprintf(i18n.T("入口 %s → 落地 %s"), entry, placeName(x.Place))
				ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(text)
			}
		case model.PathChain:
			ui.Text(c, fmt.Sprintf(i18n.T("链式代理 · %d 跳"), x.ChainHops)).FontSize(12).SingleLine()
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

func (p *Page) kv(c *ui.Context, t *ui.Theme, k, v string, extra func()) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, k).Width(56).Shrink(0).FontSize(12).TextColor(t.TextMuted).SingleLine()
		ui.Text(c, v).FontSize(12).FontFeatures("tnum").Selectable().SingleLine().Shrink(1)
		if extra != nil {
			extra()
		}
	})
}

// kvRich is kv with the value drawn by value (a dot and text, say).
func (p *Page) kvRich(c *ui.Context, t *ui.Theme, k string, value func()) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, k).Width(56).Shrink(0).FontSize(12).TextColor(t.TextMuted).SingleLine()
		ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(1).Children(value)
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
			add := ui.Text(c, i18n.T("＋ 添加网站")).FontSize(12).TextColor(t.Accent).Cursor(ui.CursorPointer).Role(ui.RoleButton)
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

func (p *Page) localSection(c *ui.Context, t *ui.Theme) {
	e := p.e
	n := e.API.NetInfo()
	p.panel(c, t, i18n.T("本机网络"), func() {
		ipv6 := n.IPv6
		if ipv6 == "" {
			ipv6 = i18n.T("未分配公网地址")
		}
		kvGrid(c, func() {
			p.kvCell(c, t, i18n.T("网络接口"), dash(n.Interface), nil)
			p.kvCell(c, t, i18n.T("Wi-Fi 名称"), dash(n.SSID), nil)
			p.kvCell(c, t, i18n.T("局域网 IP"), dash(n.LANIP), nil)
			p.kvCell(c, t, i18n.T("网关"), dash(n.Gateway), nil)
			p.kvCell(c, t, i18n.T("系统 DNS"), dash(strings.Join(n.DNS, ", ")), nil)
			p.kvCell(c, t, "IPv6", ipv6, nil)
		})
	})
}

func (p *Page) proxySection(c *ui.Context, t *ui.Theme, st model.Status) {
	e := p.e
	s := e.API.Settings()
	p.panel(c, t, i18n.T("代理状态"), func() {
		p.kvRich(c, t, i18n.T("系统代理"), func() {
			if st.SystemProxy {
				e.Kit.Dot(c, "green")
				ui.Text(c, fmt.Sprintf(i18n.T("指向澜流 127.0.0.1:%d"), st.MixedPort)).FontSize(12).FontFeatures("tnum").SingleLine()
			} else {
				e.Kit.Dot(c, "gray")
				ui.Text(c, i18n.T("未开启")).FontSize(12).TextColor(t.TextMuted)
			}
		})
		port := fmt.Sprintf(i18n.T("%d（HTTP + SOCKS5）"), st.MixedPort)
		if st.AllowLAN {
			port += " · " + i18n.T("允许局域网连接")
		}
		tun := i18n.T("未开启")
		if st.TUN {
			tun = i18n.T("已开启")
		}
		p.kv(c, t, i18n.T("监听端口"), port+" · "+i18n.T("TUN 模式")+" "+tun, nil)
		// 嗅探 and DNS report the effective config (docs/design.md:463): the
		// subscription's sections in 跟随订阅 mode, Lanflow's when taken over.
		snf := i18n.T("未开启")
		if s.Sniffer.Enable {
			snf = i18n.T("已开启") + " · " + sourceName(e.API.EffectiveSniffer().Source)
		}
		p.kv(c, t, i18n.T("嗅探"), snf, nil)
		p.kv(c, t, "DNS", dnsStatusText(e.API.EffectiveDNS()), nil)
		p.kv(c, t, i18n.T("控制接口"), i18n.T("本机套接字，仅澜流可访问"), nil)
		if a := st.ATrust; a.Running {
			p.kvRich(c, t, i18n.T("aTrust 兼容"), func() {
				e.Kit.Dot(c, "green")
				ui.Text(c, atrustSummary(a)).FontSize(12).FontFeatures("tnum").SingleLine()
			})
			ui.Text(c, atrustDetail(a)).FontSize(11).TextColor(t.TextMuted).Margin(0, 0, 0, 64).Selectable()
		}
	})
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

// ---- 泄漏检查 ----

func (p *Page) leakSection(c *ui.Context, t *ui.Theme, running bool) {
	e := p.e
	lk := e.API.LeakCheck()
	note := i18n.T("按需运行，不会自动检查")
	if !running {
		note = i18n.T("需要内核运行")
	} else if !lk.CheckedAt.IsZero() {
		note = i18n.Tf("上次检查 %s", lk.CheckedAt.Format("15:04:05"))
	}
	// The results take the height left in the column and scroll inside;
	// each line opens its details on a click.
	ui.Column(c).Gap(6).Grow(1).Basis(0).MinHeight(100).Children(func() {
		sectionTitle(c, t, i18n.T("泄漏检查"), note, func() {
			// The finer IP risk score is a third-party page: handed to the
			// browser, never requested by the app (also in 「检测」).
			link := ui.Text(c, i18n.T("在浏览器中检查 IP 纯净度")).FontSize(12).TextColor(t.Accent).Cursor(ui.CursorPointer).
				Role(ui.RoleButton).SingleLine().Tooltip(i18n.T("更细的 IP 风险评分（纯净度）可在「检测」页按需在线检测，也可在浏览器中查看"))
			if link.Clicked() {
				_ = mygo.Shell.OpenExternal(ipPurityURL)
			}
		})
		ui.Column(c).Grow(1).Radius(10).Background(t.Surface).Border(1, t.Border).Clip().Dividers(1, t.Border).Children(func() {
			ui.Row(c).Gap(8).PaddingX(12).MinHeight(40).AlignItems(ui.Center).Shrink(0).Children(func() {
				ui.TextInput(c.Key("leakdomain"), &p.leakDomain).Placeholder(i18n.T("附加对比的域名（可选）")).Font("monospace").Grow(1).
					Label(i18n.T("附加对比的域名（可选）")).Tooltip(i18n.T("系统解析器、强制 IPv6 的直连请求和直接发出的 STUN（UDP）各测一次，与内核和代理出口比较；并列出出口 IP 的归属与三路 DNS 结果"))
				if lk.Checking {
					ui.Spinner(c).Size(12, 12).Label(i18n.T("检查中…"))
					ui.Text(c, i18n.T("检查中…")).TextColor(t.TextMuted)
					return
				}
				label := i18n.T("开始检查")
				if len(lk.Items) > 0 {
					label = i18n.T("重新检查")
				}
				domain := strings.TrimSpace(p.leakDomain)
				ui.Button(c, label).Disabled(!running).Tooltip(i18n.T("检查 DNS、IPv6 和 WebRTC 是否绕过代理，以及出口 IP 的类型")).OnClick(func() {
					e.Run(func() error { return e.API.RunLeakCheckWith(domain) }, "")
				})
			})
			if lk.Error != "" {
				ui.Text(c, lk.Error).FontSize(12).TextColor(t.Danger).Padding(6, 12)
			}
			if len(lk.Items) == 0 {
				// No icon: the panel is short at the minimum window height.
				switch {
				case lk.Checking:
					e.Kit.Empty(c, "", i18n.T("检查中…"), i18n.T("正在比较系统解析器、内核和代理出口看到的地址。"), nil)
				case !running:
					e.Kit.Empty(c, "", i18n.T("内核未启动"), i18n.T("启动内核后可以检查 DNS、IPv6 和 WebRTC 是否绕过代理。"), nil)
				default:
					e.Kit.Empty(c, "", i18n.T("尚未检查"), i18n.T("检查 DNS、IPv6 和 WebRTC 是否绕过代理，以及出口 IP 的类型")+i18n.T("。"), nil)
				}
				return
			}
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Dividers(1, t.Border).Children(func() {
					for _, it := range lk.Items {
						p.leakRow(c, t, it)
					}
				})
			})
		})
	})
}

// ipPurityURL is the third-party IP risk-score site the leak section
// opens in the browser; the app itself never requests it.
const ipPurityURL = "https://ippure.com/"

// leakRow draws one result as a line (status dot, name, state, summary)
// that opens the structured rows and the explanation under it.
func (p *Page) leakRow(c *ui.Context, t *ui.Theme, it model.LeakItem) {
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
		parts := ui.CollapsibleBase(c.Key("coll"), open)
		parts.Trigger.Gap(8).PaddingX(12).MinHeight(32).AlignItems(ui.Center).Label(name + " · " + word).Children(func() {
			ui.Box(c).TextColor(t.TextMuted).Children(func() { e.Kit.Icon(c, "chevron", 12) }).Rotate(90 * parts.Progress())
			e.Kit.Dot(c, dot)
			ui.Text(c, name).Bold().FontSize(13).SingleLine().Shrink(0)
			ui.Text(c, word).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(0)
			ui.Text(c, it.Summary).FontSize(12).Grow(1).Shrink(1).SingleLine()
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

// ---- DNS query ----

func (p *Page) dnsSection(c *ui.Context, t *ui.Theme, running bool) {
	e := p.e
	p.mu.Lock()
	res, errText := p.dnsRes, p.dnsErr
	p.mu.Unlock()
	ui.Column(c).Gap(6).Shrink(0).Children(func() {
		sectionTitle(c, t, i18n.T("DNS 查询"), i18n.T("经内核的解析器"), func() {
			flush := func(label string, job func() error, ok string) {
				b := ui.Text(c, label).FontSize(12).TextColor(t.Accent).Cursor(ui.CursorPointer).Role(ui.RoleButton).SingleLine()
				if !running {
					b.TextColor(t.TextMuted).Cursor(ui.CursorDefault)
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

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
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
	return flag(pl) + placeName(pl)
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

// flag returns the regional-indicator flag of a place's country followed
// by a space, or "" when unknown.
func flag(pl *model.Place) string {
	if pl == nil || len(pl.Country) != 2 {
		return ""
	}
	cc := strings.ToUpper(pl.Country)
	var b strings.Builder
	for _, r := range cc {
		if r < 'A' || r > 'Z' {
			return ""
		}
		b.WriteRune(0x1F1E6 + r - 'A')
	}
	return b.String() + " "
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
