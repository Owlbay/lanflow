// Package proxies is the 代理 page (docs/design.md §5.2): collapsible
// proxy groups, node picking for select groups, delay tests, search and
// sort.
package proxies

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// Sort orders.
const (
	sortDefault = iota
	sortDelay
	sortName
)

// Views of a group's nodes.
const (
	viewList = iota
	viewGrid
)

// gridColumns is the compact grid's column count (docs/design.md §5.2: 3–4 列).
const gridColumns = 3

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	query       string
	search      ui.Handle
	sort        int
	hideTimeout bool
	view        int                       // viewList or viewGrid, remembered
	open        map[string]bool           // group expanded state, remembered across restarts
	openInit    map[string]bool           // groups whose default state was decided
	loaded      bool                      // open/view restored from API.UIState
	provOpen    bool                      // 代理集合 section expanded
	scrollTo    string                    // group to reveal after Navigate
	locate      locateState               // 定位当前节点 (locate.go)
	icons       map[string]ui.ImageSource // decoded group icons by reference (locate.go)
	iconFail    map[string]bool           // references that do not decode
	lists       map[string]*ui.ListState  // the virtual lists of large groups, by group

	mu      sync.Mutex
	pending map[string]string // optimistic selection by group
	testing bool              // 全部测速 running
}

// New returns the page.
func New(e *env.Env) *Page {
	return &Page{e: e, open: map[string]bool{}, openInit: map[string]bool{}, pending: map[string]string{},
		icons: map[string]ui.ImageSource{}, iconFail: map[string]bool{}, lists: map[string]*ui.ListState{}}
}

// restore loads the remembered expanded states and view once.
func (p *Page) restore() {
	if p.loaded {
		return
	}
	p.loaded = true
	u := p.e.API.UIState()
	for name, open := range u.OpenGroups {
		p.open[name] = open
		p.openInit[name] = true
	}
	if u.ProxyGrid {
		p.view = viewGrid
	}
}

// persist saves the expanded states and view off the UI thread, keeping
// what other pages remember in the UI state.
func (p *Page) persist() {
	open := make(map[string]bool, len(p.open))
	for name, o := range p.open {
		open[name] = o
	}
	grid := p.view == viewGrid
	p.e.SaveUIState("proxies", func(u *model.UIState) {
		u.OpenGroups, u.ProxyGrid = open, grid
	})
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	k := p.e.Kit
	st := p.e.API.Status()
	px := p.e.API.Proxies()
	run := st.Core == model.CoreRunning
	p.restore()
	if arg := p.e.Arg(env.Proxies); arg != "" {
		p.scrollTo = arg
		p.open[arg] = true
		p.openInit[arg] = true
	}
	p.mu.Lock()
	testingAll := p.testing
	p.mu.Unlock()

	k.Header(c, i18n.T("代理"), "", func() {
		if !st.HasSubscriptions {
			return
		}
		ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索节点")).Label(i18n.T("搜索节点")).Width(180).Bind(&p.search)
		if ui.Segmented(c.Key("view"), &p.view, i18n.T("列表"), i18n.T("网格")).Label(i18n.T("视图")).Changed() {
			p.persist()
		}
		ui.MenuButton(c, i18n.T("排序"), func(m *ui.Menu) {
			for i, name := range []string{i18n.T("默认顺序"), i18n.T("按延迟"), i18n.T("按名称")} {
				if m.Item(name).Radio(p.sort == i).Chosen() {
					p.sort = i
				}
			}
			m.Separator()
			if m.Item(i18n.T("隐藏超时节点")).Checked(p.hideTimeout).Chosen() {
				p.hideTimeout = !p.hideTimeout
			}
		}).Label(i18n.T("排序"))
		tip := i18n.T("依次测试所有组的延迟（⌘R）")
		if !p.e.Mac {
			tip = i18n.T("依次测试所有组的延迟（Ctrl+R）")
		}
		b := ui.Button(c, "").Label(i18n.T("全部测速")).Tooltip(tip).Disabled(!run || testingAll)
		b.Children(func() {
			if testingAll {
				ui.Spinner(c).Size(12, 12)
			} else {
				k.Icon(c, "bolt", 13)
			}
			ui.Text(c, i18n.T("全部测速")).SingleLine()
		})
		if b.Clicked() {
			p.testAll()
		}
	})
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		p.search.Focus(c)
	}
	if run && c.Shortcut(ui.Cmd, ui.KeyR) {
		p.testAll()
	}

	switch {
	case !st.HasSubscriptions:
		k.Empty(c, "proxies", i18n.T("还没有订阅"), i18n.T("导入订阅后，这里会列出代理组和节点。"), func() {
			if ui.PrimaryButton(c, i18n.T("导入订阅")).Clicked() {
				p.e.Navigate(env.Subs, "import-url")
			}
		})
		return
	case !run:
		k.Empty(c, "power", i18n.T("内核未启动"), i18n.T("启动内核后才能查看和切换节点。"), func() {
			if st.Core == model.CoreStarting {
				ui.Spinner(c).Label(i18n.T("启动中"))
				return
			}
			if ui.PrimaryButton(c, i18n.T("启动内核")).Clicked() {
				p.e.Run(p.e.API.StartCore, i18n.T("内核已启动"))
			}
		})
		return
	}

	ui.Scroll(c).Grow(1).Children(func() {
		body := ui.Column(c).Padding(0, 24, 20, 24).Gap(12)
		body.Children(func() {
			if st.Mode == model.ModeDirect {
				k.Banner(c, "warn", i18n.T("当前为直连模式，节点选择不生效。"), func() {
					if ui.Button(c, i18n.T("切换到规则模式")).Clicked() {
						p.e.Run(func() error { return p.e.API.SetMode(model.ModeRule) }, i18n.T("已切换到规则模式"))
					}
				})
			}
			groups := p.visibleGroups(px, st)
			names := groupNames(px)
			shown := 0
			list := ui.Column(c).Gap(10)
			if st.Mode == model.ModeDirect {
				list.Opacity(0.6)
			}
			list.Children(func() {
				for _, g := range groups {
					// Only an expanded group's members are filtered and
					// sorted; a collapsed one only counts its matches while
					// searching, to hide a group with none.
					var nodes []string
					matched := len(g.All)
					if p.isOpen(g, px) {
						nodes = p.filterNodes(g, px)
						matched = len(nodes)
					} else if p.query != "" {
						matched = p.countMatches(g)
					}
					if p.query != "" && matched == 0 {
						continue
					}
					shown++
					p.group(c, g, nodes, matched, px, names)
				}
			})
			if shown == 0 && p.query != "" {
				k.Empty(c, "search", fmt.Sprintf(i18n.T("没有匹配“%s”的节点"), p.query), i18n.T("换个关键词试试。"), nil)
			}
			if provs := p.e.API.ProxyProviders(); len(provs) > 0 && p.query == "" {
				p.providers(c, provs, px)
			}
		})
	})
}

