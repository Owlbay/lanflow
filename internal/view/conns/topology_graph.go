package conns

import (
	"sort"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/rules/ruleform"
)

// The 拓扑 view of the 连接 page draws the connections as a layered flow
// graph (a Sankey): every connection is a path through six columns, 来源 →
// 进程 → 规则 → 代理组 → 节点 → 目标, and the width of a band between two
// columns is the traffic the connections on it carry. This file builds
// the graph and lays it out; topology.go draws it.

// Column is one layer of the graph.
type Column int

const (
	ColSource Column = iota // the device: IP, or IP:port when expanded
	ColProcess
	ColRule  // rule type and payload
	ColGroup // the proxy groups of the chain, outer → inner
	ColNode  // the final node, DIRECT or REJECT
	ColDest  // the registrable domain, or the IP
	numColumns
)

// columnTitles are the column headers (i18n keys).
var columnTitles = [numColumns]string{"来源", "进程", "规则", "代理组", "节点", "目标"}

// Node is one box of the graph.
type Node struct {
	Column Column
	Key    string // unique within the column
	Label  string
	Other  bool // the 「其他」 bucket of the nodes beyond the column's limit
	// Filter is the search query that shows the node's connections in the
	// list view; "" when there is none (the 「其他」 bucket).
	Filter string
	// Route is the final node the traffic leaves by, the colour of a
	// ColNode node; "" elsewhere.
	Route string
	// Path is the executable's path of a ColProcess node, for its icon.
	Path string

	Count              int   // connections
	UpSpeed, DownSpeed int64 // bytes/s
	Upload, Download   int64 // bytes
	// Weight is what the node's height stands for: speed or total bytes
	// (Options.ByTotal), or 1 per connection when the column carries no
	// traffic at all.
	Weight float64
}

// Link is a band between a node and one of the next column.
type Link struct {
	From, To           int // node indices
	Route              string
	Count              int
	UpSpeed, DownSpeed int64
	Upload, Download   int64
	Weight             float64
}

// Graph is the built graph: nodes, the links between neighbouring
// columns, and for every connection the nodes of its path.
type Graph struct {
	Nodes   []Node
	Links   []Link
	Columns [numColumns][]int // node indices by column, in drawing order
	Paths   [][numColumns]int // one per connection: its node in each column
	// Routes are the final nodes in colour order: the main group's current
	// node first, then the others as they appear; DIRECT and REJECT are
	// not among them (they have colours of their own).
	Routes []string
	// Total is the sum of the weights of one column (the same for all).
	Total float64
}

// Options shape the graph.
type Options struct {
	// MaxPerColumn is the number of nodes a column shows; the rest are
	// merged into 「其他」. Zero means defaultMaxPerColumn.
	MaxPerColumn int
	// ByTotal weighs the bands by total bytes rather than by speed.
	ByTotal bool
	// ExpandSource splits a device into its IP:port sources.
	ExpandSource bool
	// Groups are the names the chain entries that are proxy groups have;
	// without it every entry but the last counts as a group.
	Groups map[string]bool
	// MainGroup names the main group so that its node is coloured first.
	MainGroup string
	// MainNode is the node the main group resolves to now (ResolveNode).
	MainNode string
	// Prev is the previous graph: nodes keep the order they had in it so
	// the picture does not jump when speeds change.
	Prev *Graph
}

const defaultMaxPerColumn = 8

