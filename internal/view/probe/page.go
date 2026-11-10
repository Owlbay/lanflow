// Package probe is the 检测 page (docs/design.md §5.9): whether this
// machine's traffic bypasses the proxy (泄漏检查), what the chosen exit
// looks like from the outside (IP 纯净度), which services it unlocks
// (解锁检测) and how the nodes of one region compare (地区探查). Every
// check runs only when the user presses its button; third-party sources
// are named next to their results.
package probe

import (
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// ippureURL is the third-party IP risk-score site the IP 纯净度 tab opens
// in the browser; the app itself requests only its free API, and only on
// 在线检测.
const ippureURL = "https://ippure.com/"

// Tabs of the page.
const (
	tabLeak = iota
	tabIP
	tabUnlock
	tabRegion
)

// tabKeys are the stored names of the tabs (model.UIState.ProbeTab), by tab.
var tabKeys = []string{"leak", "ip", "unlock", "region"}

// tabFromKey is the tab stored as k; the first one for "" or a key it
// does not know.
func tabFromKey(k string) int {
	for i, v := range tabKeys {
		if v == k {
			return i
		}
	}
	return tabLeak
}

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	tab         int
	loaded      bool   // tab restored from API.UIState
	target      string // node name or model.ProbeDirect; "" = follow the main group
	targetLabel string // the Select's current label

	region     string // ISO country chosen for 地区探查
	regionName string // the Select's current label
	withUnlock bool

	unlockList ui.ListState
	regionList ui.ListState
	regionSort ui.SortOrder

	leakDomain string           // optional domain for the DNS comparison of 泄漏检查
	leakOpen   map[string]*bool // 泄漏检查 items whose details are expanded, by name
}

// New returns the page.
func New(e *env.Env) *Page {
	p := &Page{e: e, regionSort: ui.SortOrder{Column: "delay"}, leakOpen: map[string]*bool{}}
	p.regionList.Sort = &p.regionSort
	return p
}

// restore loads the remembered tab once.
func (p *Page) restore() {
	if p.loaded {
		return
	}
	p.loaded = true
	p.tab = tabFromKey(p.e.API.UIState().ProbeTab)
}

// persist remembers the tab across restarts.
func (p *Page) persist() {
	key := tabKeys[p.tab]
	p.e.SaveUIState("probe", func(u *model.UIState) { u.ProbeTab = key })
}

// targetTab reports whether tab probes the exit chosen in the header:
// 泄漏检查 is about this machine and 地区探查 picks its own nodes.
func targetTab(tab int) bool { return tab == tabIP || tab == tabUnlock }