// visibleGroups returns the groups in config order, GLOBAL first in
// global mode and hidden otherwise.
func (p *Page) visibleGroups(px model.ProxySnapshot, st model.Status) []model.ProxyGroup {
	var out []model.ProxyGroup
	for _, g := range px.Groups {
		if g.Name == "GLOBAL" {
			if st.Mode == model.ModeGlobal {
				out = append([]model.ProxyGroup{g}, out...)
			}
			continue
		}
		if g.Hidden {
			continue
		}
		out = append(out, g)
	}
	return out
}

// groupNames is the set of the group names, which members may be.
func groupNames(px model.ProxySnapshot) map[string]bool {
	set := make(map[string]bool, len(px.Groups))
	for _, g := range px.Groups {
		set[g.Name] = true
	}
	return set
}

// countMatches counts the members of g whose name has the search in it.
func (p *Page) countMatches(g model.ProxyGroup) int {
	q := strings.ToLower(strings.TrimSpace(p.query))
	n := 0
	for _, m := range g.All {
		if strings.Contains(strings.ToLower(m), q) {
			n++
		}
	}
	return n
}

// filterNodes applies the search, the timeout filter and the sort.
func (p *Page) filterNodes(g model.ProxyGroup, px model.ProxySnapshot) []string {
	q := strings.ToLower(strings.TrimSpace(p.query))
	var nodes []string
	for _, n := range g.All {
		if q != "" && !strings.Contains(strings.ToLower(n), q) {
			continue
		}
		if p.hideTimeout && px.Proxies[n].Delay < 0 {
			continue
		}
		nodes = append(nodes, n)
	}
	switch p.sort {
	case sortDelay:
		dv := func(n string) int {
			pr := px.Proxies[n]
			switch {
			case pr.Testing || pr.Delay == 0:
				return 1e7
			case pr.Delay < 0:
				return 1e6
			default:
				return pr.Delay
			}
		}
		sort.SliceStable(nodes, func(i, j int) bool { return dv(nodes[i]) < dv(nodes[j]) })
	case sortName:
		sort.SliceStable(nodes, func(i, j int) bool { return nodes[i] < nodes[j] })
	}
	return nodes
}