// BuildGraph aggregates conns into the layered graph.
func BuildGraph(conns []model.Connection, o Options) *Graph {
	limit := o.MaxPerColumn
	if limit <= 0 {
		limit = defaultMaxPerColumn
	}
	// Aggregate the raw nodes of every column.
	type agg struct {
		node  Node
		conns []int
	}
	var cols [numColumns]map[string]*agg
	var keys [numColumns][]string // insertion order
	for c := range cols {
		cols[c] = map[string]*agg{}
	}
	raw := make([][numColumns]string, len(conns))
	for i, cn := range conns {
		for c := Column(0); c < numColumns; c++ {
			n := nodeOf(cn, c, o)
			raw[i][c] = n.Key
			a := cols[c][n.Key]
			if a == nil {
				a = &agg{node: n}
				cols[c][n.Key] = a
				keys[c] = append(keys[c], n.Key)
			}
			a.conns = append(a.conns, i)
			add(&a.node, cn)
		}
	}
	g := &Graph{Paths: make([][numColumns]int, len(conns))}
	// Choose the nodes of every column: the top ones by count then
	// traffic, the rest merged into 「其他」; then order them stably.
	byConn := make([][numColumns]int, len(conns))
	for c := Column(0); c < numColumns; c++ {
		ks := keys[c]
		sort.SliceStable(ks, func(i, j int) bool { return rank(cols[c][ks[i]].node, cols[c][ks[j]].node) })
		var chosen []Node
		var other *Node
		for i, k := range ks {
			a := cols[c][k]
			if i < limit-1 || (i == limit-1 && len(ks) == limit) {
				chosen = append(chosen, a.node)
				continue
			}
			if other == nil {
				other = &Node{Column: c, Key: "\x00other", Label: i18n.T("其他"), Other: true}
			}
			other.Count += a.node.Count
			other.UpSpeed += a.node.UpSpeed
			other.DownSpeed += a.node.DownSpeed
			other.Upload += a.node.Upload
			other.Download += a.node.Download
			for _, ci := range a.conns {
				raw[ci][c] = other.Key
			}
		}
		chosen = orderNodes(chosen, c, o.Prev)
		if other != nil {
			other.Label = i18n.Tf("其他 %d 项", len(ks)-len(chosen))
			chosen = append(chosen, *other)
		}
		index := map[string]int{}
		for _, n := range chosen {
			index[n.Key] = len(g.Nodes)
			g.Columns[c] = append(g.Columns[c], len(g.Nodes))
			g.Nodes = append(g.Nodes, n)
		}
		for i := range conns {
			byConn[i][c] = index[raw[i][c]]
		}
	}
	// Weights: speed or total bytes; by count when nothing flows at all,
	// so the graph still shows.
	countOnly := allZero(g.Nodes, o.ByTotal)
	for i := range g.Nodes {
		n := &g.Nodes[i]
		n.Weight = weight(n.Count, n.UpSpeed+n.DownSpeed, n.Upload+n.Download, o.ByTotal, countOnly)
	}
	for _, ni := range g.Columns[ColSource] {
		g.Total += g.Nodes[ni].Weight
	}
	// Links, in the order of their source then target nodes.
	linkAt := map[[2]int]int{}
	for i, cn := range conns {
		g.Paths[i] = byConn[i]
		for c := Column(0); c < numColumns-1; c++ {
			from, to := byConn[i][c], byConn[i][c+1]
			li, ok := linkAt[[2]int{from, to}]
			if !ok {
				li = len(g.Links)
				linkAt[[2]int{from, to}] = li
				g.Links = append(g.Links, Link{From: from, To: to, Route: g.Nodes[byConn[i][ColNode]].Route})
			}
			l := &g.Links[li]
			l.Count++
			l.UpSpeed += cn.UpSpeed
			l.DownSpeed += cn.DownSpeed
			l.Upload += cn.Upload
			l.Download += cn.Download
		}
	}
	for i := range g.Links {
		l := &g.Links[i]
		l.Weight = weight(l.Count, l.UpSpeed+l.DownSpeed, l.Upload+l.Download, o.ByTotal, countOnly)
	}
	// Routes in colour order: the main group's node, then those of the
	// previous graph that remain, then the rest in column order.
	seen := map[string]bool{}
	if o.MainNode != "" {
		for _, ni := range g.Columns[ColNode] {
			if g.Nodes[ni].Key == o.MainNode {
				g.Routes = append(g.Routes, o.MainNode)
				seen[o.MainNode] = true
			}
		}
	}
	if o.Prev != nil {
		for _, r := range o.Prev.Routes {
			if !seen[r] && hasKey(g, ColNode, r) {
				seen[r] = true
				g.Routes = append(g.Routes, r)
			}
		}
	}
	for _, ni := range g.Columns[ColNode] {
		n := g.Nodes[ni]
		if n.Other || n.Key == "DIRECT" || n.Key == "REJECT" || seen[n.Key] {
			continue
		}
		seen[n.Key] = true
		g.Routes = append(g.Routes, n.Key)
	}
	return g
}

func allZero(nodes []Node, byTotal bool) bool {
	for _, n := range nodes {
		if byTotal && n.Upload+n.Download > 0 || !byTotal && n.UpSpeed+n.DownSpeed > 0 {
			return false
		}
	}
	return true
}

func hasKey(g *Graph, c Column, key string) bool {
	for _, ni := range g.Columns[c] {
		if g.Nodes[ni].Key == key {
			return true
		}
	}
	return false
}

func weight(count int, speed, total int64, byTotal, countOnly bool) float64 {
	switch {
	case countOnly:
		return float64(count)
	case byTotal:
		return float64(total)
	default:
		return float64(speed)
	}
}