// targets lists the exits the user can choose: the direct connection and
// every node (not groups).
func targets(snap model.ProxySnapshot) []string {
	groups := map[string]bool{}
	for _, g := range snap.Groups {
		groups[g.Name] = true
	}
	var names []string
	for name, px := range snap.Proxies {
		if groups[name] || name == "DIRECT" || name == "REJECT" || name == "GLOBAL" || name == "LANFLOW-PROBE" || px.Type == "Selector" || px.Type == "URLTest" || px.Type == "Fallback" || px.Type == "LoadBalance" || px.Type == "Relay" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return append([]string{model.ProbeDirect}, names...)
}

// mainNode resolves the main group's current node, "" when none.
func mainNode(snap model.ProxySnapshot, st model.Status) string {
	name := snap.MainGroup
	if st.Mode == model.ModeGlobal {
		name = "GLOBAL"
	}
	for i := 0; i < 8; i++ {
		found := false
		for _, g := range snap.Groups {
			if g.Name == name {
				name, found = g.Now, true
				break
			}
		}
		if !found {
			break
		}
	}
	if name == "" || name == "DIRECT" || name == "REJECT" {
		return ""
	}
	if _, ok := snap.Proxies[name]; !ok {
		return ""
	}
	return name
}

// current is the exit the page probes: the user's choice, else the main
// group's node, else the direct connection.
func (p *Page) current(snap model.ProxySnapshot, st model.Status) string {
	if p.target != "" {
		if p.target == model.ProbeDirect {
			return p.target
		}
		if _, ok := snap.Proxies[p.target]; ok {
			return p.target
		}
	}
	if st.Core == model.CoreRunning && st.Mode != model.ModeDirect {
		if n := mainNode(snap, st); n != "" {
			return n
		}
	}
	return model.ProbeDirect
}

func targetLabel(t string) string {
	if t == model.ProbeDirect {
		return i18n.T("直连（本机）")
	}
	return t
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	e, t := p.e, c.Theme()
	p.restore()
	st := e.API.Status()
	snap := e.API.Proxies()
	running := st.Core == model.CoreRunning
	target := p.current(snap, st)
	ui.Column(c).Fill().Children(func() {
		e.Kit.Header(c, i18n.T("检测"), "", func() {
			if !targetTab(p.tab) {
				return
			}
			ui.Text(c, i18n.T("出口")).FontSize(12).TextColor(t.TextMuted)
			opts := targets(snap)
			labels := make([]string, len(opts))
			for i, o := range opts {
				labels[i] = targetLabel(o)
			}
			p.targetLabel = targetLabel(target)
			sel := ui.Select(c.Key("target"), &p.targetLabel, labels).Label(i18n.T("出口"))
			if sel.Changed() {
				for i, l := range labels {
					if l == p.targetLabel {
						p.target = opts[i]
					}
				}
			}
		})
		ui.Column(c).Grow(1).Padding(0, 24, 16, 24).Gap(12).Children(func() {
			if ui.Segmented(c.Key("tabs"), &p.tab, i18n.T("泄漏检查"), i18n.T("IP 纯净度"), i18n.T("解锁检测"), i18n.T("地区探查")).Label(i18n.T("检测项目")).Changed() {
				p.persist()
			}
			if targetTab(p.tab) && !running && target != model.ProbeDirect {
				e.Kit.Banner(c, "warn", i18n.T("内核未启动：只能检测直连出口。"), nil)
			}
			switch p.tab {
			case tabIP:
				p.ipTab(c, t, target)
			case tabUnlock:
				p.unlockTab(c, t, target)
			case tabRegion:
				p.regionTab(c, t, st, snap)
			default:
				p.leakTab(c, t, st)
			}
		})
	})
}

// ---- IP 纯净度 ----

// kv is a wrapping key-value line of the reports (env.KV).
func kv(c *ui.Context, k, v string, extra func()) { env.KV(c, k, v, 64, false, extra) }

func (p *Page) ipTab(c *ui.Context, t *ui.Theme, target string) {
	e := p.e
	rep, has := e.API.Probe().IP[target]
	start := func(online bool) {
		e.Run(func() error { return e.API.ProbeIP(target, online) }, "")
	}
	ui.Column(c).Grow(1).Gap(12).Children(func() {
		ui.Row(c).Gap(16).AlignItems(ui.Stretch).Shrink(0).Children(func() {
			env.InfoCard(c, i18n.T("出口信息")+" · "+targetLabel(target), func() {
				if rep.Running {
					ui.Spinner(c).Size(12, 12).Label(i18n.T("检测中…"))
					ui.Text(c, i18n.T("检测中…")).FontSize(11).TextColor(t.TextMuted)
					return
				}
				label := i18n.T("检测出口")
				if has {
					label = i18n.T("重新检测")
				}
				ui.Button(c, label).Tooltip(i18n.T("经所选出口请求 Cloudflare trace，并用本地数据库解析；不访问第三方站点")).OnClick(func() { start(false) })
			}, func() {
				switch {
				case !has:
					ui.Text(c, "—").FontSize(20).TextColor(t.TextMuted)
					ui.Text(c, i18n.T("尚未检测。按「检测出口」读取这个出口的地址、位置、ASN 和类型。")).FontSize(11).TextColor(t.TextMuted)
				case rep.Error != "":
					ui.Text(c, "—").FontSize(20).TextColor(t.TextMuted)
					ui.Text(c, rep.Error).FontSize(11).TextColor(t.Danger).Selectable()
				case rep.Running && rep.IP == "":
					ui.Text(c, "—").FontSize(20).TextColor(t.TextMuted)
				default:
					ui.Text(c, rep.IP).FontSize(20).FontFeatures("tnum").Selectable()
					ui.Column(c).Gap(5).Children(func() {
						kv(c, i18n.T("位置"), placeText(rep.Place), nil)
						kv(c, i18n.T("运营商"), orgText(rep.ASN, rep.Org), nil)
						kv(c, i18n.T("IP 类型"), "", func() { e.Kit.Tag(c, kindText(rep.Kind), kindTagKind(rep.Kind)) })
						kv(c, i18n.T("原生 IP"), "", func() {
							switch rep.Native {
							case model.NativeYes:
								e.Kit.Tag(c, i18n.T("原生"), "green")
							case model.NativeBroadcast:
								e.Kit.Tag(c, i18n.T("广播"), "orange")
							default:
								e.Kit.Tag(c, i18n.T("未知"), "gray")
							}
							if rep.NativeWhy != "" {
								ui.Text(c, rep.NativeWhy).FontSize(11).TextColor(t.TextMuted)
							}
						})
						if rep.Colo != "" {
							kv(c, i18n.T("CF 机房"), rep.Colo, nil)
						}
						kv(c, i18n.T("来源"), strings.Join(rep.Sources, " · "), nil)
						if !rep.CheckedAt.IsZero() {
							kv(c, i18n.T("时间"), rep.CheckedAt.Format("15:04:05"), nil)
						}
					})
				}
			})
			env.InfoCard(c, i18n.T("风险评估"), func() {
				// The finer score in the browser: handed to it, never
				// requested by the app.
				link := env.LinkText(c, i18n.T("在浏览器中检查 IP 纯净度"), "").FontSize(11).Tooltip(i18n.T("在浏览器中打开 ippure.com，查看更细的 IP 风险评分"))
				if link.Clicked() {
					_ = mygo.Shell.OpenExternal(ippureURL)
				}
				if rep.Running {
					return
				}
				ui.Button(c, i18n.T("在线检测")).Disabled(!has || rep.Error != "").
					Tooltip(i18n.T("经所选出口各请求一次 ip-api.com 和 ippure.com 的免费接口（无需密钥）")).
					OnClick(func() { start(true) })
			}, func() {
				switch {
				case !has || rep.Error != "":
					ui.Text(c, i18n.T("先检测出口信息，再按「在线检测」读取公开的风险评分。")).FontSize(11).TextColor(t.TextMuted)
				case len(rep.Risk) == 0 && rep.RiskError == "":
					ui.Text(c, i18n.T("「在线检测」会经所选出口各向 ip-api.com（代理 / 机房 / 移动网络标记）和 ippure.com（风险分、住宅、广播）发送一次请求。只在你按下按钮时发送。")).FontSize(11).TextColor(t.TextMuted)
				default:
					ui.Column(c).Gap(6).Children(func() {
						for _, r := range rep.Risk {
							p.riskRows(c, t, r)
						}
						if rep.RiskError != "" {
							ui.Text(c, i18n.Tf("在线来源读取失败：%s", rep.RiskError)).FontSize(11).TextColor(t.Warning).Selectable()
							ui.Button(c, i18n.T("在 ippure.com 查看")).OnClick(func() { _ = mygo.Shell.OpenExternal(ippureURL) })
						}
					})
				}
			})
		})
		if !has && !rep.Running {
			// First view: what the three tabs do and that nothing runs on its
			// own, with the one primary action of the page.
			e.Kit.Empty(c, "probe", i18n.T("按需检测，不会自动运行"),
				i18n.T("IP 纯净度读取出口的地址、位置、运营商和风险评分；解锁检测访问常用服务各一次；地区探查对比同一地区各节点的延迟和真实出口。只在你按下按钮时发出请求。"),
				func() {
					ui.PrimaryButton(c, i18n.T("检测出口")).OnClick(func() { start(false) })
				})
		}
	})
}

// riskRows draws one online source's verdicts with the source named.
func (p *Page) riskRows(c *ui.Context, t *ui.Theme, r model.RiskInfo) {
	e := p.e
	yesNo := func(v bool) (string, string) {
		if v {
			return i18n.T("是"), "orange"
		}
		return i18n.T("否"), "green"
	}
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, i18n.Tf("来源 %s", r.Source)).FontSize(11).FontWeight(600).TextColor(t.TextMuted)
		if r.HasScore {
			kv(c, i18n.T("风险分"), "", func() {
				kind := "green"
				switch {
				case r.Score >= 70:
					kind = "red"
				case r.Score >= 35:
					kind = "orange"
				}
				e.Kit.Tag(c, fmt.Sprintf("%d / 100", r.Score), kind)
				ui.Text(c, i18n.T("越低越纯净")).FontSize(11).TextColor(t.TextMuted)
			})
			kv(c, i18n.T("住宅 IP"), "", func() {
				if r.Residential {
					e.Kit.Tag(c, i18n.T("是"), "green")
				} else {
					e.Kit.Tag(c, i18n.T("否"), "gray")
				}
			})
			if r.HasBroadcast {
				kv(c, i18n.T("广播 IP"), "", func() {
					w, k := yesNo(r.Broadcast)
					e.Kit.Tag(c, w, k)
				})
			}
		}
		if r.HasFlags {
			kv(c, i18n.T("代理 / VPN"), "", func() { w, k := yesNo(r.Proxy); e.Kit.Tag(c, w, k) })
			kv(c, i18n.T("机房"), "", func() { w, k := yesNo(r.Hosting); e.Kit.Tag(c, w, k) })
			kv(c, i18n.T("移动网络"), "", func() {
				if r.Mobile {
					e.Kit.Tag(c, i18n.T("是"), "accent")
				} else {
					e.Kit.Tag(c, i18n.T("否"), "gray")
				}
			})
		}
		if r.RegionOfBlock != "" {
			kv(c, i18n.T("判定地区"), countryText(r.RegionOfBlock), nil)
		}
	})
}

