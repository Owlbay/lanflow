package overview

import (
	"math"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/model"
)

// Drawing helpers of the globe: great-circle arcs sampled to screen
// polylines, strokes that fade toward the limb, the graticule and the
// labels that avoid each other. They draw with the buffers of
// globeBuffers, reused frame after frame.

const (
	// destSamples is how finely a destination line is sampled; the request
	// arcs use arcSamples.
	destSamples = 24
	// arcLiftBase and arcLiftSpan lift an arc above the surface, in radii:
	// a little for any arc, more the farther it goes (an antipodal arc
	// rises arcLiftBase+arcLiftSpan), and arcLiftStack more for each
	// further route to the same city so that they do not coincide.
	arcLiftBase  = 0.06
	arcLiftSpan  = 0.30
	arcLiftStack = 0.09
	// fadeLevels is how many opacity steps a fading stroke is cut into.
	fadeLevels = 3
)

var hidden = [2]float32{float32(math.NaN()), float32(math.NaN())}

// globeBuffers is the scratch memory of a frame.
type globeBuffers struct {
	path   ui.Path
	line   [][2]float32
	labels []ui.Rect
	dest   arc

	// What drawMap gathers each frame, emptied and refilled.
	fit, fitDests []model.Place
	fitMemo       fitMemo
	exits         []exitDraw
	seen          map[string]int
	cities        map[string]bool
	destMarks     []destMark
	arrivals      []arrival
	speeds        []speedLabel
	speedIn       map[string]string
}

// reset empties the per-frame gatherings, keeping their memory.
func (b *globeBuffers) reset() {
	b.fit, b.fitDests, b.exits = b.fit[:0], b.fitDests[:0], b.exits[:0]
	b.destMarks, b.arrivals, b.speeds = b.destMarks[:0], b.arrivals[:0], b.speeds[:0]
	if b.seen == nil {
		b.seen, b.cities, b.speedIn = map[string]int{}, map[string]bool{}, map[string]string{}
	}
	clear(b.seen)
	clear(b.cities)
	clear(b.speedIn)
}

// destMark is a destination country's marker on the screen.
type destMark struct {
	x, y, vis float32
	city      string
	group     string
	live      bool
}

// arrival is a request arriving at its exit this frame.
type arrival struct {
	group string
	o, fp float32
}

// speedLabel is a route's speed beside the middle of its arc.
type speedLabel struct {
	x, y, nx, ny, w, vis float32
	txt                  string
	col                  ui.Color
}

// arc is a great circle from one place to another, lifted above the
// surface and sampled to the screen: pts holds the samples (hidden ones
// are NaN, which breaks a polyline), vis how visible each is, cum the
// length along the arc so that a request travels at a steady pace.
type arc struct {
	pts    [][2]float32
	vis    []float32
	cum    []float32
	length float32
}

// build samples the arc from a to b (unit vectors) with n segments,
// lifted lift radii at its middle, reusing the arc's memory.
func (a *arc) build(g globeProj, from, to vec3, lift float64, n int) {
	a.pts = grow(a.pts, n+1)
	a.vis = grow(a.vis, n+1)
	a.cum = grow(a.cum, n+1)
	a.cum[0] = 0
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		v := slerp(from, to, t).scale(1 + lift*math.Sin(math.Pi*t))
		x, y, vis := g.project(v)
		a.pts[i], a.vis[i] = [2]float32{x, y}, vis
		if i > 0 {
			p := a.pts[i-1]
			a.cum[i] = a.cum[i-1] + float32(math.Hypot(float64(x-p[0]), float64(y-p[1])))
		}
	}
	a.length = a.cum[n]
}

// arcLift is how high an arc from a to b rises, the n-th to its city.
func arcLift(from, to vec3, n int) float64 {
	return arcLiftBase + arcLiftSpan*angleBetween(from, to)/math.Pi + arcLiftStack*float64(n)
}

// pointAt returns the point a fraction f along the arc's length and how
// visible it is there.
func (a *arc) pointAt(f float32) (x, y, vis float32) {
	n := len(a.pts) - 1
	if n < 1 {
		return 0, 0, 0
	}
	target := f * a.length
	for i := 1; i <= n; i++ {
		if a.cum[i] >= target {
			seg := a.cum[i] - a.cum[i-1]
			t := float32(0)
			if seg > 0 {
				t = (target - a.cum[i-1]) / seg
			}
			p0, p1 := a.pts[i-1], a.pts[i]
			return p0[0] + (p1[0]-p0[0])*t, p0[1] + (p1[1]-p0[1])*t, a.vis[i-1] + (a.vis[i]-a.vis[i-1])*t
		}
	}
	p := a.pts[n]
	return p[0], p[1], a.vis[n]
}