func add(n *Node, cn model.Connection) {
	n.Count++
	n.UpSpeed += cn.UpSpeed
	n.DownSpeed += cn.DownSpeed
	n.Upload += cn.Upload
	n.Download += cn.Download
}

// rank orders nodes for choosing the ones a column shows: more
// connections first, then more traffic, then by key.
func rank(a, b Node) bool {
	if a.Count != b.Count {
		return a.Count > b.Count
	}
	if ta, tb := a.Upload+a.Download, b.Upload+b.Download; ta != tb {
		return ta > tb
	}
	return a.Key < b.Key
}

// orderNodes keeps the order the nodes had in prev and appends the new
// ones after them in rank order, so the picture stays put between ticks.
func orderNodes(nodes []Node, c Column, prev *Graph) []Node {
	if prev == nil {
		return nodes
	}
	pos := map[string]int{}
	for i, ni := range prev.Columns[c] {
		if n := prev.Nodes[ni]; !n.Other {
			pos[n.Key] = i
		}
	}
	rankIdx := map[string]int{}
	for i, n := range nodes {
		rankIdx[n.Key] = i
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		pi, oki := pos[nodes[i].Key]
		pj, okj := pos[nodes[j].Key]
		switch {
		case oki && okj:
			return pi < pj
		case oki != okj:
			return oki
		default:
			return rankIdx[nodes[i].Key] < rankIdx[nodes[j].Key]
		}
	})
	return nodes
}

// nodeOf is the node of a connection in a column, without its sums.
func nodeOf(cn model.Connection, c Column, o Options) Node {
	n := Node{Column: c}
	switch c {
	case ColSource:
		src := cn.SourceIP
		if o.ExpandSource && cn.SourcePort != "" && src != "" {
			src += ":" + cn.SourcePort
		}
		if src == "" {
			n.Key, n.Label = "\x00unknown", i18n.T("未知")
			return n
		}
		n.Key, n.Label, n.Filter = src, src, src
	case ColProcess:
		if cn.Process == "" {
			n.Key, n.Label = "\x00unknown", i18n.T("未知进程")
			return n
		}
		n.Key, n.Label, n.Filter, n.Path = cn.Process, cn.Process, cn.Process, cn.ProcessPath
	case ColRule:
		label := ruleName(cn)
		if cn.RulePayload != "" {
			label += " " + cn.RulePayload
		}
		n.Key, n.Label, n.Filter = label, label, label
	case ColGroup:
		groups, _ := splitChain(cn.Chains, o.Groups)
		if len(groups) == 0 {
			if len(cn.Chains) == 0 {
				n.Key, n.Label = "\x00unknown", i18n.T("未知")
				return n
			}
			// DIRECT, REJECT, or a node chosen by a rule directly: the
			// policy is the node itself.
			label := cn.Chains[len(cn.Chains)-1]
			n.Key, n.Label, n.Filter = label, label, label
			return n
		}
		label := strings.Join(groups, " › ")
		n.Key, n.Label, n.Filter = label, label, label
	case ColNode:
		_, node := splitChain(cn.Chains, o.Groups)
		if node == "" {
			n.Key, n.Label = "\x00unknown", i18n.T("未知")
			return n
		}
		n.Key, n.Label, n.Filter, n.Route = node, node, node, node
	case ColDest:
		host := hostOnly(cn.Host)
		if host == "" {
			host = cn.DstIP
		}
		if host == "" {
			n.Key, n.Label = "\x00unknown", i18n.T("未知")
			return n
		}
		d := ruleform.SuffixOf(host)
		n.Key, n.Label, n.Filter = d, d, d
	}
	return n
}

// splitChain splits a connection's chain (outermost first) into its
// groups and the final node. With groups known, the node is the first
// entry that is not a group (the rest after it, if any, are ignored);
// without, every entry but the last is a group.
func splitChain(chains []string, groups map[string]bool) ([]string, string) {
	if len(chains) == 0 {
		return nil, ""
	}
	if groups == nil {
		return chains[:len(chains)-1], chains[len(chains)-1]
	}
	var gs []string
	for _, e := range chains {
		if groups[e] {
			gs = append(gs, e)
			continue
		}
		return gs, e
	}
	// Every entry is a group (a load-balance group reports no member):
	// the innermost stands for the node.
	return gs[:len(gs)-1], gs[len(gs)-1]
}

// ResolveNode follows a group's current choice down to a node, "" when
// the chain does not end (an unknown name, or a cycle).
func ResolveNode(px model.ProxySnapshot, name string) string {
	groups := map[string]model.ProxyGroup{}
	for _, g := range px.Groups {
		groups[g.Name] = g
	}
	for i := 0; i < 16; i++ {
		g, ok := groups[name]
		if !ok {
			if _, known := px.Proxies[name]; known || name == "DIRECT" || name == "REJECT" {
				return name
			}
			return ""
		}
		if g.Now == "" {
			return ""
		}
		name = g.Now
	}
	return ""
}

