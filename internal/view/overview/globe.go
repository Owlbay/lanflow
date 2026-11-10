package overview

import (
	"math"
	"slices"
	"time"

	"lanflow/internal/geo"
	"lanflow/internal/model"
)

// The 出口地图 is an orthographic globe (docs/design.md §5.1 「出口地图规则」):
// the land is a set of unit vectors computed once per dot spacing, and a
// frame only rotates and projects them. This file holds the geometry (unit
// vectors, great circles, the projection), the land cache, the framing of
// 「你」 and the exits, and the view state the user rotates.
const (
	// maxLat is how far the view's centre may tilt toward a pole.
	maxLat = 72.0
	// At 1× the whole sphere shows: its radius is half the smaller side of
	// the box less globeMargin for the atmosphere. The framing zooms in
	// on 「你」 and the exits so that their box, with room for labels
	// (fitLabelW × fitLabelH), fills fitFill of the map; the wheel, a
	// pinch and the buttons zoom between 1× and maxUserZoom, and a double
	// click or the reset button brings the framing back.
	globeMargin = 6.0
	maxUserZoom = 4.0
	buttonZoom  = 1.35
	fitFill     = 0.6
	fitLabelW   = 90.0
	fitLabelH   = 34.0
	fitLabelPad = 10.0
	// wheelZoom is how much a notch of the wheel, or a DIP of a touchpad's
	// scroll, zooms.
	wheelZoom = 0.0025
	// dotPx is the spacing of the land dots at the centre of the globe.
	dotPx = 2.8
	// Idle motion: after autoDelay without input the globe sways swayAmp
	// degrees to each side of where it is, once every swayPeriod, so that
	// the flows stay in view (docs/exit-map.md §4).
	autoDelay  = 4 * time.Second
	swayAmp    = 12.0
	swayPeriod = 36 * time.Second
	// swayStep is the step of the sway, in degrees: under a pixel at the
	// globe's usual size.
	swayStep = 0.25
	// Inertia after a fling: the angular speed halves about every
	// inertiaTau and stops under inertiaStop degrees a second.
	inertiaTau  = 450 * time.Millisecond
	inertiaStop = 0.6
	inertiaMax  = 240.0
	// dragSlop is how far a press may move and still count as a click.
	dragSlop = 4.0
)

// vec3 is a point in the globe's frame: x toward (0°N, 0°E), y toward
// (0°N, 90°E), z toward the north pole; a surface point is a unit vector.
type vec3 struct{ x, y, z float64 }

func unitVec(lat, lon float64) vec3 {
	la, lo := lat*math.Pi/180, lon*math.Pi/180
	c := math.Cos(la)
	return vec3{c * math.Cos(lo), c * math.Sin(lo), math.Sin(la)}
}

func placeVec(p model.Place) vec3 { return unitVec(p.Lat, p.Lon) }

func (v vec3) latLon() (lat, lon float64) {
	return math.Atan2(v.z, math.Hypot(v.x, v.y)) * 180 / math.Pi, math.Atan2(v.y, v.x) * 180 / math.Pi
}

func (v vec3) add(o vec3) vec3      { return vec3{v.x + o.x, v.y + o.y, v.z + o.z} }
func (v vec3) scale(k float64) vec3 { return vec3{v.x * k, v.y * k, v.z * k} }
func (v vec3) dot(o vec3) float64   { return v.x*o.x + v.y*o.y + v.z*o.z }
func (v vec3) norm() float64        { return math.Sqrt(v.dot(v)) }
func (v vec3) cross(o vec3) vec3 {
	return vec3{v.y*o.z - v.z*o.y, v.z*o.x - v.x*o.z, v.x*o.y - v.y*o.x}
}
func angleBetween(a, b vec3) float64  { return math.Acos(clamp(a.dot(b), -1, 1)) }
func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func (v vec3) unit() vec3 {
	n := v.norm()
	if n == 0 {
		return vec3{1, 0, 0}
	}
	return v.scale(1 / n)
}

// slerp returns the point a fraction t along the great circle from a to b
// (unit vectors), the shorter way round.
func slerp(a, b vec3, t float64) vec3 {
	ang := angleBetween(a, b)
	if ang < 1e-9 {
		return a
	}
	s := math.Sin(ang)
	if s < 1e-6 {
		// Antipodes: every great circle between them is as short; go
		// over a pole-ish perpendicular.
		axis := a.cross(vec3{0, 0, 1})
		if axis.norm() < 1e-6 {
			axis = a.cross(vec3{0, 1, 0})
		}
		m := axis.unit()
		return slerp(a, m, 2*t).unit()
	}
	return a.scale(math.Sin((1-t)*ang) / s).add(b.scale(math.Sin(t*ang) / s))
}