func groupTypeName(t model.GroupType) string {
	switch t {
	case model.GroupSelect:
		return i18n.T("选择")
	case model.GroupURLTest:
		return i18n.T("自动测速")
	case model.GroupFallback:
		return i18n.T("故障转移")
	case model.GroupLoadBalance:
		return i18n.T("负载均衡")
	case model.GroupRelay:
		return i18n.T("链式代理")
	default:
		return string(t)
	}
}

// isOpen returns whether a group is expanded: by default only the main
// group is.
func (p *Page) isOpen(g model.ProxyGroup, px model.ProxySnapshot) bool {
	if !p.openInit[g.Name] {
		p.openInit[g.Name] = true
		p.open[g.Name] = g.Name == px.MainGroup || g.Name == "GLOBAL"
	}
	return p.open[g.Name]
}

// bigGroup is the member count from which the list view shows a group's
// members in a virtual list bigGroupRows tall, which builds only the rows
// in view.
const (
	bigGroup     = 60
	bigGroupRows = 12
	nodeRowH     = 32
)

// group draws one collapsible group with its header controls; nodes are
// its members shown when it is open, matched how many match the search.
func (p *Page) group(c *ui.Context, g model.ProxyGroup, nodes []string, matched int, px model.ProxySnapshot, names map[string]bool) {
	t := c.Theme()
	k := p.e.Kit
	open := p.isOpen(g, px)
	o := open
	p.mu.Lock()
	pendingNow := p.pending[g.Name]
	p.mu.Unlock()
	now := g.Now
	if pendingNow != "" {
		now = pendingNow
	}
	groupTesting := false
	for _, n := range g.All {
		if px.Proxies[n].Testing {
			groupTesting = true
			break
		}
	}
	selectable := g.Type == model.GroupSelect

	box := ui.Column(c.Key("group:"+g.Name)).Radius(10).Background(t.Surface).Border(1, t.Border).Clip()
	if p.scrollTo == g.Name {
		box.ScrollIntoView()
		p.scrollTo = ""
	}
	box.Children(func() {
		var parts ui.CollapsibleParts
		ui.Row(c).AlignItems(ui.Center).Gap(4).Padding(0, 8, 0, 0).Children(func() {
			parts = ui.CollapsibleBase(c.Key("coll"), &o)
			parts.Trigger.Grow(1).MinHeight(40).Padding(6, 12).Gap(8).AlignItems(ui.Center).Label(g.Name).Children(func() {
				ui.Box(c).TextColor(t.TextMuted).Children(func() { k.Icon(c, "chevron", 12) }).Rotate(90 * parts.Progress())
				p.groupIcon(c, g.Icon)
				ui.Text(c, g.Name).FontSize(13).FontWeight(600).SingleLine()
				if g.Name == px.MainGroup {
					k.Tag(c, i18n.T("主代理组"), "gray")
				}
				meta := groupTypeName(g.Type)
				if now != "" {
					meta = i18n.Tf("%s · 当前 %s", meta, now)
				}
				if p.query != "" {
					meta = i18n.Tf("%s · %d 个匹配", meta, matched)
				}
				ui.Text(c, meta).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(1)
			})
			if now != "" {
				loc := ui.Button(c, "").Label(i18n.T("定位当前节点 ") + g.Name).Tooltip(i18n.T("定位当前节点")).Children(func() {
					k.Icon(c, "locate", 14)
				})
				if loc.Clicked() {
					p.locateNow(c, g.Name)
				}
			}
			test := ui.Button(c, "").Label(i18n.T("测速 ") + g.Name).Tooltip(i18n.T("组内测速")).Disabled(groupTesting).Children(func() {
				if groupTesting {
					ui.Spinner(c).Size(12, 12)
				} else {
					k.Icon(c, "bolt", 14)
				}
			})
			if test.Clicked() {
				name := g.Name
				p.e.Run(func() error { return p.e.API.TestGroupDelay(name) }, "")
			}
			ui.Button(c, "").Label(i18n.T("更多 ") + g.Name).Tooltip(i18n.T("更多")).Children(func() { k.Icon(c, "more", 14) }).Menu(func(m *ui.Menu) {
				if m.Item(i18n.T("设为主代理组")).Checked(g.Name == px.MainGroup).Disabled(!selectable).Chosen() {
					name := g.Name
					p.e.Run(func() error { return p.e.API.SetMainGroup(name) }, i18n.Tf("已将「%s」设为主代理组", name))
				}
				if m.Item(i18n.T("复制组名")).Chosen() {
					c.WriteClipboard(g.Name)
					p.e.Toast(i18n.T("已复制"), false)
				}
			})
		})
		if o != open {
			p.open[g.Name] = o
			p.persist()
		}
		parts.Panel(func() {
			ui.Column(c).Padding(0, 6, 6, 6).Children(func() {
				ui.Box(c).Height(1).Background(t.Border).Margin(0, 6, 4, 6)
				if p.view == viewGrid {
					ui.Grid(c).Columns(gridColumns).Gap(6).Padding(4, 6, 2, 6).Children(func() {
						for _, n := range nodes {
							p.cell(c, g, n, now, selectable, px, names)
						}
					})
					return
				}
				if len(nodes) > bigGroup {
					p.bigList(c, g, nodes, now, selectable, px, names)
					return
				}
				for _, n := range nodes {
					p.node(c, g, n, now, selectable, px, names)
				}
			})
		})
	})
}