// ---- 解锁检测 ----

var unlockCols = []ui.TableColumn{
	{Title: "服务", ID: "service", Width: 140},
	{Title: "结果", ID: "status", Width: 90},
	{Title: "地区", ID: "region", Width: 90},
	{Title: "说明", ID: "note", MinWidth: 160},
	{Title: "耗时", ID: "ms", Width: 70, Align: ui.End},
}

func (p *Page) unlockTab(c *ui.Context, t *ui.Theme, target string) {
	e := p.e
	rep, has := e.API.Probe().Unlock[target]
	ui.Column(c).Grow(1).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("解锁检测")+" · "+targetLabel(target)).Bold()
			ui.Text(c, i18n.T("· 经所选出口各访问一次，不登录账号")).FontSize(11).TextColor(t.TextMuted)
			ui.Spacer(c)
			switch {
			case rep.Running:
				ui.Spinner(c).Size(12, 12).Label(i18n.T("检测中…"))
				ui.Text(c, i18n.Tf("%d / %d", rep.Done, len(rep.Results))).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
			default:
				if has && !rep.CheckedAt.IsZero() {
					ui.Text(c, i18n.Tf("上次检测 %s", rep.CheckedAt.Format("15:04:05"))).FontSize(11).TextColor(t.TextMuted)
				}
				label := i18n.T("开始检测")
				if has {
					label = i18n.T("重新检测")
				}
				ui.PrimaryButton(c, label).OnClick(func() {
					e.Run(func() error { return e.API.ProbeUnlock(target) }, "")
				})
			}
		})
		if rep.Error != "" {
			e.Kit.Banner(c, "error", rep.Error, nil)
		}
		if !has {
			e.Kit.Empty(c, "probe", i18n.T("尚未检测"), i18n.T("检测 ChatGPT、Claude、Gemini、Netflix、YouTube Premium、Disney+、TikTok、Spotify、Google 搜索和 Bing 在这个出口的可用性与判定地区。"), nil)
			return
		}
		rows := rep.Results
		p.unlockList.Key = func(i int) any { return rows[i].Service }
		ui.Table(c.Key("unlock"), &p.unlockList, env.I18nColumns(unlockCols), len(rows), func(row, col int) {
			r := rows[row]
			switch unlockCols[col].ID {
			case "service":
				ui.Text(c, r.Service).SingleLine()
			case "status":
				p.statusCell(c, t, r)
			case "region":
				ui.Text(c, countryText(r.Region)).SingleLine()
			case "note":
				ui.Text(c, env.Dash(r.Note)).TextColor(t.TextMuted).SingleLine()
			case "ms":
				if r.Millis > 0 {
					ui.Text(c, fmt.Sprintf("%d ms", r.Millis)).FontFeatures("tnum").TextColor(t.TextMuted)
				} else {
					ui.Text(c, "—").TextColor(t.TextMuted)
				}
			}
		}).FillWidth().Radius(8).Border(1, t.Border).Label(i18n.T("解锁检测"))
	})
}

