// Package conns is the 连接 page (docs/design.md §5.4): a sortable table of
// the connections going through the core, a detail panel, and the entry
// points for adding a custom rule from a connection.
package conns

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/procicon"
	"lanflow/internal/view/rules/ruleform"
)

const detailWidth = 280

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	query  string
	search ui.Handle // the search field, focused by ⌘F
	paused bool
	frozen struct {
		active, closed []model.Connection
	}
	tab      int // 0 活动, 1 已关闭
	view     int // 0 列表, 1 拓扑 (topology.go)
	topo     topology
	sort     ui.SortOrder
	list     ui.ListState
	selected int    // row index the table chooses
	sel      string // ID of the connection shown in the detail panel
	hoverRow int
	form     ruleform.Form
	icons    *procicon.Cache // application icons of the 进程 column

	// hidden holds the IDs of the columns the user hid through the header's
	// context menu; it mirrors Settings.ConnHiddenColumns.
	hidden map[string]bool
	// cols are the columns shown this frame, in the order of allColumns.
	cols []ui.TableColumn
}

// New returns the page.
func New(e *env.Env) *Page {
	p := &Page{e: e, selected: -1, hoverRow: -1, sort: ui.SortOrder{Column: "down", Descending: true}, icons: procicon.New(e.API)}
	p.topo.icons = p.icons
	p.list.Sort = &p.sort
	p.list.Selected = &p.selected
	p.list.KeepOffset = true
	p.list.HeaderMenu = p.headerMenu
	return p
}

// allColumns are the table's columns (docs/design.md §5.4); the user
// chooses which show by right-clicking the header. 主机 always shows.
var allColumns = []ui.TableColumn{
	{Title: "主机", ID: "host", MinWidth: 150, Sortable: true},
	{Title: "进程", ID: "process", Width: 104, Sortable: true},
	{Title: "规则", ID: "rule", Width: 118, Sortable: true},
	{Title: "链路", ID: "chain", Width: 150, Sortable: true},
	{Title: "↓ 速度", ID: "down", Width: 84, Align: ui.End, Sortable: true},
	{Title: "↑ 速度", ID: "up", Width: 84, Align: ui.End, Sortable: true},
	{Title: "↓ 总计", ID: "total", Width: 92, Align: ui.End, Sortable: true},
	{Title: "时长", ID: "dur", Width: 68, Align: ui.End, Sortable: true},
	{Title: "", ID: "close", Width: 30, Fixed: true},
}

// visibleColumns returns allColumns without the hidden ones.
func (p *Page) visibleColumns() []ui.TableColumn {
	hidden := p.e.API.Settings().ConnHiddenColumns
	p.hidden = make(map[string]bool, len(hidden))
	for _, id := range hidden {
		p.hidden[id] = true
	}
	cols := make([]ui.TableColumn, 0, len(allColumns))
	for _, col := range allColumns {
		col.Title = i18n.T(col.Title)
		if !p.hidden[col.ID] || col.ID == "host" || col.Fixed {
			cols = append(cols, col)
		}
	}
	return cols
}

// headerMenu is the header's context menu: a check item per column.
func (p *Page) headerMenu(m *ui.Menu, _ int) {
	for _, col := range allColumns {
		if col.Fixed {
			continue
		}
		item := m.Item(i18n.T(col.Title)).Checked(!p.hidden[col.ID]).Disabled(col.ID == "host")
		if item.Chosen() {
			p.toggleColumn(col.ID)
		}
	}
	m.Separator()
	if m.Item(i18n.T("显示全部列")).Disabled(len(p.hidden) == 0).Chosen() {
		p.saveHidden(nil)
	}
}

// toggleColumn hides a shown column or shows a hidden one.
func (p *Page) toggleColumn(id string) {
	var hidden []string
	for _, col := range allColumns {
		if (p.hidden[col.ID]) != (col.ID == id) {
			hidden = append(hidden, col.ID)
		}
	}
	p.saveHidden(hidden)
}