// globeProj is the orthographic projection of a globe of radius R DIPs
// centred on (cx, cy), the point (lat0, lon0) facing the viewer.
type globeProj struct {
	cx, cy, R  float32
	lat0, lon0 float64
	sl, cl     float64 // sin, cos of lat0
	sn, cn     float64 // sin, cos of lon0
}

func newGlobeProj(lat0, lon0 float64, cx, cy, R float32) globeProj {
	la, lo := lat0*math.Pi/180, lon0*math.Pi/180
	return globeProj{cx: cx, cy: cy, R: R, lat0: lat0, lon0: lon0, sl: math.Sin(la), cl: math.Cos(la), sn: math.Sin(lo), cn: math.Cos(lo)}
}

// rotate brings v into the viewer's frame: x right, y up, z toward the
// viewer, all in radii.
func (g globeProj) rotate(v vec3) (x, y, z float64) {
	a := g.cn*v.x + g.sn*v.y
	return -g.sn*v.x + g.cn*v.y, g.cl*v.z - g.sl*a, g.cl*a + g.sl*v.z
}

// project returns where v (a unit vector, or one lifted above the surface)
// lands on the screen and how visible it is: 0 behind the globe, rising
// to 1 as it turns to face the viewer, so that what nears the limb fades.
func (g globeProj) project(v vec3) (sx, sy, vis float32) {
	x, y, z := g.rotate(v)
	sx, sy = g.cx+float32(x)*g.R, g.cy-float32(y)*g.R
	rho2 := x*x + y*y
	if rho2 >= 1 {
		// Off the disc: a lifted point arching over the limb; it fades
		// as it goes behind.
		return sx, sy, float32(clamp((z+0.25)/0.25, 0, 1))
	}
	if z < math.Sqrt(1-rho2)-1e-6 {
		return sx, sy, 0 // behind the front surface
	}
	return sx, sy, float32(clamp(z/0.3, 0, 1))
}

// front reports whether a surface point faces the viewer at all.
func (g globeProj) front(v vec3) bool {
	_, _, z := g.rotate(v)
	return z > 0
}

// unproject returns the surface point under the screen point (sx, sy),
// and false when the point is off the disc.
func (g globeProj) unproject(sx, sy float32) (vec3, bool) {
	x, y := float64(sx-g.cx)/float64(g.R), -float64(sy-g.cy)/float64(g.R)
	rho2 := x*x + y*y
	if rho2 > 1 {
		return vec3{}, false
	}
	z := math.Sqrt(1 - rho2)
	a := g.cl*z - g.sl*y
	return vec3{g.cn*a - g.sn*x, g.sn*a + g.cn*x, g.sl*z + g.cl*y}, true
}

// ---- land ----

// landCache keeps the land dots as unit vectors for one dot spacing; the
// spacing follows the zoom so that the dots stay about dotPx apart.
type landCache struct {
	step float64
	vecs []vec3
}

// landStep is the angular spacing of the land dots for a globe of radius
// R, in whole quarter degrees so that zoom glides rarely rebuild them.
func landStep(R float32) float64 {
	if R <= 0 {
		return 4
	}
	px := dotPx + clamp((float64(R)-70)/90, 0, 1)*0.7
	step := px / (float64(R) * math.Pi / 180)
	return clamp(math.Round(step*4)/4, 1, 6)
}

// points returns the land dots for the spacing, rebuilt when it changes:
// rows of cells step degrees tall, each row's cells about as wide on the
// ground (fewer toward the poles), a dot at each cell centre on land.
func (lc *landCache) points(step float64) []vec3 {
	if lc.step == step && lc.vecs != nil {
		return lc.vecs
	}
	lc.step = step
	lc.vecs = lc.vecs[:0]
	for lat := -90 + step/2; lat < 90; lat += step {
		n := int(math.Round(360 * math.Cos(lat*math.Pi/180) / step))
		if n < 1 {
			n = 1
		}
		for i := 0; i < n; i++ {
			lon := -180 + (float64(i)+0.5)*360/float64(n)
			if geo.OnLand(lat, lon) {
				lc.vecs = append(lc.vecs, unitVec(lat, lon))
			}
		}
	}
	return lc.vecs
}

// ---- framing ----

// globeFit is a framing of the globe: the point facing the viewer, the
// radius of the whole sphere and the zoom to start at, for a box of W×H.
type globeFit struct {
	lat, lon, R float64
	W, H        float32
	zoom        float64
}

// fitKey is a framing rounded to what shows: a tenth of a degree, a
// whole DIP.
type fitKey struct{ lat, lon, R, W, H int64 }