// statusCell draws an unlock verdict as a colored word.
func (p *Page) statusCell(c *ui.Context, t *ui.Theme, r model.UnlockResult) {
	e := p.e
	switch r.Status {
	case model.UnlockYes:
		e.Kit.Tag(c, i18n.T("可用"), "green")
	case model.UnlockNo:
		e.Kit.Tag(c, i18n.T("不可用"), "red")
	case model.UnlockPartial:
		e.Kit.Tag(c, i18n.T("仅自制剧"), "orange")
	case model.UnlockError:
		e.Kit.Tag(c, i18n.T("失败"), "gray")
	default:
		ui.Spinner(c).Size(12, 12).Label(i18n.T("检测中…"))
	}
}

// ---- 地区探查 ----

var regionCols = []ui.TableColumn{
	{Title: "节点", ID: "node", MinWidth: 140, Sortable: true},
	{Title: "延迟", ID: "delay", Width: 80, Align: ui.End, Sortable: true},
	{Title: "出口 IP", ID: "ip", Width: 150, Sortable: true},
	{Title: "出口位置", ID: "place", Width: 120, Sortable: true},
	{Title: "解锁", ID: "unlock", Width: 110, Sortable: true},
	{Title: "操作", ID: "action", Width: 90, Fixed: true},
}

