// Package stats is the 统计 page: the traffic accounted per device, domain,
// process, rule, proxy, destination country and inbound (API.TrafficStats)
// over today, this week, this month or everything, with a bar chart over
// time and a sortable table.
package stats

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/procicon"
)

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	rng   int  // index in rangeKeys
	dim   int  // index in dims
	hosts bool // the 域名 dimension lists full hosts instead of registrable domains

	query    string
	search   ui.Handle
	sort     ui.SortOrder
	list     ui.ListState
	selected int
	icons    *procicon.Cache // application icons of the 进程 dimension
}

var rangeKeys = []string{model.StatsToday, model.StatsWeek, model.StatsMonth, model.StatsAll}

// rangeLabels name the ranges (i18n keys).
var rangeLabels = []string{"今天", "本周", "本月", "全部"}

var dims = []model.StatsDimension{model.StatsDevice, model.StatsDomain, model.StatsProcess, model.StatsRule, model.StatsProxy, model.StatsCountry, model.StatsInbound}

// dimLabels name the dimensions (i18n keys), in the order of dims.
var dimLabels = []string{"设备", "域名", "进程", "规则", "代理|统计", "地区", "入站"}

// columns of the table (titles are i18n keys).
var columns = []ui.TableColumn{
	{Title: "名称", ID: "name", MinWidth: 160, Sortable: true},
	{Title: "请求数", ID: "req", Width: 76, Align: ui.End, Sortable: true},
	{Title: "上传", ID: "up", Width: 86, Align: ui.End, Sortable: true},
	{Title: "下载", ID: "down", Width: 86, Align: ui.End, Sortable: true},
	{Title: "总计", ID: "total", Width: 86, Align: ui.End, Sortable: true},
	{Title: "占比", ID: "share", Width: 130, Sortable: true},
	{Title: "最近活动", ID: "last", Width: 100, Align: ui.End, Sortable: true},
}

// New returns the page.
func New(e *env.Env) *Page {
	p := &Page{e: e, selected: -1, sort: ui.SortOrder{Column: "total", Descending: true}, icons: procicon.New(e.API)}
	p.list.Sort = &p.sort
	p.list.Selected = &p.selected
	p.list.KeepOffset = true
	return p
}

// row is one table line: a bucket with its display texts.
type row struct {
	model.StatsBucket
	name, detail string
	share        float64 // of the range's total bytes, 0…1
}

// View draws the page.
func (p *Page) View(c *ui.Context) {
	e, t := p.e, c.Theme()
	st := e.API.TrafficStats(rangeKeys[p.rng])
	subtitle := ""
	if p.rng == len(rangeKeys)-1 && !st.Since.IsZero() {
		subtitle = fmt.Sprintf(i18n.T("自 %s 起"), st.Since.Format("2006-01-02"))
	}
	ui.Column(c).Fill().Children(func() {
		e.Kit.Header(c, i18n.T("统计"), subtitle, func() { p.tools(c) })
		if c.Shortcut(ui.Cmd, ui.KeyF) {
			p.search.Focus(c)
		}
		ui.Column(c).Grow(1).Padding(0, 24, 16, 24).Gap(12).Children(func() {
			p.cards(c, t, st)
			if st.Totals.Requests == 0 && st.Totals.Upload+st.Totals.Download == 0 {
				e.Kit.Empty(c, "stats", i18n.T("暂无统计数据"), i18n.T("内核运行时，经过澜流的流量会按设备、域名、进程等在这里汇总。"), nil)
				return
			}
			p.chart(c, t, st)
			p.table(c, t, st)
		})
	})
}

// tools are the header's range switch and the reset button.
func (p *Page) tools(c *ui.Context) {
	e := p.e
	ui.Segmented(c.Key("range"), &p.rng, i18n.Options(rangeLabels...)...).Label(i18n.T("时间范围"))
	ui.Button(c, i18n.T("重置统计")).OnClick(func() {
		e.Confirm(i18n.T("重置统计？"), i18n.T("将删除所有已记录的流量统计，且无法恢复。"), i18n.T("重置"), true, func() {
			e.Run(e.API.ResetTrafficStats, i18n.T("统计已重置"))
		})
	})
}