// bigList shows a large group's members in a virtual list, scrolled to
// the current member when 定位当前节点 asks for it.
func (p *Page) bigList(c *ui.Context, g model.ProxyGroup, nodes []string, now string, selectable bool, px model.ProxySnapshot, names map[string]bool) {
	ls := p.lists[g.Name]
	if ls == nil {
		ls = &ui.ListState{}
		p.lists[g.Name] = ls
	}
	ls.Key = func(i int) any { return nodes[i] }
	if l := p.locate; l.group == g.Name && !l.shown && !l.until.IsZero() {
		if i := slices.Index(nodes, now); i >= 0 {
			ls.ScrollTo(i, ui.Center)
		}
	}
	ui.List(c.Key("nodes"), ls, len(nodes), func(i int) {
		p.node(c, g, nodes[i], now, selectable, px, names)
	}).Height(bigGroupRows * nodeRowH).FillWidth().Label(g.Name)
}

// memberKind classifies a group member for display: builtin (DIRECT/REJECT),
// nested group (one of names), or node with its protocol.
func memberKind(n string, px model.ProxySnapshot, names map[string]bool) (proto string, isGroup, builtin bool) {
	if names[n] {
		return i18n.T("代理组"), true, false
	}
	if n == "DIRECT" || n == "REJECT" {
		return i18n.T("内置"), false, true
	}
	if pr, known := px.Proxies[n]; known {
		return pr.Type, false, false
	}
	return "", false, false
}

// cell draws one member as a compact grid tile: name on top, protocol and
// delay below; the current member is tinted and gets the dot/标签 as in the
// list.
func (p *Page) cell(c *ui.Context, g model.ProxyGroup, n, now string, selectable bool, px model.ProxySnapshot, names map[string]bool) {
	t := c.Theme()
	k := p.e.Kit
	pr := px.Proxies[n]
	proto, isGroup, builtin := memberKind(n, px, names)
	cur := n == now
	var tile ui.Element
	if selectable {
		tile = ui.ButtonBase(c.Key("cell:" + n)).Label(n).Cursor(ui.CursorPointer)
		if tile.Hovered() {
			tile.Background(t.SurfaceHover)
		}
	} else {
		tile = ui.Column(c.Key("cell:" + n))
	}
	tile.Padding(8, 10).Gap(4).Radius(8).Border(1, t.Border).AlignItems(ui.Start).MinHeight(56)
	if cur {
		tile.Background(t.Accent.Alpha(0.08)).Border(1, t.Accent.Alpha(0.5))
	}
	if reveal, tint := p.locating(c, g.Name, cur); tint {
		tile.Background(t.Accent.Alpha(0.28))
		if reveal {
			tile.ScrollIntoView(ui.ScrollIntoViewOptions{Block: ui.ScrollCenter, Animation: &ui.Smooth})
		}
	}
	tile.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).FillWidth().Children(func() {
			if selectable {
				radio := ui.Box(c).Size(10, 10).Radius(5).Shrink(0)
				if cur {
					radio.Border(3, t.Accent).Background(t.Background)
				} else {
					radio.Border(1.5, t.TextMuted.Alpha(0.6))
				}
			}
			ui.Text(c, n).FontSize(12).FontWeight(500).SingleLine().Shrink(1)
			if !selectable && cur {
				ui.Spacer(c)
				k.Tag(c, i18n.T("当前"), "accent")
			}
		})
		ui.Row(c).Gap(6).AlignItems(ui.Center).FillWidth().Children(func() {
			if proto != "" {
				ui.Text(c, proto).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
			}
			ui.Spacer(c)
			if isGroup || builtin {
				ui.Text(c, "—").FontSize(12).TextColor(t.TextMuted)
				return
			}
			d := ui.ButtonBase(c).Label(i18n.T("重新测速 ")+n).Tooltip(i18n.T("单击重新测速")).Padding(1, 4).Radius(4)
			if d.Hovered() {
				d.Background(t.SurfaceHover)
			}
			d.Children(func() { k.Delay(c, pr.Delay, pr.Testing) })
			if d.Clicked() {
				name := n
				p.e.Run(func() error { return p.e.API.TestProxyDelay(name) }, "")
			}
		})
	})
	if selectable && tile.Clicked() && !cur {
		p.pick(g.Name, n)
	}
}