func (f globeFit) key() fitKey {
	r := func(x float64, by float64) int64 { return int64(math.Round(x * by)) }
	return fitKey{r(f.lat, 10), r(f.lon, 10), r(f.R, 1), r(float64(f.W), 1), r(float64(f.H), 1)}
}

// fitMemo keeps the last fitGlobe, which only changes with its inputs:
// the places and the box, not the frame.
type fitMemo struct {
	you          model.Place
	exits, dests []model.Place
	W, H         float32
	out          globeFit
	ok           bool
}

// fit is fitGlobe, computed again only when an input changed.
func (m *fitMemo) fit(you model.Place, exits, dests []model.Place, W, H float32) globeFit {
	if m.ok && m.you == you && m.W == W && m.H == H && slices.Equal(m.exits, exits) && slices.Equal(m.dests, dests) {
		return m.out
	}
	m.out = fitGlobe(you, exits, dests, W, H)
	m.you, m.W, m.H, m.ok = you, W, H, true
	m.exits = append(m.exits[:0], exits...)
	m.dests = append(m.dests[:0], dests...)
	return m.out
}

// globeRadius is the radius that shows the whole sphere in a box of W×H.
func globeRadius(W, H float32) float64 {
	return math.Max(float64(min(W, H))/2-globeMargin, 10)
}

// fitGlobe frames 「你」 and the exits: the view turns to their spherical
// mean (you counting double, the destination countries half, so that the
// globe leans their way) and zooms in so that their box on the screen,
// with room for their labels, fills fitFill of the map, up to
// maxUserZoom: close exits, as 上海 → 东京/香港, show large while their
// requests flow. With no exit, or one behind the globe, the whole sphere
// shows. Destinations never drive the zoom: one may lie behind the globe,
// its dashed line still points the way, and a drag brings it round.
func fitGlobe(you model.Place, exits, dests []model.Place, W, H float32) globeFit {
	sum := placeVec(you).scale(2)
	for _, p := range exits {
		sum = sum.add(placeVec(p))
	}
	lat, lon := sum.unit().latLon()
	lat = clamp(lat, -maxLat, maxLat)
	R := globeRadius(W, H)
	f := globeFit{lat: lat, lon: lon, R: R, W: W, H: H, zoom: 1}
	if len(exits) == 0 {
		return f.leaning(dests)
	}
	// Frame you and the exits: zoom so that their box on the screen,
	// with room for their labels, fills fitFill of the map, and turn so
	// that the box's centre is the map's. The zoom changes where things
	// land, so go round a few times.
	pts := append([]model.Place{you}, exits...)
	z := 1.0
	for i := 0; i < 4; i++ {
		g := newGlobeProj(lat, lon, 0, 0, float32(R*z))
		x0, x1, y0, y1 := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		for _, p := range pts {
			x, y, vis := g.project(placeVec(p))
			if vis <= 0 {
				return f.leaning(dests)
			}
			x0, x1 = math.Min(x0, float64(x)), math.Max(x1, float64(x))
			y0, y1 = math.Min(y0, float64(y)), math.Max(y1, float64(y))
		}
		x0, x1 = x0-fitLabelPad, x1+fitLabelW
		y0, y1 = y0-fitLabelH/2, y1+fitLabelH/2
		next := math.Min(fitFill*float64(W)/(x1-x0), fitFill*float64(H)/(y1-y0)) * z
		next = clamp(next, 1, maxUserZoom)
		if v, ok := g.unproject(float32((x0+x1)/2), float32((y0+y1)/2)); ok {
			lat, lon = v.latLon()
			lat = clamp(lat, -maxLat, maxLat)
		}
		z = next
	}
	if z <= 1 {
		return f.leaning(dests)
	}
	f.lat, f.lon, f.zoom = lat, lon, z
	return f
}

// leaning turns a whole-sphere framing a little toward the destination
// countries (half the weight of an exit), so that the globe leans their
// way.
func (f globeFit) leaning(dests []model.Place) globeFit {
	if len(dests) == 0 {
		return f
	}
	sum := unitVec(f.lat, f.lon).scale(3)
	for _, p := range dests {
		sum = sum.add(placeVec(p).scale(0.5))
	}
	lat, lon := sum.unit().latLon()
	f.lat, f.lon = clamp(lat, -maxLat, maxLat), lon
	return f
}

func lerpFit(a, b globeFit, k float64) globeFit {
	l := func(x, y float64) float64 { return x + (y-x)*k }
	return globeFit{lat: l(a.lat, b.lat), lon: normLon(l(a.lon, unwrap(b.lon, a.lon))), R: l(a.R, b.R), W: b.W, H: b.H, zoom: b.zoom}
}