// regionRow is a 地区探查 row with the live delay merged in.
type regionRow struct {
	model.RegionNode
	unlocked, total int
}

func (p *Page) regionTab(c *ui.Context, t *ui.Theme, st model.Status, snap model.ProxySnapshot) {
	e := p.e
	regions := e.API.ProbeRegions()
	rep := e.API.Probe().Region
	running := st.Core == model.CoreRunning
	// Keep the choice valid: default to the region with the most nodes.
	labels := make([]string, len(regions))
	byLabel := map[string]string{}
	found := false
	for i, r := range regions {
		labels[i] = fmt.Sprintf("%s%s (%d)", env.FlagPrefix(r.Country), countryText(r.Country), len(r.Nodes))
		byLabel[labels[i]] = r.Country
		if r.Country == p.region {
			p.regionName, found = labels[i], true
		}
	}
	if !found && len(regions) > 0 {
		p.region, p.regionName = regions[0].Country, labels[0]
	}
	ui.Column(c).Grow(1).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("地区")).FontSize(12).TextColor(t.TextMuted)
			if len(regions) == 0 {
				ui.Text(c, i18n.T("没有能从名称判断地区的节点")).FontSize(12).TextColor(t.TextMuted)
			} else {
				sel := ui.Select(c.Key("region"), &p.regionName, labels).Label(i18n.T("地区"))
				if sel.Changed() {
					if cc, ok := byLabel[p.regionName]; ok {
						p.region = cc
					}
				}
			}
			ui.Checkbox(c, &p.withUnlock, i18n.T("同时检测解锁"))
			ui.Spacer(c)
			switch {
			case rep.Running:
				ui.Spinner(c).Size(12, 12).Label(i18n.T("探查中…"))
				ui.Text(c, i18n.Tf("%d / %d", rep.Done, len(rep.Nodes))).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
			default:
				if !rep.CheckedAt.IsZero() {
					ui.Text(c, i18n.Tf("上次探查 %s", rep.CheckedAt.Format("15:04:05"))).FontSize(11).TextColor(t.TextMuted)
				}
				region, withUnlock := p.region, p.withUnlock
				ui.PrimaryButton(c, i18n.T("开始探查")).Disabled(!running || len(regions) == 0).OnClick(func() {
					e.Run(func() error { return e.API.ProbeRegion(region, withUnlock) }, "")
				})
			}
		})
		if rep.Error != "" {
			e.Kit.Banner(c, "error", rep.Error, nil)
		}
		if len(rep.Nodes) == 0 {
			title, desc := i18n.T("尚未探查"), i18n.T("对所选地区的每个节点测延迟、实测出口地址，并可同时检测解锁；点「设为当前」把节点切换到主代理组。")
			if len(regions) == 0 {
				title, desc = i18n.T("没有可探查的地区"), i18n.T("地区按节点名称判断（如「香港 01」「JP Tokyo」）。订阅里的节点名都不含地区时，这里没有可选项。")
			}
			e.Kit.Empty(c, "probe", title, desc, nil)
			return
		}
		rows := make([]regionRow, len(rep.Nodes))
		for i, n := range rep.Nodes {
			if px, ok := snap.Proxies[n.Node]; ok && (n.Testing || n.Delay == 0) {
				n.Delay, n.Testing = px.Delay, px.Testing || n.Testing
			}
			r := regionRow{RegionNode: n}
			for _, u := range n.Unlock {
				r.total++
				if u.Status == model.UnlockYes || u.Status == model.UnlockPartial {
					r.unlocked++
				}
			}
			rows[i] = r
		}
		sortRows(rows, p.regionSort)
		mainGroup := snap.MainGroup
		cur := mainNode(snap, st)
		p.regionList.Key = func(i int) any { return rows[i].Node }
		ui.Table(c.Key("region"), &p.regionList, env.I18nColumns(regionCols), len(rows), func(row, col int) {
			r := rows[row]
			switch regionCols[col].ID {
			case "node":
				ui.Text(c, r.Node).SingleLine()
			case "delay":
				e.Kit.Delay(c, r.Delay, r.Testing)
			case "ip":
				switch {
				case r.Error != "":
					ui.Text(c, i18n.T("失败")).TextColor(t.Danger).Tooltip(r.Error)
				case !r.Probed:
					ui.Text(c, "—").TextColor(t.TextMuted)
				default:
					ui.Text(c, r.Exit.IP).FontFeatures("tnum").SingleLine().Selectable()
				}
			case "place":
				if r.Probed {
					ui.Text(c, placeText(r.Exit.Place)).SingleLine()
				} else {
					ui.Text(c, "—").TextColor(t.TextMuted)
				}
			case "unlock":
				switch {
				case !rep.WithUnlock:
					ui.Text(c, "—").TextColor(t.TextMuted)
				case r.total == 0:
					ui.Text(c, "—").TextColor(t.TextMuted)
				default:
					kind := "green"
					if r.unlocked < r.total {
						kind = "orange"
					}
					if r.unlocked == 0 {
						kind = "red"
					}
					e.Kit.Tag(c, i18n.Tf("%d / %d 可用", r.unlocked, r.total), kind)
				}
			case "action":
				if r.Node == cur {
					e.Kit.Tag(c, i18n.T("当前"), "accent")
					return
				}
				node := r.Node
				b := env.LinkText(c, i18n.T("设为当前"), i18n.Tf("设为当前 %s", node)).Tooltip(i18n.Tf("在「%s」中选择「%s」", mainGroup, node))
				if b.Clicked() && mainGroup != "" {
					e.Run(func() error { return e.API.SelectProxy(mainGroup, node) }, i18n.Tf("已切换到「%s」", node))
				}
			}
		}).FillWidth().Radius(8).Border(1, t.Border).Label(i18n.T("地区探查"))
	})
}

