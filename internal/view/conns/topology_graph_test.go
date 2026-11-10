package conns

import (
	"fmt"
	"testing"

	"lanflow/internal/model"
)

// sampleConns are a few connections of the shapes the core reports.
func sampleConns() []model.Connection {
	return []model.Connection{
		{ID: "1", Host: "www.google.com:443", Process: "Safari", Rule: "DomainSuffix", RulePayload: "google.com", Chains: []string{"节点选择", "自动选择", "香港 01"}, SourceIP: "192.168.1.5", SourcePort: "50001", UpSpeed: 1000, DownSpeed: 9000, Upload: 10, Download: 90},
		{ID: "2", Host: "mail.google.com:443", Process: "Safari", Rule: "DomainSuffix", RulePayload: "google.com", Chains: []string{"节点选择", "自动选择", "香港 01"}, SourceIP: "192.168.1.5", SourcePort: "50002", UpSpeed: 500, DownSpeed: 500, Upload: 5, Download: 5},
		{ID: "3", Host: "i.ytimg.com:443", Process: "Google Chrome", Rule: "GeoSite", RulePayload: "youtube", Chains: []string{"流媒体", "东京 03"}, SourceIP: "192.168.1.5", SourcePort: "50003", UpSpeed: 100, DownSpeed: 40000, Upload: 1, Download: 400},
		{ID: "4", Host: "192.168.1.1:80", Rule: "IPCIDR", RulePayload: "192.168.0.0/16", Chains: []string{"DIRECT"}, SourceIP: "192.168.1.9", SourcePort: "50004"},
		{ID: "5", Host: "ads.example.net:443", Process: "Safari", Rule: "Match", Chains: []string{"REJECT"}, SourceIP: "192.168.1.5", SourcePort: "50005"},
	}
}

var sampleGroups = map[string]bool{"节点选择": true, "自动选择": true, "流媒体": true}