// ---- the view the user rotates ----

// viewState is where the globe is turned to. The framing glides when the
// set of places changes (fit), the user drags it round (drag, fling),
// zooms with the wheel (zoomBy) and double-clicks to come back
// (recenter), and left alone it sways slowly (shown), never under reduced
// motion.
type viewState struct {
	target   globeFit // the last framing asked for
	cur      globeFit // where the globe is, sway aside
	from, to globeFit // the glide in progress
	start    time.Time
	has      bool

	zoom      float64   // the user's zoom, 1 (the whole sphere) to maxUserZoom
	held      bool      // the pointer presses the globe
	dragDist  float32   // how far the press moved
	lastInput time.Time // the last drag, fling or recentre
	swayOff   float64   // the sway's offset of the last frame, degrees
	paused    bool      // the window is inactive: no sway (set by the page before shown)
	swayHeld  bool      // the sway was paused (inactive window or reduced motion) in the last frame
	vLat      float64   // inertia, degrees a second
	vLon      float64
	tick      time.Time // the last inertia step

	buf globeBuffers // per-frame scratch, reused
}

// fit takes the framing of this frame's places and returns the view to
// draw and whether it is gliding. The first framing and size changes
// jump; a new framing glides from where the globe is, interrupting a
// drag's inertia; the same framing leaves a user's rotation alone.
func (v *viewState) fit(target globeFit, now time.Time, reduce bool) (globeFit, bool) {
	if !v.has || v.cur.W != target.W || v.cur.H != target.H {
		if !v.has {
			v.zoom = target.zoom
		}
		v.target, v.cur, v.to, v.has = target, target, target, true
		v.start, v.swayOff, v.vLat, v.vLon = time.Time{}, 0, 0, 0
		v.lastInput = now
		return v.cur, false
	}
	if target.key() != v.target.key() {
		// New routes: frame them afresh, zoom included.
		v.target = target
		v.bake()
		v.vLat, v.vLon = 0, 0
		v.lastInput = now
		if reduce {
			v.cur, v.zoom, v.start = target, target.zoom, time.Time{}
			return v.cur, false
		}
		v.from, v.to, v.start = v.cur, target, now
		v.from.zoom = v.zoomFactor()
	}
	return v.glide(now)
}

// glide advances the glide in progress, the zoom along with the turn.
func (v *viewState) glide(now time.Time) (globeFit, bool) {
	if v.start.IsZero() {
		return v.cur, false
	}
	el := now.Sub(v.start)
	if el >= glideDur {
		v.cur, v.zoom, v.start = v.to, v.to.zoom, time.Time{}
		return v.cur, false
	}
	k := float64(easeInOut(float32(el) / float32(glideDur)))
	v.cur = lerpFit(v.from, v.to, k)
	v.zoom = v.from.zoom + (v.to.zoom-v.from.zoom)*k
	return v.cur, true
}

// bake folds the sway's offset into the view so that nothing jumps when
// the sway stops.
func (v *viewState) bake() {
	v.cur.lon = normLon(v.cur.lon + v.swayOff)
	v.swayOff = 0
}

// radius is the radius drawn: the whole sphere's, zoomed by the user.
func (v *viewState) radius() float64 {
	return v.cur.R * v.zoomFactor()
}

func (v *viewState) zoomFactor() float64 {
	if v.zoom < 1 {
		return 1
	}
	return v.zoom
}

// zoomBy zooms by a factor about the centre, within 1 and maxUserZoom;
// the user's zoom stays until a reset or new routes.
func (v *viewState) zoomBy(factor float64, now time.Time) {
	if !v.has || factor <= 0 || math.IsNaN(factor) || math.IsInf(factor, 0) {
		return
	}
	v.zoomTo(v.zoomFactor()*factor, now)
}

// zoomTo sets the zoom, within 1 and maxUserZoom, ending any glide.
func (v *viewState) zoomTo(zoom float64, now time.Time) {
	if !v.has || math.IsNaN(zoom) {
		return
	}
	v.bake()
	v.zoom = clamp(zoom, 1, maxUserZoom)
	v.start, v.vLat, v.vLon = time.Time{}, 0, 0
	v.lastInput = now
}