// hitPoints returns the arc's visible samples for click testing: the
// hidden ones are NaN, which no distance matches.
func (a *arc) hitPoints(dst [][2]float32) [][2]float32 {
	dst = dst[:0]
	for i, p := range a.pts {
		if a.vis[i] <= 0 {
			p = hidden
		}
		dst = append(dst, p)
	}
	return dst
}

// stroke draws the arc up to a fraction of its length, width DIPs wide,
// fading where it nears the limb: the samples are cut into runs of about
// the same visibility, each stroked at its opacity, joined end to end
// with butt caps so that the runs do not overlap.
func (a *arc) stroke(pt *ui.Painter, b *globeBuffers, upto, width float32, col ui.Color) {
	n := len(a.pts) - 1
	if n < 1 || upto <= 0 {
		return
	}
	end := upto * a.length
	level := func(vis float32) int { return int(math.Ceil(float64(vis) * fadeLevels)) }
	path := &b.path
	path.Reset().Cap(ui.ButtCap)
	cur, count := 0, 0
	flush := func() {
		if count > 1 && cur > 0 {
			pt.StrokePath(path, width, col.Alpha(float32(cur)/fadeLevels))
		}
		path.Reset().Cap(ui.ButtCap)
		count = 0
	}
	for i := 0; i <= n; i++ {
		p := a.pts[i]
		if a.cum[i] > end && i > 0 {
			// Cut the last segment at the fraction.
			seg := a.cum[i] - a.cum[i-1]
			t := float32(1)
			if seg > 0 {
				t = (end - a.cum[i-1]) / seg
			}
			q := a.pts[i-1]
			p = [2]float32{q[0] + (p[0]-q[0])*t, q[1] + (p[1]-q[1])*t}
		}
		lv := level(a.vis[i])
		if lv != cur {
			// Close the run at this point and start the next one here.
			if count > 0 {
				path.LineTo(p[0], p[1])
				count++
			}
			flush()
			cur = lv
		}
		if lv == 0 {
			if a.cum[i] > end {
				break
			}
			continue
		}
		if count == 0 {
			path.MoveTo(p[0], p[1])
		} else {
			path.LineTo(p[0], p[1])
		}
		count++
		if a.cum[i] > end {
			break
		}
	}
	flush()
}

// Dashes of a destination line, in DIPs.
const dashOn, dashOff = 3.0, 3.0

// dashed draws the arc as a thin dashed line, hidden parts left out. Each
// dash is a segment of its own: a translucent segment draws on the GPU
// as a rounded rectangle, where a dashed path would be rasterized on the
// CPU as one mask the size of the arc, in every frame that turns the
// globe (see globe_ground.go).
func (a *arc) dashed(pt *ui.Painter, b *globeBuffers, width float32, col ui.Color) {
	n := len(a.pts) - 1
	if n < 1 {
		return
	}
	seg := b.line[:0]
	defer func() { b.line = seg[:0] }()
	// at interpolates the point at s along the arc within segment i.
	at := func(i int, s float32) [2]float32 {
		p0, p1 := a.pts[i-1], a.pts[i]
		l := a.cum[i] - a.cum[i-1]
		t := float32(0)
		if l > 0 {
			t = (s - a.cum[i-1]) / l
		}
		return [2]float32{p0[0] + (p1[0]-p0[0])*t, p0[1] + (p1[1]-p0[1])*t}
	}
	i := 1
	for s := float32(0); s < a.length; s += dashOn + dashOff {
		e := min(s+dashOn, a.length)
		for i < n && a.cum[i] < s {
			i++
		}
		j := i
		for j < n && a.cum[j] < e {
			j++
		}
		// Both ends of the dash on the visible side; a dash across a
		// sample that is hidden is left out with it.
		if a.vis[i-1] <= 0 || a.vis[i] <= 0 || a.vis[j-1] <= 0 || a.vis[j] <= 0 {
			continue
		}
		seg = append(seg[:0], at(i, s), at(j, e))
		pt.StrokePolyline(seg, width, col)
	}
}

