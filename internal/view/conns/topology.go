package conns

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/procicon"
)

// Drawing of the 拓扑 view (topology_graph.go builds what it draws): the
// nodes are bars in six columns with their labels beside them, the links
// are bands whose width is the traffic, coloured by the node the traffic
// leaves through as the exit map colours its routes. Hovering a node or a
// band lights the paths through it and shows a tooltip; clicking a node
// shows its connections in the list view. Widths ease towards their new
// values every tick, unless the system asks for reduced motion.

const (
	topoPad      = 12.0
	topoHeaderH  = 20.0
	topoNodeW    = 8.0
	topoGap      = 6.0
	topoMinH     = 16.0
	topoLabelGap = 6.0
	// topoEase is the time constant of the width animation.
	topoEase = 380 * time.Millisecond
	// topoSettle is the weight change, as a share of the total, under
	// which the animation is done.
	topoSettle = 0.002
	// topoFPS is the frame rate of the width animation: bands easing
	// over a third of a second look as smooth at 60 as at 120.
	topoFPS = 60
)

// topoRouteColors colour the routes after the main one, as the exit map
// does (overview.routeColors: 系统紫、粉、青、橙).
var topoRouteColors = []ui.Color{ui.Hex("#AF52DE"), ui.Hex("#FF2D55"), ui.Hex("#30B0C7"), ui.Hex("#FF9500")}

// topology is the state of the 拓扑 view.
type topology struct {
	byTotal    int  // 0 速度, 1 流量
	withClosed bool // include the recently closed connections
	byPort     bool // sources as IP:port

	graph  *Graph
	layout Layout // of the last paint, in window coordinates
	area   ui.Rect
	hover  struct{ node, link int }

	// shown are the eased weights, by node and link key.
	shown map[string]float64
	last  time.Time
	elide map[elideKey]string

	// lay is the layout of the last paint, kept while the graph, the
	// area and the weights stay the same (a settled animation).
	lay struct {
		g        *Graph
		o        layoutKey
		nodeW    []float64
		linkW    []float64
		l        Layout
		measured bool
	}
	path ui.Path // a band's shape, its memory reused

	// icons draws the application icons of the 进程 column (the page's).
	icons *procicon.Cache
}

// topoIconSize is the edge of a process node's icon, in DIPs.
const topoIconSize = 14

// topology draws the 拓扑 view under the header.
func (p *Page) topology(c *ui.Context, t *ui.Theme, active, closed []model.Connection) {
	tp := &p.topo
	src := active
	if tp.withClosed {
		src = append(append(make([]model.Connection, 0, len(active)+len(closed)), active...), closed...)
	}
	conns := p.filter(src)
	if len(conns) == 0 {
		tp.graph = nil
		if strings.TrimSpace(p.query) != "" {
			p.e.Kit.Empty(c, "search", i18n.Tf("没有匹配“%s”的连接", strings.TrimSpace(p.query)), "", nil)
		} else {
			p.e.Kit.Empty(c, "conns", i18n.T("暂无活动连接"), i18n.T("有连接经过澜流时，这里会画出它们从设备到目标的路径。"), nil)
		}
		return
	}
	px := p.e.API.Proxies()
	o := Options{
		MaxPerColumn: tp.limit(),
		ByTotal:      tp.byTotal == 1,
		ExpandSource: tp.byPort,
		Groups:       GroupSet(px),
		MainGroup:    px.MainGroup,
		MainNode:     ResolveNode(px, px.MainGroup),
		Prev:         tp.graph,
	}
	g := BuildGraph(conns, o)
	tp.graph = g
	reduce := c.Preferences().ReduceMotion

	m := ui.Box(c.Key("topology")).Grow(1).FillWidth().Clip().Label(i18n.T("网络拓扑")).Description(topoDescription(g))
	// Hover and click use the geometry of the last paint.
	tp.hover.node, tp.hover.link = -1, -1
	if x, y, over := m.PointerPosition(); over && len(tp.layout.Boxes) == len(g.Nodes) {
		ni, li := tp.layout.Hit(x+tp.area.X, y+tp.area.Y)
		tp.hover.node, tp.hover.link = ni, li
		switch {
		case ni >= 0:
			n := g.Nodes[ni]
			tip := n.Label + " · " + topoStats(n.Count, n.UpSpeed, n.DownSpeed, n.Upload+n.Download)
			if n.Filter != "" {
				tip += i18n.T(" · 点击在列表中查看")
				m.Cursor(ui.CursorPointer)
				if m.Clicked() {
					p.showInList(n.Filter)
				}
			}
			m.Tooltip(tip)
		case li >= 0:
			l := g.Links[li]
			m.Tooltip(g.Nodes[l.From].Label + " → " + g.Nodes[l.To].Label + " · " + topoStats(l.Count, l.UpSpeed, l.DownSpeed, l.Upload+l.Download))
		}
	}
	// The widths ease in the paint, from the frame's time: an animation
	// frame paints again without building the view, so easing here keeps
	// moving between builds.
	m.Draw(func(pt *ui.Painter, r ui.Rect) {
		nodeW, linkW, moving := tp.smooth(g, pt.Now(), reduce)
		tp.draw(pt, r, t, g, nodeW, linkW)
		if moving {
			pt.AnimationFrameRate(topoFPS)
		}
	})
}