// saveHidden persists the hidden columns in the settings.
func (p *Page) saveHidden(hidden []string) {
	e := p.e
	e.Run(func() error {
		set := e.API.Settings()
		set.ConnHiddenColumns = hidden
		return e.API.SaveSettings(set)
	}, "")
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	e, t := p.e, c.Theme()
	if a := e.Arg(env.Conns); a != "" {
		// The exit map opens the page filtered to one route (docs/exit-map.md §4).
		p.query, p.tab, p.paused, p.view = a, 0, false, 0
	}
	st := e.API.Status()
	running := st.Core == model.CoreRunning
	active, closed := e.API.Connections(), e.API.ClosedConnections()
	if p.paused {
		if p.frozen.active == nil && p.frozen.closed == nil {
			p.frozen.active, p.frozen.closed = active, closed
		}
		active, closed = p.frozen.active, p.frozen.closed
	} else {
		p.frozen.active, p.frozen.closed = nil, nil
	}
	if !running {
		p.paused = false
	}

	subtitle := ""
	if running {
		subtitle = fmt.Sprintf(i18n.T("%d 个活动连接"), len(active))
	}
	ui.Column(c).Fill().Children(func() {
		e.Kit.Header(c, i18n.T("连接"), subtitle, func() { p.tools(c, running, len(active)) })
		if c.Shortcut(ui.Cmd, ui.KeyF) {
			p.search.Focus(c)
		}
		switch {
		case !running:
			e.Kit.Empty(c, "conns", i18n.T("内核未启动"), i18n.T("启动内核后这里会实时显示经过澜流的连接。"), nil)
		case p.view == 1:
			p.topology(c, t, active, closed)
		default:
			p.body(c, t, active, closed)
		}
	})
	p.form.View(c, e)
}

func (p *Page) tools(c *ui.Context, running bool, n int) {
	e := p.e
	// The toolbar must fit beside the title at the minimum window width
	// (docs/design.md §4), with the topology's extra controls too.
	searchWidth := float32(130)
	if env.TwoColumns(c) {
		searchWidth = 180
	}
	ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索")).Label(i18n.T("搜索连接")).Width(searchWidth).Bind(&p.search)
	pauseLabel, icon := i18n.T("暂停"), "pause"
	if p.paused {
		pauseLabel, icon = i18n.T("继续"), "play"
	}
	ui.Button(c, "").Label(pauseLabel).Disabled(!running).Children(func() {
		e.Kit.Icon(c, icon, 13)
		ui.Text(c, pauseLabel)
	}).OnClick(func() { p.paused = !p.paused })
	if p.view == 1 {
		// The topology weighs its bands by speed or by total bytes, and
		// may take the recently closed connections in (topology.go).
		ui.Segmented(c.Key("weight"), &p.topo.byTotal, i18n.T("速度"), i18n.T("流量")).Label(i18n.T("线宽依据"))
		ui.Button(c, "").Label(i18n.T("拓扑选项")).Tooltip(i18n.T("拓扑选项")).Children(func() {
			e.Kit.Icon(c, "more", 14)
		}).Menu(func(m *ui.Menu) {
			if m.Item(i18n.T("含已关闭")).Checked(p.topo.withClosed).Chosen() {
				p.topo.withClosed = !p.topo.withClosed
			}
			if m.Item(i18n.T("分端口")).Checked(p.topo.byPort).Chosen() {
				p.topo.byPort = !p.topo.byPort
			}
		})
	} else {
		ui.Segmented(c.Key("tab"), &p.tab, i18n.T("活动"), i18n.T("已关闭")).Label(i18n.T("连接列表"))
	}
	ui.Segmented(c.Key("view"), &p.view, i18n.T("列表"), i18n.T("拓扑")).Label(i18n.T("连接视图"))
	ui.Button(c, i18n.T("全部断开")).Disabled(!running || n == 0).OnClick(func() {
		e.Confirm(fmt.Sprintf(i18n.T("断开全部 %d 个连接？"), n), i18n.T("应用会自动重新建立需要的连接。"), i18n.T("断开"), false, func() {
			p.sel = ""
			e.Run(e.API.CloseAllConnections, i18n.T("已断开全部连接"))
		})
	})
}