// sortRows orders the region rows by s; untested delays sort last.
func sortRows(rows []regionRow, s ui.SortOrder) {
	less := func(a, b regionRow) bool {
		switch s.Column {
		case "delay":
			da, db := delayRank(a.Delay), delayRank(b.Delay)
			if da != db {
				return da < db
			}
		case "ip":
			if a.Exit.IP != b.Exit.IP {
				return a.Exit.IP < b.Exit.IP
			}
		case "place":
			pa, pb := placeText(a.Exit.Place), placeText(b.Exit.Place)
			if pa != pb {
				return pa < pb
			}
		case "unlock":
			if a.unlocked != b.unlocked {
				return a.unlocked > b.unlocked
			}
		}
		return a.Node < b.Node
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if s.Descending {
			return less(rows[j], rows[i])
		}
		return less(rows[i], rows[j])
	})
}

// delayRank orders delays: measured ascending, then untested, then timeouts.
func delayRank(ms int) int {
	switch {
	case ms > 0:
		return ms
	case ms == 0:
		return 1 << 30
	}
	return 1<<30 + 1
}

// ---- helpers ----

// countryText names a country for the UI: the Chinese name in Chinese,
// the ISO code in English (the geo tables carry no English names).
func countryText(cc string) string {
	if cc == "" {
		return "—"
	}
	if i18n.Lang() == i18n.EN {
		return cc
	}
	if n := geo.CountryName(cc); n != "" {
		return n
	}
	return cc
}

func placeText(pl *model.Place) string {
	if pl == nil {
		return i18n.T("未知")
	}
	name := pl.City
	if name == "" {
		name = countryText(pl.Country)
	}
	return env.FlagPrefix(pl.Country) + name
}

func orgText(asn, org string) string {
	switch {
	case asn == "" && org == "":
		return "—"
	case org == "":
		return asn
	case asn == "":
		return org
	}
	return org + " " + asn
}

func kindText(kind string) string {
	switch kind {
	case model.IPKindDatacenter:
		return i18n.T("机房")
	case model.IPKindResidential:
		return i18n.T("住宅 / 运营商")
	case model.IPKindMobile:
		return i18n.T("移动网络")
	}
	return i18n.T("未知")
}

func kindTagKind(kind string) string {
	switch kind {
	case model.IPKindDatacenter:
		return "orange"
	case model.IPKindResidential, model.IPKindMobile:
		return "green"
	}
	return "gray"
}

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own). TopicNet carries the
// 泄漏检查 results (API.LeakCheck).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicProbe, app.TopicProxies, app.TopicNet}
}