// showInList switches to the list view filtered to q.
func (p *Page) showInList(q string) {
	p.query = q
	p.view = 0
	if p.topo.withClosed && p.tab == 1 {
		return
	}
	p.tab = 0
}

// limit is how many nodes a column shows, fewer in a short box.
func (tp *topology) limit() int {
	if tp.area.H <= 0 {
		return defaultMaxPerColumn
	}
	n := int((tp.area.H - topoHeaderH - 2*topoPad) / (topoMinH + topoGap + 6))
	return max(3, min(defaultMaxPerColumn, n))
}

// smooth eases the shown weights towards the graph's and returns them,
// and whether they are still moving.
func (tp *topology) smooth(g *Graph, now time.Time, reduce bool) (nodes, links []float64, moving bool) {
	nodes = make([]float64, len(g.Nodes))
	links = make([]float64, len(g.Links))
	snap := reduce || tp.shown == nil
	if tp.shown == nil {
		tp.shown = map[string]float64{}
	}
	k := 1.0
	if !snap {
		dt := now.Sub(tp.last)
		if dt < 0 || dt > 120*time.Millisecond {
			dt = 120 * time.Millisecond
		}
		k = 1 - math.Exp(-float64(dt)/float64(topoEase))
	}
	tp.last = now
	settle := g.Total * topoSettle
	if settle <= 0 {
		settle = 1e-6
	}
	step := func(key string, target float64) float64 {
		cur, ok := tp.shown[key]
		if !ok || snap {
			cur = target
		} else {
			cur += (target - cur) * k
			if math.Abs(target-cur) > settle {
				moving = true
			} else {
				cur = target
			}
		}
		tp.shown[key] = cur
		return cur
	}
	live := make(map[string]bool, len(g.Nodes)+len(g.Links))
	for i, n := range g.Nodes {
		key := nodeKey(n)
		live[key] = true
		nodes[i] = step(key, n.Weight)
	}
	for i, l := range g.Links {
		key := nodeKey(g.Nodes[l.From]) + "\x01" + nodeKey(g.Nodes[l.To])
		live[key] = true
		links[i] = step(key, l.Weight)
	}
	for key := range tp.shown {
		if !live[key] {
			delete(tp.shown, key)
		}
	}
	return nodes, links, moving
}

func nodeKey(n Node) string { return fmt.Sprintf("%d\x00%s", n.Column, n.Key) }

// routeColor is the colour of the traffic leaving through route, as the
// exit map colours its routes: the main one in the accent, the rest in
// turn; DIRECT green, REJECT red, unknown muted.
func routeColor(route string, g *Graph, t *ui.Theme) ui.Color {
	switch route {
	case "DIRECT":
		return t.Success
	case "REJECT":
		return t.Danger
	case "":
		return t.TextMuted
	}
	for i, r := range g.Routes {
		if r == route {
			if i == 0 {
				return t.Accent
			}
			return topoRouteColors[(i-1)%len(topoRouteColors)]
		}
	}
	return t.TextMuted
}