// GroupSet returns the names of the groups of a proxy snapshot, nil when
// it has none (so splitChain falls back to position).
func GroupSet(px model.ProxySnapshot) map[string]bool {
	if len(px.Groups) == 0 {
		return nil
	}
	m := make(map[string]bool, len(px.Groups))
	for _, g := range px.Groups {
		m[g.Name] = true
	}
	return m
}

// ---- layout ----

// Band is one drawn link: its vertical extent at the source (Y0, Y1) and
// at the target (Y2, Y3), between X0 and X1.
type Band struct {
	Link           int
	X0, Y0, Y1     float32
	X1, Y2, Y3     float32
	Highlight, Dim bool
}

// Box is a laid-out node.
type Box struct {
	Node       int
	X, Y, W, H float32
}

// Layout is the geometry of a graph in a box.
type Layout struct {
	Boxes []Box
	Bands []Band
	// ColumnX is the left edge of the nodes of every column.
	ColumnX [numColumns]float32
}

// LayoutOptions size the layout.
type LayoutOptions struct {
	X, Y, W, H float32 // the area the nodes take (labels and headers excluded)
	NodeW      float32 // width of a node's bar
	Gap        float32 // between nodes of a column
	MinH       float32 // least height of a node
	// Weights override the graph's weights (indexed as Nodes and Links),
	// for the smoothed values of an animation; nil uses the graph's.
	NodeWeights, LinkWeights []float64
}

// LayoutGraph places the nodes in their columns with heights in proportion
// to their weights and lays the bands between them.
func LayoutGraph(g *Graph, o LayoutOptions) Layout {
	var l Layout
	if len(g.Nodes) == 0 || o.W <= 0 || o.H <= 0 {
		return l
	}
	if o.NodeW <= 0 {
		o.NodeW = 8
	}
	nw := func(i int) float64 {
		if o.NodeWeights != nil {
			return o.NodeWeights[i]
		}
		return g.Nodes[i].Weight
	}
	lw := func(i int) float64 {
		if o.LinkWeights != nil {
			return o.LinkWeights[i]
		}
		return g.Links[i].Weight
	}
	l.Boxes = make([]Box, len(g.Nodes))
	step := (o.W - o.NodeW) / float32(numColumns-1)
	for c := Column(0); c < numColumns; c++ {
		idx := g.Columns[c]
		x := o.X + float32(c)*step
		l.ColumnX[c] = x
		hs := heights(idx, nw, o.H, o.Gap, o.MinH)
		y := o.Y
		for i, ni := range idx {
			l.Boxes[ni] = Box{Node: ni, X: x, Y: y, W: o.NodeW, H: hs[i]}
			y += hs[i] + o.Gap
		}
	}
	// Bands, one per link: a node's outgoing bands stack in the order of
	// their targets, its incoming ones in the order of their sources, so
	// they cross as little as possible; each is as tall as its share of
	// the node, at least a hairline so an idle connection still shows.
	pos := make([]int, len(g.Nodes))
	for c := range g.Columns {
		for i, ni := range g.Columns[c] {
			pos[ni] = i
		}
	}
	outs := make([][]int, len(g.Nodes))
	ins := make([][]int, len(g.Nodes))
	for li, lk := range g.Links {
		outs[lk.From] = append(outs[lk.From], li)
		ins[lk.To] = append(ins[lk.To], li)
	}
	l.Bands = make([]Band, len(g.Links))
	for li := range g.Links {
		l.Bands[li].Link = li
	}
	for ni, box := range l.Boxes {
		out, in := outs[ni], ins[ni]
		sort.SliceStable(out, func(a, b int) bool { return pos[g.Links[out[a]].To] < pos[g.Links[out[b]].To] })
		sort.SliceStable(in, func(a, b int) bool { return pos[g.Links[in[a]].From] < pos[g.Links[in[b]].From] })
		y := box.Y
		for i, h := range heights(out, lw, box.H, 0, 1) {
			b := &l.Bands[out[i]]
			b.X0, b.Y0, b.Y1 = box.X+box.W, y, y+h
			y += h
		}
		y = box.Y
		for i, h := range heights(in, lw, box.H, 0, 1) {
			b := &l.Bands[in[i]]
			b.X1, b.Y2, b.Y3 = box.X, y, y+h
			y += h
		}
	}
	return l
}