func labels(g *Graph, c Column) []string {
	var out []string
	for _, ni := range g.Columns[c] {
		out = append(out, g.Nodes[ni].Label)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBuildGraphColumns(t *testing.T) {
	g := BuildGraph(sampleConns(), Options{Groups: sampleGroups, MainGroup: "节点选择", MainNode: "香港 01"})
	if len(g.Paths) != 5 {
		t.Fatalf("%d paths", len(g.Paths))
	}
	// Ranked by count, then traffic, then key.
	if got := labels(g, ColSource); !eq(got, []string{"192.168.1.5", "192.168.1.9"}) {
		t.Errorf("sources %q", got)
	}
	if got := labels(g, ColProcess); !eq(got, []string{"Safari", "Google Chrome", "未知进程"}) {
		t.Errorf("processes %q", got)
	}
	if got := labels(g, ColRule); !eq(got, []string{"DOMAIN-SUFFIX google.com", "GEO-SITE youtube", "IPCIDR 192.168.0.0/16", "MATCH"}) {
		t.Errorf("rules %q", got)
	}
	if got := labels(g, ColGroup); !eq(got, []string{"节点选择 › 自动选择", "流媒体", "DIRECT", "REJECT"}) {
		t.Errorf("groups %q", got)
	}
	if got := labels(g, ColNode); !eq(got, []string{"香港 01", "东京 03", "DIRECT", "REJECT"}) {
		t.Errorf("nodes %q", got)
	}
	if got := labels(g, ColDest); !eq(got, []string{"google.com", "ytimg.com", "192.168.1.1", "example.net"}) {
		t.Errorf("dests %q", got)
	}
	// Routes: the main group's node first, DIRECT and REJECT left out.
	if !eq(g.Routes, []string{"香港 01", "东京 03"}) {
		t.Errorf("routes %q", g.Routes)
	}
	// Every path's nodes sit in their columns and its links exist.
	for i, p := range g.Paths {
		for c := Column(0); c < numColumns; c++ {
			if g.Nodes[p[c]].Column != c {
				t.Errorf("path %d column %d points at a node of column %d", i, c, g.Nodes[p[c]].Column)
			}
		}
	}
	if len(g.Links) == 0 {
		t.Fatal("no links")
	}
	for _, l := range g.Links {
		if g.Nodes[l.To].Column != g.Nodes[l.From].Column+1 {
			t.Errorf("link %d→%d spans columns %d→%d", l.From, l.To, g.Nodes[l.From].Column, g.Nodes[l.To].Column)
		}
		if l.Route == "" {
			t.Errorf("link %d→%d has no route", l.From, l.To)
		}
	}
	// Sums: the node column's 香港 01 carries the two Safari Google connections.
	hk := g.Nodes[g.Columns[ColNode][0]]
	if hk.Count != 2 || hk.DownSpeed != 9500 || hk.UpSpeed != 1500 || hk.Download != 95 || hk.Filter != "香港 01" || hk.Route != "香港 01" {
		t.Errorf("香港 01: %+v", hk)
	}
	if g.Total != 51100 {
		t.Errorf("total weight %v", g.Total)
	}
	// The weights of a column sum to the total.
	for c := Column(0); c < numColumns; c++ {
		sum := 0.0
		for _, ni := range g.Columns[c] {
			sum += g.Nodes[ni].Weight
		}
		if sum != g.Total {
			t.Errorf("column %d weighs %v, total %v", c, sum, g.Total)
		}
	}
}

func TestBuildGraphWeightsAndSources(t *testing.T) {
	conns := sampleConns()
	g := BuildGraph(conns, Options{Groups: sampleGroups, ByTotal: true, ExpandSource: true})
	if g.Total != 511 {
		t.Errorf("total bytes %v", g.Total)
	}
	if got := labels(g, ColSource); len(got) != 5 || got[0] != "192.168.1.5:50003" {
		t.Errorf("expanded sources %q", got)
	}
	// Nothing flowing: weights fall back to counts.
	for i := range conns {
		conns[i].UpSpeed, conns[i].DownSpeed = 0, 0
	}
	g = BuildGraph(conns, Options{Groups: sampleGroups})
	if g.Total != 5 {
		t.Errorf("idle total %v", g.Total)
	}
	for _, l := range g.Links {
		if l.Weight != float64(l.Count) {
			t.Errorf("idle link weight %v for %d connections", l.Weight, l.Count)
		}
	}
	// Without the group set, every chain entry but the last is a group.
	g = BuildGraph(sampleConns(), Options{})
	if got := labels(g, ColGroup); got[0] != "节点选择 › 自动选择" || got[2] != "DIRECT" {
		t.Errorf("groups by position %q", got)
	}
}

func TestBuildGraphOtherBucketAndStableOrder(t *testing.T) {
	// a.com ×3, b.com ×2, c.com ×1 (with traffic), then nine singles.
	conn := func(id, dest string, down int64) model.Connection {
		return model.Connection{ID: id, Host: "www." + dest + ":443", Process: "p", Chains: []string{"G", "N"}, SourceIP: "10.0.0.1", Download: down}
	}
	conns := []model.Connection{conn("a1", "a.com", 0), conn("a2", "a.com", 0), conn("a3", "a.com", 0), conn("b1", "b.com", 0), conn("b2", "b.com", 0), conn("c1", "c.com", 1000)}
	for i := 0; i < 9; i++ {
		conns = append(conns, conn(fmt.Sprint("s", i), fmt.Sprintf("single%d.net", i), 0))
	}
	g := BuildGraph(conns, Options{MaxPerColumn: 4})
	if got := labels(g, ColDest); !eq(got, []string{"a.com", "b.com", "c.com", "其他 9 项"}) {
		t.Fatalf("dests with a limit of 4: %q", got)
	}
	other := g.Nodes[g.Columns[ColDest][3]]
	if !other.Other || other.Count != 9 || other.Filter != "" {
		t.Errorf("other bucket: %+v", other)
	}
	if got := labels(g, ColProcess); !eq(got, []string{"p"}) {
		t.Errorf("a column within the limit keeps all its nodes: %q", got)
	}
	for _, p := range g.Paths {
		if g.Nodes[p[ColDest]].Column != ColDest {
			t.Error("a path into 其他 lost its column")
		}
	}
	// b.com overtakes a.com: the previous order holds with Prev, not without.
	conns = append(conns, conn("b3", "b.com", 0), conn("b4", "b.com", 0))
	if got := labels(BuildGraph(conns, Options{MaxPerColumn: 4}), ColDest); !eq(got[:2], []string{"b.com", "a.com"}) {
		t.Errorf("fresh order: %q", got)
	}
	g2 := BuildGraph(conns, Options{MaxPerColumn: 4, Prev: g})
	if got := labels(g2, ColDest); !eq(got, []string{"a.com", "b.com", "c.com", "其他 9 项"}) {
		t.Errorf("order changed with the counts: %q", got)
	}
	// A newcomer that ranks first goes after the kept nodes; what it pushes
	// out joins 其他.
	for i := 0; i < 5; i++ {
		conns = append(conns, conn(fmt.Sprint("d", i), "d.com", 0))
	}
	g3 := BuildGraph(conns, Options{MaxPerColumn: 4, Prev: g2})
	if got := labels(g3, ColDest); !eq(got, []string{"a.com", "b.com", "d.com", "其他 10 项"}) {
		t.Errorf("order with a newcomer: %q", got)
	}
}

func TestSplitChainAndResolve(t *testing.T) {
	groups := map[string]bool{"A": true, "B": true}
	if gs, n := splitChain([]string{"A", "B", "x"}, groups); !eq(gs, []string{"A", "B"}) || n != "x" {
		t.Errorf("%q %q", gs, n)
	}
	if gs, n := splitChain([]string{"A", "B"}, groups); !eq(gs, []string{"A"}) || n != "B" {
		t.Errorf("all groups: %q %q", gs, n)
	}
	if gs, n := splitChain([]string{"DIRECT"}, groups); len(gs) != 0 || n != "DIRECT" {
		t.Errorf("DIRECT: %q %q", gs, n)
	}
	if gs, n := splitChain(nil, groups); gs != nil || n != "" {
		t.Errorf("empty: %q %q", gs, n)
	}
	px := model.ProxySnapshot{
		Groups:    []model.ProxyGroup{{Name: "A", Now: "B"}, {Name: "B", Now: "n1"}, {Name: "L", Now: ""}, {Name: "C1", Now: "C2"}, {Name: "C2", Now: "C1"}},
		Proxies:   map[string]model.Proxy{"n1": {Name: "n1"}},
		MainGroup: "A",
	}
	if got := ResolveNode(px, "A"); got != "n1" {
		t.Errorf("resolve A = %q", got)
	}
	if got := ResolveNode(px, "L"); got != "" {
		t.Errorf("resolve load-balance = %q", got)
	}
	if got := ResolveNode(px, "C1"); got != "" {
		t.Errorf("resolve cycle = %q", got)
	}
	if got := ResolveNode(px, "DIRECT"); got != "DIRECT" {
		t.Errorf("resolve DIRECT = %q", got)
	}
	if gs := GroupSet(px); len(gs) != 5 || !gs["A"] {
		t.Errorf("group set %v", gs)
	}
	if GroupSet(model.ProxySnapshot{}) != nil {
		t.Error("an empty snapshot should give no set")
	}
}

func TestLayoutGraph(t *testing.T) {
	g := BuildGraph(sampleConns(), Options{Groups: sampleGroups, MainNode: "香港 01"})
	o := LayoutOptions{X: 10, Y: 20, W: 600, H: 300, NodeW: 8, Gap: 6, MinH: 16}
	l := LayoutGraph(g, o)
	if len(l.Boxes) != len(g.Nodes) || len(l.Bands) != len(g.Links) {
		t.Fatalf("%d boxes, %d bands", len(l.Boxes), len(l.Bands))
	}
	for c := Column(0); c < numColumns; c++ {
		var prev *Box
		for _, ni := range g.Columns[c] {
			b := l.Boxes[ni]
			if b.X < o.X || b.X+b.W > o.X+o.W+0.01 || b.Y < o.Y || b.Y+b.H > o.Y+o.H+0.01 {
				t.Errorf("box %q out of the area: %+v", g.Nodes[ni].Label, b)
			}
			if b.H < o.MinH-0.01 {
				t.Errorf("box %q is %v high", g.Nodes[ni].Label, b.H)
			}
			if prev != nil && b.Y < prev.Y+prev.H+o.Gap-0.01 {
				t.Errorf("box %q overlaps the one above", g.Nodes[ni].Label)
			}
			prev = &b
		}
	}
	if l.ColumnX[ColSource] != o.X || l.ColumnX[ColDest] != o.X+o.W-o.NodeW {
		t.Errorf("column x: %v", l.ColumnX)
	}
	// The bands leaving a node fill its height; those arriving too.
	out := map[int]float32{}
	in := map[int]float32{}
	for _, b := range l.Bands {
		lk := g.Links[b.Link]
		out[lk.From] += b.Y1 - b.Y0
		in[lk.To] += b.Y3 - b.Y2
		if b.X0 != l.Boxes[lk.From].X+o.NodeW || b.X1 != l.Boxes[lk.To].X {
			t.Errorf("band of link %d→%d spans %v–%v", lk.From, lk.To, b.X0, b.X1)
		}
	}
	for ni, h := range out {
		if d := h - l.Boxes[ni].H; d > 0.5 || d < -0.5 {
			t.Errorf("bands out of %q sum to %v, box is %v", g.Nodes[ni].Label, h, l.Boxes[ni].H)
		}
	}
	for ni, h := range in {
		if d := h - l.Boxes[ni].H; d > 0.5 || d < -0.5 {
			t.Errorf("bands into %q sum to %v, box is %v", g.Nodes[ni].Label, h, l.Boxes[ni].H)
		}
	}
	// The biggest node of the node column is the 40 kB/s one.
	var biggest Box
	for _, ni := range g.Columns[ColNode] {
		if l.Boxes[ni].H > biggest.H {
			biggest = l.Boxes[ni]
		}
	}
	if g.Nodes[biggest.Node].Label != "东京 03" {
		t.Errorf("biggest node is %q", g.Nodes[biggest.Node].Label)
	}

	// Hit testing: a node's bar, the middle of a band, and nothing.
	b := l.Boxes[g.Columns[ColNode][0]]
	if ni, li := l.Hit(b.X+2, b.Y+b.H/2); ni != b.Node || li != -1 {
		t.Errorf("hit on a bar: node %d link %d", ni, li)
	}
	band := l.Bands[0]
	mx := (band.X0 + band.X1) / 2
	my := (bandY(band.Y0, band.Y2, 0.5) + bandY(band.Y1, band.Y3, 0.5)) / 2
	if ni, li := l.Hit(mx, my); ni != -1 || li != band.Link {
		t.Errorf("hit on a band: node %d link %d (want link %d)", ni, li, band.Link)
	}
	if ni, li := l.Hit(o.X-20, o.Y-20); ni != -1 || li != -1 {
		t.Errorf("hit outside: node %d link %d", ni, li)
	}

	// Highlighting a node lights every band of the paths through it and
	// dims the rest; highlighting a link does the same by the link.
	// 香港 01 carries two paths that share every node (source, process,
	// rule, group, node and google.com): six nodes and five bands.
	lit := l.Highlight(g, b.Node, -1)
	if len(lit) != int(numColumns) {
		t.Errorf("%d nodes lit", len(lit))
	}
	n := 0
	for _, bd := range l.Bands {
		if bd.Highlight {
			n++
		} else if !bd.Dim {
			t.Error("a band neither lit nor dim")
		}
	}
	if n != int(numColumns)-1 {
		t.Errorf("%d bands lit", n)
	}
	l.Highlight(g, -1, -1)
	for _, bd := range l.Bands {
		if bd.Highlight || bd.Dim {
			t.Error("a band stays lit or dim with nothing hovered")
		}
	}
	// An empty graph lays out nothing.
	if l := LayoutGraph(&Graph{}, o); len(l.Boxes) != 0 {
		t.Error("empty graph has boxes")
	}
}

func TestHeightsMinimum(t *testing.T) {
	w := []float64{1000, 1, 1, 1}
	hs := heights([]int{0, 1, 2, 3}, func(i int) float64 { return w[i] }, 200, 4, 16)
	if hs[1] != 16 || hs[2] != 16 || hs[3] != 16 {
		t.Errorf("small nodes: %v", hs)
	}
	if d := hs[0] - (200 - 3*4 - 3*16); d > 0.01 || d < -0.01 {
		t.Errorf("big node takes the rest: %v", hs)
	}
	// Too many for the minimum: equal shares.
	hs = heights([]int{0, 1, 2, 3}, func(i int) float64 { return w[i] }, 40, 4, 16)
	for _, h := range hs {
		if h != 7 {
			t.Errorf("cramped: %v", hs)
		}
	}
}