// cards are the five totals.
func (p *Page) cards(c *ui.Context, t *ui.Theme, st model.TrafficStats) {
	card := func(label, value string) {
		ui.Column(c).Grow(1).Gap(3).Padding(10, 12).Radius(10).Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).SingleLine()
			ui.Text(c, value).FontSize(20).FontWeight(600).FontFeatures("tnum").SingleLine()
		})
	}
	ui.Row(c).Gap(10).Shrink(0).Children(func() {
		card(i18n.T("上传"), env.FmtBytes(st.Totals.Upload))
		card(i18n.T("下载"), env.FmtBytes(st.Totals.Download))
		card(i18n.T("总计"), env.FmtBytes(st.Totals.Upload+st.Totals.Download))
		card(i18n.T("请求数"), env.FmtInt(st.Totals.Requests))
		card(i18n.T("设备数"), fmt.Sprint(st.Totals.Devices))
	})
}

// chart is the bar chart over time: download at the bottom, upload stacked
// on it, hourly for 今天 and daily for the other ranges.
func (p *Page) chart(c *ui.Context, t *ui.Theme, st model.TrafficStats) {
	series := st.Series
	var peak int64 = 1
	for _, s := range series {
		peak = max(peak, s.Upload+s.Download)
	}
	title := i18n.T("最近 30 天")
	if st.Hourly {
		title = i18n.T("今天（按小时）")
	}
	ui.Column(c).Gap(6).Padding(10, 12).Radius(10).Background(t.Surface).Border(1, t.Border).Shrink(0).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(12).FontWeight(600).SingleLine()
			ui.Spacer(c)
			legend(c, t, i18n.T("下载"), t.Accent)
			legend(c, t, i18n.T("上传"), t.TextMuted.Alpha(0.55))
			ui.Text(c, i18n.T("峰值 ")+env.FmtBytes(peak)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
		})
		box := ui.Box(c).Height(104).FillWidth().Label(title)
		px, _, hovering := box.PointerPosition()
		box.Draw(func(pt *ui.Painter, r ui.Rect) {
			const labels = 14
			plot := ui.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H - labels}
			for _, f := range []float32{0, .5, 1} {
				y := plot.Y + plot.H*f
				pt.Line(plot.X, y, plot.X+plot.W, y, 1, t.Border)
			}
			n := len(series)
			if n == 0 {
				return
			}
			slot := plot.W / float32(n)
			bw := slot * 0.68
			hover := -1
			if hovering && px >= 0 && px < r.W {
				hover = min(n-1, int(px/slot))
			}
			for i, s := range series {
				x := plot.X + slot*float32(i) + (slot-bw)/2
				hd := plot.H * float32(s.Download) / float32(peak)
				hu := plot.H * float32(s.Upload) / float32(peak)
				if i == hover {
					pt.Fill(ui.Rect{X: plot.X + slot*float32(i), Y: plot.Y, W: slot, H: plot.H}, t.Accent.Alpha(0.08), 3)
				}
				if hd > 0 {
					pt.Fill(ui.Rect{X: x, Y: plot.Y + plot.H - hd, W: bw, H: hd}, t.Accent, 1.5)
				}
				if hu > 0 {
					pt.Fill(ui.Rect{X: x, Y: plot.Y + plot.H - hd - hu, W: bw, H: hu}, t.TextMuted.Alpha(0.55), 1.5)
				}
				if lab := axisLabel(i, n, s.Time, st.Hourly); lab != "" {
					pt.Text(plot.X+slot*float32(i), plot.Y+plot.H+3, lab, 10, t.TextMuted)
				}
			}
			if hover >= 0 {
				s := series[hover]
				tip := fmt.Sprintf(i18n.T("%s · ↓ %s ↑ %s · %d 次请求"), tipTime(s.Time, st.Hourly), env.FmtBytes(s.Download), env.FmtBytes(s.Upload), s.Requests)
				tw, _ := pt.MeasureText(0, ui.Span{Text: tip, Size: 11})
				tx := min(max(r.X+px+10, r.X), r.X+r.W-tw-8)
				pt.Fill(ui.Rect{X: tx - 6, Y: r.Y + 2, W: tw + 12, H: 20}, t.Background, 6)
				pt.Stroke(ui.Rect{X: tx - 6, Y: r.Y + 2, W: tw + 12, H: 20}, t.Border, 6, 1)
				pt.Text(tx, r.Y+5, tip, 11, t.Text)
			}
		})
	})
}