func (p *Page) body(c *ui.Context, t *ui.Theme, active, closed []model.Connection) {
	src := active
	if p.tab == 1 {
		src = closed
	}
	rows := p.filter(src)
	sortRows(rows, p.sort)
	if len(rows) == 0 {
		switch {
		case strings.TrimSpace(p.query) != "":
			p.e.Kit.Empty(c, "search", fmt.Sprintf(i18n.T("没有匹配“%s”的连接"), strings.TrimSpace(p.query)), "", nil)
		case p.tab == 1:
			p.e.Kit.Empty(c, "conns", i18n.T("暂无已关闭连接"), i18n.T("断开的连接会在这里保留最近 500 条，仅保存在内存中。"), nil)
		default:
			p.e.Kit.Empty(c, "conns", i18n.T("暂无活动连接"), "", nil)
		}
		return
	}
	// The detail panel follows the connection's ID, not its row.
	p.selected = -1
	for i, r := range rows {
		if r.ID == p.sel {
			p.selected = i
		}
	}
	p.hoverRow = -1
	p.list.Key = func(i int) any { return rows[i].ID }
	p.list.Label = func(i int) string { return rows[i].Host }
	p.list.CellMenu = func(m *ui.Menu, row, col int) { p.rowMenu(c, m, rows[row], p.tab == 0) }
	p.cols = p.visibleColumns()
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Table(c.Key("table"), &p.list, p.cols, len(rows), func(row, col int) {
			p.cell(c, t, rows[row], row, p.cols[col].ID)
		}).Grow(1).FillHeight().Label(i18n.T("连接"))
		if p.selected >= 0 && p.selected < len(rows) {
			p.sel = rows[p.selected].ID
		} else if p.selected < 0 {
			p.sel = ""
		}
		if p.sel != "" {
			if conn, ok := find(rows, p.sel); ok {
				p.detail(c, t, conn, p.tab == 0)
			}
		}
	})
}

// cell builds one cell; col is the column's ID.
func (p *Page) cell(c *ui.Context, t *ui.Theme, cn model.Connection, row int, col string) {
	q := strings.TrimSpace(p.query)
	muted := t.TextMuted
	box := ui.Row(c).FillWidth()
	if box.Hovered() {
		p.hoverRow = row
	}
	box.Children(func() {
		switch col {
		case "host":
			highlight(c, cn.Host, q, t.Text, t).SingleLine()
		case "process":
			name := cn.Process
			if name == "" {
				name = "—"
			}
			ui.Row(c).Gap(5).AlignItems(ui.Center).FillWidth().Children(func() {
				if cn.Process != "" {
					p.icons.Draw(c, t, cn.ProcessPath, procicon.Size)
				}
				highlight(c, name, q, muted, t).SingleLine().Shrink(1)
			})
		case "rule":
			highlight(c, ruleName(cn), q, muted, t).SingleLine()
		case "chain":
			highlight(c, chain(cn), q, t.Text, t).SingleLine()
		case "down":
			speed(c, t, cn.DownSpeed)
		case "up":
			speed(c, t, cn.UpSpeed)
		case "total":
			ui.Text(c, env.FmtBytes(cn.Download)).FontFeatures("tnum").SingleLine()
		case "dur":
			ui.Text(c, env.FmtDuration(duration(cn))).FontFeatures("tnum").SingleLine()
		case "close":
			if p.tab != 0 {
				return
			}
			show := p.hoverRow == row || p.selected == row
			id := cn.ID
			b := ui.Button(c, "").Label(i18n.T("断开")).Tooltip(i18n.T("断开")).Padding(0, 4).Children(func() {
				ui.Text(c, "✕").FontSize(11)
			}).OnClick(func() { p.closeConn(id) })
			if !show {
				b.Invisible()
			}
		}
	})
}

func speed(c *ui.Context, t *ui.Theme, v int64) {
	if v <= 0 {
		// Idle connections read as a dash, not a unit-less zero.
		ui.Text(c, "—").TextColor(t.TextMuted).FontFeatures("tnum")
		return
	}
	ui.Text(c, env.FmtSpeed(v)).FontFeatures("tnum").SingleLine()
}

// rowMenu is the context menu of a row; it runs while the view builds, so
// c is the window's context.
func (p *Page) rowMenu(c *ui.Context, m *ui.Menu, cn model.Connection, active bool) {
	if active {
		if m.Item(i18n.T("断开")).Chosen() {
			p.closeConn(cn.ID)
		}
		m.Separator()
	}
	if m.Item(i18n.T("此域名始终走…")).Chosen() {
		p.addRuleForHost(cn)
	}
	if m.Item(i18n.T("此程序始终走…")).Disabled(cn.Process == "").Chosen() {
		p.addRuleForProcess(cn)
	}
	m.Separator()
	if m.Item(i18n.T("复制主机")).Chosen() {
		c.WriteClipboard(cn.Host)
		p.e.Toast(i18n.T("已复制"), false)
	}
}