// zoomAt zooms by a factor about the point (px, py) of the box, so that
// the ground under the pointer stays under it: the globe turns as it
// grows. Off the disc it zooms about the centre.
func (v *viewState) zoomAt(factor float64, px, py float32, now time.Time) {
	if !v.has {
		return
	}
	v.bake()
	cx, cy := v.cur.W/2, v.cur.H/2
	before := newGlobeProj(v.cur.lat, v.cur.lon, cx, cy, float32(v.radius()))
	under, on := before.unproject(px, py)
	v.zoomBy(factor, now)
	if !on {
		return
	}
	// Turn the globe so that the point comes back under the pointer:
	// moving the centre by the two points' difference is close, and a
	// few rounds make it exact.
	lat0, lon0 := under.latLon()
	for i := 0; i < 4; i++ {
		after := newGlobeProj(v.cur.lat, v.cur.lon, cx, cy, float32(v.radius()))
		there, ok := after.unproject(px, py)
		if !ok {
			break
		}
		lat1, lon1 := there.latLon()
		v.cur.lat = clamp(v.cur.lat+lat0-lat1, -maxLat, maxLat)
		v.cur.lon = normLon(v.cur.lon + unwrap(lon0, lon1) - lon1)
	}
	v.start, v.vLat, v.vLon = time.Time{}, 0, 0
}

// drag turns the globe under a pointer that moved dx, dy DIPs: the ground
// under the pointer follows it.
func (v *viewState) drag(dx, dy float32, now time.Time) {
	if !v.has {
		return
	}
	v.bake()
	v.start, v.vLat, v.vLon = time.Time{}, 0, 0
	v.lastInput = now
	v.dragDist += float32(math.Abs(float64(dx)) + math.Abs(float64(dy)))
	k := 180 / math.Pi / math.Max(v.radius(), 1)
	v.cur.lon = normLon(v.cur.lon - float64(dx)*k)
	v.cur.lat = clamp(v.cur.lat+float64(dy)*k, -maxLat, maxLat)
}

// fling keeps the globe turning after the pointer let go at vx, vy DIPs a
// second, slowing down; reduced motion stops it at once.
func (v *viewState) fling(vx, vy float32, now time.Time, reduce bool) {
	v.lastInput = now
	if reduce || !v.has {
		return
	}
	k := 180 / math.Pi / math.Max(v.radius(), 1)
	v.vLon = clamp(-float64(vx)*k, -inertiaMax, inertiaMax)
	v.vLat = clamp(float64(vy)*k, -inertiaMax, inertiaMax)
	v.tick = now
}

// recenter glides back to the framing of the places, the whole sphere in
// view again.
func (v *viewState) recenter(now time.Time, reduce bool) {
	if !v.has {
		return
	}
	v.bake()
	v.vLat, v.vLon = 0, 0
	v.lastInput = now
	if reduce {
		v.cur, v.zoom, v.start = v.target, v.target.zoom, time.Time{}
		return
	}
	v.from, v.to, v.start = v.cur, v.target, now
	v.from.zoom = v.zoomFactor()
}

// shown returns the view to draw this frame, after the frame's fit: the
// inertia of a fling, then the idle sway once nothing has happened for
// autoDelay, and whether the globe is moving on its own.
func (v *viewState) shown(now time.Time, reduce bool) (globeFit, bool) {
	moving := false
	if v.vLat != 0 || v.vLon != 0 {
		dt := now.Sub(v.tick).Seconds()
		v.tick = now
		if dt > 0 {
			v.cur.lat = clamp(v.cur.lat+v.vLat*dt, -maxLat, maxLat)
			v.cur.lon = normLon(v.cur.lon + v.vLon*dt)
			decay := math.Exp(-dt / inertiaTau.Seconds())
			v.vLat, v.vLon = v.vLat*decay, v.vLon*decay
			if math.Hypot(v.vLat, v.vLon) < inertiaStop {
				v.vLat, v.vLon = 0, 0
			}
		}
		v.lastInput = now
		moving = true
	}
	// The sway pauses with the window inactive: where it stopped is kept
	// (nothing jumps), and it starts again autoDelay after the window is
	// active, from there, as after an input.
	if held := reduce || v.paused; held != v.swayHeld {
		v.swayHeld = held
		v.bake()
		v.lastInput = now
	}
	v.swayOff = 0
	if !v.swayHeld && !v.held && v.start.IsZero() && !v.lastInput.IsZero() {
		if idle := now.Sub(v.lastInput) - autoDelay; idle > 0 {
			// In steps of swayStep: the ground's canvas is drawn again
			// only when the globe turned a step, not every frame of a
			// motion of two degrees a second.
			off := swayAmp * math.Sin(2*math.Pi*idle.Seconds()/swayPeriod.Seconds())
			v.swayOff = math.Round(off/swayStep) * swayStep
			moving = true
		}
	}
	out := v.cur
	out.lon = normLon(out.lon + v.swayOff)
	return out, moving
}