// providers draws the collapsed 代理集合 section at the bottom of the page:
// one row per proxy-provider with its source, node count, update time,
// subscription usage, and buttons to re-download and health-check it.
func (p *Page) providers(c *ui.Context, provs []model.ProxyProvider, px model.ProxySnapshot) {
	t := c.Theme()
	k := p.e.Kit
	box := ui.Column(c.Key("providers")).Radius(10).Background(t.Surface).Border(1, t.Border).Clip()
	box.Children(func() {
		parts := ui.CollapsibleBase(c.Key("coll"), &p.provOpen)
		parts.Trigger.MinHeight(40).Padding(6, 12).Gap(8).AlignItems(ui.Center).Label(i18n.T("代理集合")).Children(func() {
			ui.Box(c).TextColor(t.TextMuted).Children(func() { k.Icon(c, "chevron", 12) }).Rotate(90 * parts.Progress())
			ui.Text(c, i18n.T("代理集合")).FontSize(13).FontWeight(600).SingleLine()
			ui.Text(c, fmt.Sprintf(i18n.T("%d 个代理集合"), len(provs))).FontSize(12).TextColor(t.TextMuted).SingleLine().
				Tooltip(fmt.Sprintf(i18n.T("%d 个 proxy-provider"), len(provs)))
		})
		parts.Panel(func() {
			ui.Column(c).Padding(0, 6, 6, 6).Children(func() {
				ui.Box(c).Height(1).Background(t.Border).Margin(0, 6, 4, 6)
				for _, pv := range provs {
					p.provider(c, pv, px)
				}
			})
		})
	})
}

// providerSource names a provider's vehicle type.
func providerSource(vehicle string) string {
	switch vehicle {
	case "HTTP":
		return i18n.T("链接")
	case "File":
		return i18n.T("文件")
	case "Inline":
		return i18n.T("内联")
	case "Compatible":
		return i18n.T("配置内")
	default:
		return vehicle
	}
}

// providerMeta is the muted summary line under a provider's name.
func providerMeta(pv model.ProxyProvider) string {
	meta := fmt.Sprintf(i18n.T("%s · %d 个节点"), providerSource(pv.VehicleType), len(pv.Proxies))
	if !pv.UpdatedAt.IsZero() {
		meta += i18n.T(" · 更新于 ") + env.FmtAgo(pv.UpdatedAt)
	}
	if pv.Total > 0 {
		meta += fmt.Sprintf(i18n.T(" · 已用 %s / %s GB"), env.FmtGB(pv.Upload+pv.Download), env.FmtGB(pv.Total))
	}
	if !pv.Expire.IsZero() {
		meta += i18n.T(" · 到期 ") + pv.Expire.Format("2006-01-02")
	}
	return meta
}