// draw paints the graph in r.
func (tp *topology) draw(pt *ui.Painter, r ui.Rect, t *ui.Theme, g *Graph, nodeW, linkW []float64) {
	tp.area = r
	if r.W < 120 || r.H < 60 {
		tp.layout = Layout{}
		return
	}
	// The last column's labels sit to the right of its bars.
	lastLabelW := float32(math.Min(150, float64(r.W)*0.17))
	lo := LayoutOptions{
		X: r.X + topoPad, Y: r.Y + topoPad + topoHeaderH,
		W: r.W - 2*topoPad - lastLabelW, H: r.H - 2*topoPad - topoHeaderH,
		NodeW: topoNodeW, Gap: topoGap, MinH: topoMinH,
		NodeWeights: nodeW, LinkWeights: linkW,
	}
	l := tp.layoutGraph(g, lo)
	lit := l.Highlight(g, tp.hover.node, tp.hover.link)
	tp.layout = l
	hovering := tp.hover.node >= 0 || tp.hover.link >= 0
	step := (lo.W - topoNodeW) / float32(numColumns-1)
	labelW := step - topoNodeW - 2*topoLabelGap

	// Column headers.
	for c := Column(0); c < numColumns; c++ {
		pt.RichText(l.ColumnX[c], r.Y+topoPad, 0, ui.Span{Text: i18n.T(columnTitles[c]), Size: 11, Weight: 600, Color: t.TextMuted})
	}
	// Bands, dim ones first so lit ones draw over them.
	for pass := 0; pass < 2; pass++ {
		for _, b := range l.Bands {
			if (pass == 0) == b.Highlight {
				continue
			}
			col := routeColor(g.Links[b.Link].Route, g, t)
			alpha := float32(0.3)
			switch {
			case b.Highlight:
				alpha = 0.72
			case b.Dim:
				alpha = 0.08
			}
			pt.FillPath(bandPath(&tp.path, b), col.Alpha(alpha))
		}
	}
	// Nodes and labels.
	for _, b := range l.Boxes {
		n := g.Nodes[b.Node]
		col := t.Text.Alpha(0.75)
		if n.Column == ColNode {
			col = routeColor(n.Route, g, t)
		}
		if n.Other {
			col = t.TextMuted.Alpha(0.6)
		}
		textCol := t.Text
		if hovering && !lit[b.Node] {
			col = col.Alpha(0.3)
			textCol = t.TextMuted.Alpha(0.6)
		} else if lit[b.Node] && b.Node == tp.hover.node {
			pt.Fill(ui.Rect{X: b.X - 2, Y: b.Y - 2, W: b.W + 4, H: b.H + 4}, col.Alpha(0.35), 4)
		}
		pt.Fill(ui.Rect{X: b.X, Y: b.Y, W: b.W, H: b.H}, col, 2)
		maxW := labelW
		if n.Column == ColDest {
			maxW = lastLabelW - topoLabelGap - 4
		}
		if maxW < 24 {
			continue
		}
		x := b.X + b.W + topoLabelGap
		// A process node's label starts with its application icon when
		// the bar is tall enough and the label keeps room for a name.
		icon := n.Column == ColProcess && !n.Other && !strings.HasPrefix(n.Key, "\x00") && tp.icons != nil &&
			b.H >= topoIconSize && maxW >= topoIconSize+topoLabelGap+24
		if icon {
			maxW -= topoIconSize + topoLabelGap
		}
		label := ui.Span{Text: tp.elided(pt, n.Label, maxW, 11, 500), Size: 11, Weight: 500, Color: textCol}
		// The second line carries live figures that change every tick:
		// it is fitted anew rather than cached.
		_, lh := pt.MeasureText(0, label)
		sub := ""
		if b.H >= lh*2+2 {
			sub = topoSub(n, tp.byTotal == 1)
		}
		y := b.Y + (b.H-lh)/2
		var sh float32
		subSpan := ui.Span{Text: elide(pt, sub, maxW, 10, 400), Size: 10, Color: t.TextMuted}
		if sub != "" {
			_, sh = pt.MeasureText(0, subSpan)
			y = b.Y + (b.H-lh-sh)/2
		}
		// A translucent halo keeps the text legible where bands run
		// under it.
		lw, _ := pt.MeasureText(0, label)
		hw := lw
		if sub != "" {
			if sw, _ := pt.MeasureText(0, subSpan); sw > hw {
				hw = sw
			}
		}
		if icon {
			hw += topoIconSize + topoLabelGap
		}
		if hw > 0 {
			pt.Fill(ui.Rect{X: x - 3, Y: y - 1, W: hw + 6, H: lh + sh + 2}, t.Background.Alpha(0.72), 3)
		}
		if icon {
			tp.icons.Paint(pt, n.Path, ui.Rect{X: x, Y: y + (lh-topoIconSize)/2, W: topoIconSize, H: topoIconSize})
			x += topoIconSize + topoLabelGap
		}
		pt.RichText(x, y, 0, label)
		if sub != "" {
			pt.RichText(x, y+lh, 0, subSpan)
		}
	}
}