// heights sizes the nodes of a column: in proportion to their weights,
// at least minH each, the surplus of the small ones taken from the big.
func heights(idx []int, w func(int) float64, H, gap, minH float32) []float32 {
	n := len(idx)
	hs := make([]float32, n)
	if n == 0 {
		return hs
	}
	avail := H - gap*float32(n-1)
	if avail < minH*float32(n) {
		// Not even the minimum fits: share equally.
		h := avail / float32(n)
		if h < 1 {
			h = 1
		}
		for i := range hs {
			hs[i] = h
		}
		return hs
	}
	total := 0.0
	for _, ni := range idx {
		total += w(ni)
	}
	fixed := make([]bool, n)
	for iter := 0; iter < n; iter++ {
		rest, restW := avail, 0.0
		for i, ni := range idx {
			if fixed[i] {
				rest -= minH
			} else {
				restW += w(ni)
			}
		}
		changed := false
		for i, ni := range idx {
			if fixed[i] {
				hs[i] = minH
				continue
			}
			h := rest
			if restW > 0 {
				h = float32(w(ni)/restW) * rest
			} else {
				h = rest / float32(countFalse(fixed))
			}
			if h < minH {
				fixed[i] = true
				changed = true
				continue
			}
			hs[i] = h
		}
		if !changed {
			break
		}
	}
	return hs
}

func countFalse(b []bool) int {
	n := 0
	for _, x := range b {
		if !x {
			n++
		}
	}
	return n
}

// Highlight marks the bands on the paths through node (ni ≥ 0) or along
// link (li ≥ 0) and dims the others; with neither, every band is plain.
// It returns the node indices on the lit paths.
func (l *Layout) Highlight(g *Graph, ni, li int) map[int]bool {
	for i := range l.Bands {
		l.Bands[i].Highlight, l.Bands[i].Dim = false, false
	}
	if ni < 0 && li < 0 {
		return nil
	}
	linkAt := map[[2]int]int{}
	for i, lk := range g.Links {
		linkAt[[2]int{lk.From, lk.To}] = i
	}
	lit := map[int]bool{}
	nodes := map[int]bool{}
	for _, p := range g.Paths {
		on := false
		for c := 0; c < int(numColumns); c++ {
			if p[c] == ni {
				on = true
			}
			if c < int(numColumns)-1 && li >= 0 && linkAt[[2]int{p[c], p[c+1]}] == li {
				on = true
			}
		}
		if !on {
			continue
		}
		for c := 0; c < int(numColumns); c++ {
			nodes[p[c]] = true
			if c < int(numColumns)-1 {
				lit[linkAt[[2]int{p[c], p[c+1]}]] = true
			}
		}
	}
	for i := range l.Bands {
		if lit[l.Bands[i].Link] {
			l.Bands[i].Highlight = true
		} else {
			l.Bands[i].Dim = true
		}
	}
	return nodes
}

// bandX is the x of a band's edge curve at parameter t ∈ [0,1], and
// bandY the y of its top (y0→y2) or bottom edge: the cubic Bézier with
// control points at the middle x, as drawn.
func bandX(b Band, t float32) float32 {
	xm := (b.X0 + b.X1) / 2
	u := 1 - t
	return u*u*u*b.X0 + 3*u*u*t*xm + 3*u*t*t*xm + t*t*t*b.X1
}

func bandY(ya, yb, t float32) float32 {
	return ya + (yb-ya)*(3*t*t-2*t*t*t)
}

// Hit returns the node under (x, y), else the band, else -1, -1.
func (l *Layout) Hit(x, y float32) (node, link int) {
	const slop = 3
	for _, b := range l.Boxes {
		if x >= b.X-slop && x < b.X+b.W+slop && y >= b.Y-1 && y < b.Y+b.H+1 {
			return b.Node, -1
		}
	}
	for _, b := range l.Bands {
		if x < b.X0 || x > b.X1 || b.X1 <= b.X0 {
			continue
		}
		// Invert x(t) by bisection: x(t) is monotonic.
		lo, hi := float32(0), float32(1)
		for i := 0; i < 18; i++ {
			mid := (lo + hi) / 2
			if bandX(b, mid) < x {
				lo = mid
			} else {
				hi = mid
			}
		}
		t := (lo + hi) / 2
		top, bot := bandY(b.Y0, b.Y2, t), bandY(b.Y1, b.Y3, t)
		if bot-top < 4 {
			m := (top + bot) / 2
			top, bot = m-2, m+2
		}
		if y >= top && y <= bot {
			return -1, b.Link
		}
	}
	return -1, -1
}