// graticule draws meridians and parallels every 30° over the front of the
// globe, width wide in the painter's units. Each circle is stroked on its
// own: on the GPU an opaque polyline draws as segments, and a mask, where
// one is needed, then covers a circle rather than the whole disc.
func graticule(pt *ui.Painter, b *globeBuffers, g globeProj, col ui.Color, width float32) {
	b.line = b.line[:0]
	const step = 5.0
	add := func(lat, lon float64) {
		x, y, vis := g.project(unitVec(lat, lon))
		if vis <= 0 {
			b.line = append(b.line, hidden)
			return
		}
		b.line = append(b.line, [2]float32{x, y})
	}
	flush := func() {
		if len(b.line) > 1 {
			pt.StrokePolyline(b.line, width, col)
		}
		b.line = b.line[:0]
	}
	for lon := -180.0; lon < 180; lon += 30 {
		for lat := -90.0; lat <= 90; lat += step {
			add(lat, lon)
		}
		flush()
	}
	for lat := -60.0; lat <= 60; lat += 30 {
		for lon := -180.0; lon <= 180; lon += step {
			add(lat, lon)
		}
		flush()
	}
}

// placeLabel draws a label beside the mark at (x, y): to its right, else
// to its left, below or above, wherever it stays inside r and clear of
// the labels and marks placed before it this frame. A label that fits
// nowhere is drawn to the right when must is set, and skipped otherwise.
// It returns whether the label was drawn.
func placeLabel(pt *ui.Painter, b *globeBuffers, r ui.Rect, x, y, gap float32, s string, col, back ui.Color, size float32, bold, must bool) bool {
	span := labelSpan(s, col, back, size, bold)
	w, h := pt.MeasureText(0, span)
	cands := [4]ui.Rect{
		{X: x + gap, Y: y - h/2, W: w, H: h},
		{X: x - gap - w, Y: y - h/2, W: w, H: h},
		{X: x - w/2, Y: y + gap, W: w, H: h},
		{X: x - w/2, Y: y - gap - h, W: w, H: h},
	}
	if tryLabel(pt, b, r, span, cands[:]) {
		return true
	}
	if !must {
		return false
	}
	c := cands[0]
	if c.X+c.W > r.X+r.W-2 {
		c = cands[1]
	}
	b.labels = append(b.labels, c)
	pt.RichText(c.X, c.Y, 0, span)
	return true
}

// labelBeside draws a label centred along the normal (nx, ny) from the
// point (x, y), at least gap away, on whichever side is clear of the
// labels and marks placed before, nearer first; it returns whether it
// was drawn.
func labelBeside(pt *ui.Painter, b *globeBuffers, r ui.Rect, x, y, nx, ny, gap float32, s string, col, back ui.Color, size float32, bold bool) bool {
	span := labelSpan(s, col, back, size, bold)
	w, h := pt.MeasureText(0, span)
	var cands []ui.Rect
	for _, d := range []float32{gap + 8, gap + 16, gap + 26} {
		for _, sign := range []float32{1, -1} {
			cx, cy := x+nx*sign*(d+w/2), y+ny*sign*(d+h/2)
			cands = append(cands, ui.Rect{X: cx - w/2, Y: cy - h/2, W: w, H: h})
		}
	}
	return tryLabel(pt, b, r, span, cands)
}

// labelSpan is a label's text over a pill of the backing colour.
func labelSpan(s string, col, back ui.Color, size float32, bold bool) ui.Span {
	span := ui.Span{Text: s, Size: size, Color: col, Background: back, Radius: 4, PaddingX: 3, PaddingY: 1}
	if bold {
		span.Weight = 600
	}
	return span
}

// tryLabel draws the span in the first candidate box that lies inside r
// and clear of what was placed before, and keeps the box.
func tryLabel(pt *ui.Painter, b *globeBuffers, r ui.Rect, span ui.Span, cands []ui.Rect) bool {
	inner := ui.Rect{X: r.X + 2, Y: r.Y + 2, W: r.W - 4, H: r.H - 4}
	for _, c := range cands {
		if !contains(inner, c) || overlapsAny(b.labels, c) {
			continue
		}
		b.labels = append(b.labels, c)
		pt.RichText(c.X, c.Y, 0, span)
		return true
	}
	return false
}

// reserve keeps a mark's box clear of labels.
func (b *globeBuffers) reserve(x, y, radius float32) {
	b.labels = append(b.labels, ui.Rect{X: x - radius, Y: y - radius, W: 2 * radius, H: 2 * radius})
}

func contains(outer, in ui.Rect) bool {
	return in.X >= outer.X && in.Y >= outer.Y && in.X+in.W <= outer.X+outer.W && in.Y+in.H <= outer.Y+outer.H
}

func overlaps(a, b ui.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func overlapsAny(rs []ui.Rect, r ui.Rect) bool {
	for _, o := range rs {
		if overlaps(o, r) {
			return true
		}
	}
	return false
}

// grow returns s with length n, reusing its memory.
func grow[T any](s []T, n int) []T {
	if cap(s) >= n {
		return s[:n]
	}
	return make([]T, n)
}