// axisLabel labels every 6th hour or every 5th day.
func axisLabel(i, n int, at time.Time, hourly bool) string {
	if hourly {
		if i%6 == 0 || i == n-1 {
			return at.Format("15:04")
		}
		return ""
	}
	if i == n-1 {
		return i18n.T("今天")
	}
	if (n-1-i)%5 == 0 {
		return at.Format("01-02")
	}
	return ""
}

// tipTime names a bar in the hover tip.
func tipTime(at time.Time, hourly bool) string {
	if hourly {
		return at.Format("15:00") + "–" + at.Add(time.Hour).Format("15:04")
	}
	return at.Format("2006-01-02")
}

func legend(c *ui.Context, t *ui.Theme, label string, col ui.Color) {
	ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(10, 10).Radius(2).Background(col)
		ui.Text(c, label).FontSize(11).TextColor(t.TextMuted)
	})
}

// table is the dimension switch, the search and the sortable rows.
func (p *Page) table(c *ui.Context, t *ui.Theme, st model.TrafficStats) {
	e := p.e
	ui.Row(c).Gap(10).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Segmented(c.Key("dim"), &p.dim, i18n.Options(dimLabels...)...).Label(i18n.T("统计维度"))
		if dims[p.dim] == model.StatsDomain {
			ui.Checkbox(c, &p.hosts, i18n.T("完整主机名"))
		}
		ui.Spacer(c)
		ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索")).Label(i18n.T("搜索统计")).Width(200).Bind(&p.search)
	})
	rows := p.rows(st)
	q := strings.TrimSpace(p.query)
	if len(rows) == 0 {
		if q != "" {
			e.Kit.Empty(c, "search", fmt.Sprintf(i18n.T("没有匹配“%s”的项目"), q), "", nil)
		} else {
			e.Kit.Empty(c, "stats", i18n.T("此范围内暂无数据"), "", nil)
		}
		return
	}
	p.list.Key = func(i int) any { return rows[i].Key }
	p.list.Label = func(i int) string { return rows[i].name }
	p.list.CellMenu = func(m *ui.Menu, r, _ int) {
		if m.Item(i18n.T("复制名称")).Chosen() {
			c.WriteClipboard(rows[r].name)
			e.Toast(i18n.T("已复制"), false)
		}
	}
	cols := env.I18nColumns(columns)
	ui.Column(c).Grow(1).Radius(10).Border(1, t.Border).Clip().Children(func() {
		ui.Table(c.Key("table"), &p.list, cols, len(rows), func(r, col int) {
			p.cell(c, t, rows[r], cols[col].ID, q)
		}).Grow(1).FillHeight().Label(i18n.T("统计"))
	})
}

// cell builds one cell; col is the column's ID.
func (p *Page) cell(c *ui.Context, t *ui.Theme, r row, col, q string) {
	switch col {
	case "name":
		withIcon := dims[p.dim] == model.StatsProcess && r.Key != "" && r.Key != model.StatsOtherKey
		align := ui.Baseline
		if withIcon {
			align = ui.Center // an icon has no baseline
		}
		ui.Row(c).Gap(6).AlignItems(align).FillWidth().Children(func() {
			if withIcon {
				p.icons.Draw(c, t, r.Detail, procicon.Size)
			}
			highlight(c, r.name, q, t.Text, t).SingleLine().Shrink(1)
			if r.detail != "" && r.detail != r.name {
				highlight(c, r.detail, q, t.TextMuted, t).FontSize(11).SingleLine().Shrink(2)
			}
		})
	case "req":
		ui.Text(c, env.FmtInt(r.Requests)).FontFeatures("tnum").SingleLine()
	case "up":
		ui.Text(c, env.FmtBytes(r.Upload)).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine()
	case "down":
		ui.Text(c, env.FmtBytes(r.Download)).FontFeatures("tnum").SingleLine()
	case "total":
		ui.Text(c, env.FmtBytes(r.Total())).FontFeatures("tnum").SingleLine()
	case "share":
		share := float32(r.share)
		ui.Row(c).Gap(6).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Box(c).Height(6).Grow(1).Draw(func(pt *ui.Painter, b ui.Rect) {
				pt.Fill(b, t.Border, 3)
				if share > 0 {
					pt.Fill(ui.Rect{X: b.X, Y: b.Y, W: max(2, b.W*share), H: b.H}, t.Accent, 3)
				}
			})
			ui.Text(c, fmt.Sprintf("%.1f%%", r.share*100)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Width(44).TextAlign(ui.End).SingleLine()
		})
	case "last":
		ui.Text(c, env.FmtAgo(r.LastSeen)).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
	}
}