func (p *Page) detail(c *ui.Context, t *ui.Theme, cn model.Connection, active bool) {
	e := p.e
	ui.Column(c.Key("detail")).Width(detailWidth).FillHeight().BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Scroll(c).Grow(1).Padding(14, 16).Gap(12).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Start).Children(func() {
				ui.Text(c, cn.Host).Bold().Grow(1).Selectable()
				ui.Button(c, "").Label(i18n.T("关闭详情")).Tooltip(i18n.T("关闭")).Padding(0, 4).Children(func() {
					ui.Text(c, "✕").FontSize(11)
				}).OnClick(func() { p.sel = "" })
			})
			meta := strings.ToUpper(cn.Network)
			if cn.Type != "" {
				meta += " · " + cn.Type
			}
			if !cn.ClosedAt.IsZero() {
				meta += i18n.T(" · 已关闭 ") + env.FmtAgo(cn.ClosedAt)
			}
			ui.Text(c, meta).FontSize(11).TextColor(t.TextMuted)
			ui.Column(c).Gap(6).Selectable().Children(func() {
				kv := func(k, v string) { p.kv(c, t, k, v, "") }
				src := cn.SourceIP
				if cn.SourcePort != "" {
					src += ":" + cn.SourcePort
				}
				kv(i18n.T("源地址"), src)
				kv(i18n.T("目标地址"), cn.Host)
				kv(i18n.T("目标 IP"), dash(cn.DstIP))
				if cn.RemoteDestination != "" && cn.RemoteDestination != cn.DstIP {
					kv(i18n.T("实际连接"), cn.RemoteDestination)
				}
				p.kv(c, t, i18n.T("进程"), dash(cn.Process), cn.ProcessPath)
				kv(i18n.T("规则"), ruleName(cn))
				kv(i18n.T("规则载荷"), dash(cn.RulePayload))
				kv(i18n.T("链路"), chain(cn))
				kv(i18n.T("下行"), i18n.Tf("%s · 共 %s", env.FmtSpeed(cn.DownSpeed), env.FmtBytes(cn.Download)))
				kv(i18n.T("上行"), i18n.Tf("%s · 共 %s", env.FmtSpeed(cn.UpSpeed), env.FmtBytes(cn.Upload)))
				kv(i18n.T("时长"), env.FmtDuration(duration(cn)))
			})
			ui.Column(c).Gap(6).AlignItems(ui.Start).Children(func() {
				ui.Button(c, "").Children(func() {
					e.Kit.Icon(c, "rules", 13)
					ui.Text(c, i18n.T("此域名始终走…"))
				}).OnClick(func() { p.addRuleForHost(cn) })
				ui.Button(c, "").Disabled(cn.Process == "").Children(func() {
					e.Kit.Icon(c, "rules", 13)
					ui.Text(c, i18n.T("此程序始终走…"))
				}).OnClick(func() { p.addRuleForProcess(cn) })
				if active && cn.ClosedAt.IsZero() {
					id := cn.ID
					ui.Button(c, i18n.T("断开此连接")).OnClick(func() { p.closeConn(id) })
				}
			})
		})
	})
}

func (p *Page) kv(c *ui.Context, t *ui.Theme, k, v, sub string) {
	ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
		ui.Text(c, k).Width(64).FontSize(12).TextColor(t.TextMuted).Unselectable()
		ui.Column(c).Grow(1).Children(func() {
			ui.Text(c, v).FontSize(12).FontFeatures("tnum")
			if sub != "" {
				ui.Text(c, sub).FontSize(11).TextColor(t.TextMuted)
			}
		})
	})
}

func (p *Page) closeConn(id string) {
	e := p.e
	if p.sel == id {
		p.sel = ""
	}
	e.Run(func() error { return e.API.CloseConnection(id) }, "")
}

func (p *Page) addRuleForHost(cn model.Connection) {
	host := hostOnly(cn.Host)
	r := model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: ruleform.SuffixOf(host), Target: p.defaultTarget(cn)}
	if isIP(host) {
		r.Type, r.Payload = "IP-CIDR", ruleform.CIDROf(host)
	}
	p.form.Open(r)
}

func (p *Page) addRuleForProcess(cn model.Connection) {
	p.form.Open(model.CustomRule{Type: "PROCESS-NAME", Payload: cn.Process, Target: p.defaultTarget(cn)})
}