// topoSub is the second line of a node: its connections and traffic.
func topoSub(n Node, byTotal bool) string {
	if byTotal {
		return i18n.Tf("%d 个连接 · %s", n.Count, env.FmtBytes(n.Upload+n.Download))
	}
	return i18n.Tf("%d 个连接 · %s", n.Count, env.FmtSpeed(n.UpSpeed+n.DownSpeed))
}

// topoStats is the tooltip's figures.
func topoStats(count int, up, down, total int64) string {
	return i18n.Tf("%d 个连接 · ↓ %s ↑ %s · 共 %s", count, env.FmtSpeed(down), env.FmtSpeed(up), env.FmtBytes(total))
}

// topoDescription describes the graph to assistive technology.
func topoDescription(g *Graph) string {
	var b strings.Builder
	b.WriteString(i18n.Tf("%d 个连接的流向，从来源经进程、规则、代理组、节点到目标。", len(g.Paths)))
	var nodes []string
	for _, ni := range g.Columns[ColNode] {
		nodes = append(nodes, g.Nodes[ni].Label)
	}
	if len(nodes) > 0 {
		b.WriteString(i18n.Tf("节点：%s", strings.Join(nodes, i18n.T("、"))))
	}
	return b.String()
}

// layoutKey is LayoutOptions without the weights.
type layoutKey struct{ x, y, w, h, nodeW, gap, minH float32 }

// layoutGraph is LayoutGraph, reusing the last layout while nothing it
// depends on changed.
func (tp *topology) layoutGraph(g *Graph, o LayoutOptions) Layout {
	ly := &tp.lay
	key := layoutKey{o.X, o.Y, o.W, o.H, o.NodeW, o.Gap, o.MinH}
	if ly.measured && ly.g == g && ly.o == key && slices.Equal(ly.nodeW, o.NodeWeights) && slices.Equal(ly.linkW, o.LinkWeights) {
		return ly.l
	}
	ly.l = LayoutGraph(g, o)
	ly.g, ly.o, ly.measured = g, key, true
	ly.nodeW = append(ly.nodeW[:0], o.NodeWeights...)
	ly.linkW = append(ly.linkW[:0], o.LinkWeights...)
	return ly.l
}

// bandPath is the filled shape of a band: two cubic curves whose control
// points sit at the middle x, as bandX and bandY compute them. It is
// built in p, whose memory each band reuses.
func bandPath(p *ui.Path, b Band) *ui.Path {
	xm := (b.X0 + b.X1) / 2
	return p.Reset().MoveTo(b.X0, b.Y0).
		CubeTo(xm, b.Y0, xm, b.Y2, b.X1, b.Y2).
		LineTo(b.X1, b.Y3).
		CubeTo(xm, b.Y3, xm, b.Y1, b.X0, b.Y1).
		Close()
}

// elideKey is what an elided label depends on.
type elideKey struct {
	s      string
	maxW   int
	size   int
	weight int
}

// elided shortens s with an ellipsis to fit maxW, caching the result: for
// the node names, which stay while their widths change little.
func (tp *topology) elided(pt *ui.Painter, s string, maxW, size float32, weight int) string {
	key := elideKey{s, int(maxW), int(size), weight}
	if v, ok := tp.elide[key]; ok {
		return v
	}
	if tp.elide == nil || len(tp.elide) > 2000 {
		tp.elide = map[elideKey]string{}
	}
	out := elide(pt, s, maxW, size, weight)
	tp.elide[key] = out
	return out
}

// elide shortens s with an ellipsis to fit maxW.
func elide(pt *ui.Painter, s string, maxW, size float32, weight int) string {
	fits := func(x string) bool {
		w, _ := pt.MeasureText(0, ui.Span{Text: x, Size: size, Weight: weight})
		return w <= maxW
	}
	out := s
	if !fits(s) {
		rs := []rune(s)
		lo, hi := 0, len(rs)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if fits(string(rs[:mid]) + "…") {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		out = string(rs[:lo]) + "…"
	}
	return out
}