// provider draws one proxy-provider row.
func (p *Page) provider(c *ui.Context, pv model.ProxyProvider, px model.ProxySnapshot) {
	t := c.Theme()
	k := p.e.Kit
	checking := pv.Checking
	for _, n := range pv.Proxies {
		if px.Proxies[n].Testing {
			checking = true
			break
		}
	}
	ui.Row(c.Key("prov:"+pv.Name)).MinHeight(44).Padding(4, 4, 4, 12).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
			ui.Text(c, pv.Name).FontSize(13).FontWeight(500).SingleLine()
			ui.Text(c, providerMeta(pv)).FontSize(11).TextColor(t.TextMuted).SingleLine()
		})
		canUpdate := pv.VehicleType == "HTTP" || pv.VehicleType == "File"
		up := ui.Button(c, "").Label(i18n.T("更新 ") + pv.Name).Tooltip(i18n.T("重新下载该代理集合")).Disabled(!canUpdate || pv.Updating).Children(func() {
			if pv.Updating {
				ui.Spinner(c).Size(12, 12)
			} else {
				k.Icon(c, "refresh", 14)
			}
		})
		if up.Clicked() {
			name := pv.Name
			p.e.Run(func() error { return p.e.API.UpdateProxyProvider(name) }, i18n.Tf("已更新「%s」", name))
		}
		hc := ui.Button(c, "").Label(i18n.T("健康检查 ") + pv.Name).Tooltip(i18n.T("测试该集合中所有节点的延迟")).Disabled(checking || pv.TestURL == "").Children(func() {
			if checking {
				ui.Spinner(c).Size(12, 12)
			} else {
				k.Icon(c, "bolt", 14)
			}
		})
		if hc.Clicked() {
			name := pv.Name
			p.e.Run(func() error { return p.e.API.HealthCheckProvider(name) }, "")
		}
	})
}

// node draws one member row.
func (p *Page) node(c *ui.Context, g model.ProxyGroup, n, now string, selectable bool, px model.ProxySnapshot, names map[string]bool) {
	t := c.Theme()
	k := p.e.Kit
	pr := px.Proxies[n]
	proto, isGroup, builtin := memberKind(n, px, names)
	cur := n == now
	var row ui.Element
	if selectable {
		row = ui.ButtonBase(c.Key("node:" + n)).Label(n).Cursor(ui.CursorPointer)
		if row.Hovered() {
			row.Background(t.SurfaceHover)
		}
	} else {
		row = ui.Row(c.Key("node:" + n))
	}
	row.Height(nodeRowH).Padding(0, 10, 0, 12).Gap(10).AlignItems(ui.Center).Radius(6)
	if cur {
		row.Background(t.Accent.Alpha(0.08))
	}
	if reveal, tint := p.locating(c, g.Name, cur); tint {
		row.Background(t.Accent.Alpha(0.28))
		if reveal {
			row.ScrollIntoView(ui.ScrollIntoViewOptions{Block: ui.ScrollCenter, Animation: &ui.Smooth})
		}
	}
	row.Children(func() {
		if selectable {
			radio := ui.Box(c).Size(14, 14).Radius(7).Shrink(0)
			if cur {
				radio.Border(4, t.Accent).Background(t.Background)
			} else {
				radio.Border(1.5, t.TextMuted.Alpha(0.6))
			}
		} else {
			ui.Box(c).Size(14, 14).Shrink(0)
		}
		ui.Text(c, n).FontSize(13).SingleLine().Shrink(1)
		if !selectable && cur {
			k.Tag(c, i18n.T("当前"), "accent")
		}
		if proto != "" {
			ui.Text(c, proto).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(0)
		}
		ui.Spacer(c)
		if isGroup || builtin {
			ui.Text(c, "—").FontSize(12).TextColor(t.TextMuted)
			return
		}
		d := ui.ButtonBase(c).Label(i18n.T("重新测速 ")+n).Tooltip(i18n.T("单击重新测速")).Padding(2, 6).Radius(4).MinWidth(56).Justify(ui.End)
		if d.Hovered() {
			d.Background(t.SurfaceHover)
		}
		d.Children(func() { k.Delay(c, pr.Delay, pr.Testing) })
		if d.Clicked() {
			name := n
			p.e.Run(func() error { return p.e.API.TestProxyDelay(name) }, "")
		}
	})
	if selectable && row.Clicked() && !cur {
		p.pick(g.Name, n)
	}
}

// pick selects a node: optimistic until the core confirms.
func (p *Page) pick(group, name string) {
	p.mu.Lock()
	p.pending[group] = name
	p.mu.Unlock()
	p.e.Run(func() error {
		err := p.e.API.SelectProxy(group, name)
		p.mu.Lock()
		delete(p.pending, group)
		p.mu.Unlock()
		return err
	}, i18n.T("已切换到 ")+name)
}

func (p *Page) testAll() {
	p.mu.Lock()
	if p.testing {
		p.mu.Unlock()
		return
	}
	p.testing = true
	p.mu.Unlock()
	p.e.Run(func() error {
		err := p.e.API.TestAllDelays()
		p.mu.Lock()
		p.testing = false
		p.mu.Unlock()
		return err
	}, i18n.T("测速完成"))
}

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicProxies}
}