// rows maps the chosen dimension's buckets to table rows, filtered by the
// search and sorted.
func (p *Page) rows(st model.TrafficStats) []row {
	dim := dims[p.dim]
	if dim == model.StatsDomain && p.hosts {
		dim = model.StatsHost
	}
	total := st.Totals.Upload + st.Totals.Download
	lan := p.e.API.Status().LANAddress
	q := strings.ToLower(strings.TrimSpace(p.query))
	rows := make([]row, 0, len(st.Buckets[dim]))
	for _, b := range st.Buckets[dim] {
		r := row{StatsBucket: b}
		r.name, r.detail = p.label(dim, b, lan)
		if total > 0 {
			r.share = float64(b.Total()) / float64(total)
		}
		if q != "" && !strings.Contains(strings.ToLower(r.name+" "+r.detail+" "+b.Key), q) {
			continue
		}
		rows = append(rows, r)
	}
	sortRows(rows, p.sort)
	return rows
}

// label names a bucket for the table.
func (p *Page) label(dim model.StatsDimension, b model.StatsBucket, lan string) (name, detail string) {
	if b.Key == model.StatsOtherKey {
		return i18n.T("其他"), ""
	}
	switch dim {
	case model.StatsDevice:
		switch {
		case b.Key == "":
			return i18n.T("未知"), ""
		case isLocal(b.Key) || (lan != "" && b.Key == lan):
			return i18n.T("本机"), b.Key
		}
		return b.Key, ""
	case model.StatsProcess:
		if b.Key == "" {
			return i18n.T("未知进程"), ""
		}
		return b.Key, b.Detail
	case model.StatsRule:
		rule, payload, _ := strings.Cut(b.Key, ",")
		if rule == "" {
			rule = i18n.T("未知")
		}
		return rule, payload
	case model.StatsProxy:
		group, node, _ := strings.Cut(b.Key, "/")
		if group == "" {
			return i18n.T("未知"), ""
		}
		return env.TargetName(group), env.TargetName(node)
	case model.StatsCountry:
		if b.Key == "" {
			return i18n.T("未知"), ""
		}
		name := b.Key
		if i18n.Lang() == i18n.ZH {
			if cn := geo.CountryName(b.Key); cn != "" {
				name = cn
			}
		}
		if f := geo.Flag(b.Key); f != "" {
			name = f + " " + name
		}
		return name, b.Key
	case model.StatsHost:
		return b.Key, b.Detail
	}
	if b.Key == "" {
		return i18n.T("未知"), ""
	}
	return b.Key, ""
}

// isLocal reports a loopback source address.
func isLocal(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1" || ip == "localhost" || strings.HasPrefix(ip, "127.")
}

func sortRows(rows []row, s ui.SortOrder) {
	less := func(a, b row) bool {
		switch s.Column {
		case "name":
			return strings.ToLower(a.name) < strings.ToLower(b.name)
		case "req":
			return a.Requests < b.Requests
		case "up":
			return a.Upload < b.Upload
		case "down":
			return a.Download < b.Download
		case "last":
			return a.LastSeen.Before(b.LastSeen)
		default: // "total", "share"
			return a.Total() < b.Total()
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if s.Descending {
			a, b = b, a
		}
		return less(a, b)
	})
}

// highlight draws text with the matches of q marked.
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

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicStats, app.TopicProcIcons}
}