// defaultTarget prefills the rule's target with the connection's first
// hop when it is a group, else the main group.
func (p *Page) defaultTarget(cn model.Connection) string {
	if len(cn.Chains) > 0 && cn.Chains[0] != "DIRECT" && cn.Chains[0] != "REJECT" {
		return cn.Chains[0]
	}
	return p.e.API.Proxies().MainGroup
}

// ---- helpers ----

func (p *Page) filter(src []model.Connection) []model.Connection {
	q := strings.ToLower(strings.TrimSpace(p.query))
	out := make([]model.Connection, 0, len(src))
	for _, cn := range src {
		if q == "" || strings.Contains(strings.ToLower(haystack(cn)), q) {
			out = append(out, cn)
		}
	}
	return out
}

// haystack is the text the search matches: host, process, rule with its
// payload, chain, destination IP and source, so a topology node's label
// (topology_graph.go) finds its connections.
func haystack(cn model.Connection) string {
	src := cn.SourceIP
	if cn.SourcePort != "" {
		src += ":" + cn.SourcePort
	}
	return cn.Host + " " + cn.Process + " " + ruleName(cn) + " " + cn.RulePayload + " " + chain(cn) + " " + cn.DstIP + " " + src
}

func sortRows(rows []model.Connection, s ui.SortOrder) {
	less := func(a, b model.Connection) bool {
		switch s.Column {
		case "host":
			return a.Host < b.Host
		case "process":
			return a.Process < b.Process
		case "rule":
			return ruleName(a) < ruleName(b)
		case "chain":
			return chain(a) < chain(b)
		case "up":
			return a.UpSpeed < b.UpSpeed
		case "total":
			return a.Download < b.Download
		case "dur":
			return duration(a) < duration(b)
		default: // "down"
			return a.DownSpeed < b.DownSpeed
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if s.Descending {
			a, b = b, a
		}
		if less(a, b) {
			return true
		}
		if less(b, a) {
			return false
		}
		return rows[i].Start.After(rows[j].Start) // newest first on ties
	})
}

func find(rows []model.Connection, id string) (model.Connection, bool) {
	for _, r := range rows {
		if r.ID == id {
			return r, true
		}
	}
	return model.Connection{}, false
}

func duration(cn model.Connection) time.Duration {
	end := time.Now()
	if !cn.ClosedAt.IsZero() {
		end = cn.ClosedAt
	}
	if cn.Start.IsZero() {
		return 0
	}
	return end.Sub(cn.Start)
}

// ruleName shows the rule as Mihomo's config spells it (DomainSuffix → DOMAIN-SUFFIX).
func ruleName(cn model.Connection) string {
	switch cn.Rule {
	case "":
		return "—"
	case "Match":
		return "MATCH"
	}
	var b strings.Builder
	for i, r := range cn.Rule {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(cn.Rule[i-1])
			if prev < 'A' || prev > 'Z' {
				b.WriteByte('-')
			}
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

func chain(cn model.Connection) string {
	if len(cn.Chains) == 0 {
		return "—"
	}
	return strings.Join(cn.Chains, " › ")
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func hostOnly(h string) string {
	if i := strings.LastIndex(h, ":"); i > 0 && strings.Count(h, ":") == 1 {
		return h[:i]
	}
	if strings.HasPrefix(h, "[") {
		if j := strings.Index(h, "]"); j > 0 {
			return h[1:j]
		}
	}
	return h
}

func isIP(h string) bool {
	for _, r := range h {
		if !(r >= '0' && r <= '9') && r != '.' && r != ':' && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
			return false
		}
	}
	return strings.ContainsAny(h, ".:")
}

// highlight draws text with the case-insensitive matches of q marked.
func highlight(c *ui.Context, text, q string, color ui.Color, t *ui.Theme) ui.Element {
	if q == "" {
		return ui.Text(c, text).TextColor(color)
	}
	lower, lq := strings.ToLower(text), strings.ToLower(q)
	var spans []ui.Span
	for i := 0; i < len(text); {
		j := strings.Index(lower[i:], lq)
		if j < 0 {
			spans = append(spans, ui.Span{Text: text[i:], Color: color})
			break
		}
		if j > 0 {
			spans = append(spans, ui.Span{Text: text[i : i+j], Color: color})
		}
		spans = append(spans, ui.Span{Text: text[i+j : i+j+len(q)], Color: color, Background: t.Warning.Alpha(0.35)})
		i += j + len(q)
	}
	return ui.RichText(c, spans...)
}
