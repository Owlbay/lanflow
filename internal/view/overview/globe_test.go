package overview

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

// TestGlobeCentreProjectsToBoxCentre: the point facing the viewer lands on
// the centre of the globe, fully visible, and points 90° away land on the
// limb.
func TestGlobeCentreProjectsToBoxCentre(t *testing.T) {
	g := newGlobeProj(31.23, 121.47, 240, 100, 80)
	x, y, vis := g.project(unitVec(31.23, 121.47))
	if !near(float64(x), 240, 1e-3) || !near(float64(y), 100, 1e-3) || vis != 1 {
		t.Errorf("centre projected to %v,%v vis=%v", x, y, vis)
	}
	// East of the centre is to the right, north is up.
	if ex, _, _ := g.project(unitVec(31.23, 131.47)); ex <= 240 {
		t.Errorf("a point east of the centre should be right of it, x=%v", ex)
	}
	if _, ny, _ := g.project(unitVec(41.23, 121.47)); ny >= 100 {
		t.Errorf("a point north of the centre should be above it, y=%v", ny)
	}
	// 90° of longitude away, at the equator of the view, is on the limb.
	g = newGlobeProj(0, 0, 240, 100, 80)
	if lx, _, _ := g.project(unitVec(0, 90)); !near(float64(lx), 320, 1e-3) {
		t.Errorf("a point 90° east should sit on the limb at x=320, got %v", lx)
	}
}

// TestGlobeBackHemisphereHidden: what faces away from the viewer is not
// visible, and what nears the limb fades.
func TestGlobeBackHemisphereHidden(t *testing.T) {
	g := newGlobeProj(0, 0, 0, 0, 100)
	if _, _, vis := g.project(unitVec(0, 180)); vis != 0 {
		t.Errorf("the antipode should be hidden, vis=%v", vis)
	}
	if _, _, vis := g.project(unitVec(20, 120)); vis != 0 {
		t.Errorf("a point 120° away should be hidden, vis=%v", vis)
	}
	if g.front(unitVec(0, 100)) || !g.front(unitVec(0, 80)) {
		t.Error("front should split the hemispheres at 90°")
	}
	_, _, far := g.project(unitVec(0, 85))
	_, _, mid := g.project(unitVec(0, 45))
	if far <= 0 || far >= mid || mid > 1 {
		t.Errorf("visibility should fade toward the limb: 85°=%v 45°=%v", far, mid)
	}
	// A point lifted above the surface just behind the limb still shows,
	// arching over it, while one lifted well behind does not.
	if _, _, vis := g.project(unitVec(0, 95).scale(1.2)); vis <= 0 {
		t.Errorf("a lifted point over the limb should show, vis=%v", vis)
	}
	if _, _, vis := g.project(unitVec(0, 150).scale(1.2)); vis != 0 {
		t.Errorf("a lifted point far behind should be hidden, vis=%v", vis)
	}
}

// TestGreatCircleMidpoint: slerp halves the angle along the great circle,
// the shorter way round, and unit vectors round-trip to lat/lon.
func TestGreatCircleMidpoint(t *testing.T) {
	m := slerp(unitVec(0, 0), unitVec(0, 90), 0.5)
	if lat, lon := m.latLon(); !near(lat, 0, 1e-6) || !near(lon, 45, 1e-6) {
		t.Errorf("midpoint of (0,0)-(0,90) = %v,%v", lat, lon)
	}
	if !near(m.norm(), 1, 1e-9) {
		t.Errorf("slerp should stay on the sphere, |m|=%v", m.norm())
	}
	// Across the antimeridian the midpoint is at ±180, not at 0.
	m = slerp(unitVec(35, 170), unitVec(35, -170), 0.5)
	if _, lon := m.latLon(); !near(math.Abs(lon), 180, 1e-6) {
		t.Errorf("midpoint across the antimeridian at lon %v", lon)
	}
	// A great circle between two points on one latitude bows poleward.
	m = slerp(unitVec(35, 170), unitVec(35, -170), 0.5)
	if lat, _ := m.latLon(); lat <= 35 {
		t.Errorf("midpoint should bow north, lat=%v", lat)
	}
	// Ends and quarter points.
	a, b := unitVec(10, 20), unitVec(-30, 140)
	if q := slerp(a, b, 0.25); !near(angleBetween(a, q), angleBetween(a, b)/4, 1e-9) {
		t.Errorf("quarter point angle %v, want %v", angleBetween(a, q), angleBetween(a, b)/4)
	}
	if lat, lon := unitVec(-33.87, 151.21).latLon(); !near(lat, -33.87, 1e-9) || !near(lon, 151.21, 1e-9) {
		t.Errorf("round trip %v,%v", lat, lon)
	}
	// Antipodes still produce a path of the right length.
	if m := slerp(unitVec(0, 0), unitVec(0, 180), 0.5); !near(angleBetween(unitVec(0, 0), m), math.Pi/2, 1e-6) {
		t.Errorf("antipodal midpoint at %v rad from the start", angleBetween(unitVec(0, 0), m))
	}
}

// TestArcBuildAndPoint: an arc sampled to the screen is longer than its
// chord, lifted toward the viewer, and pointAt walks it by length.
func TestArcBuildAndPoint(t *testing.T) {
	// Seen from 30°N the arc along the equator lies below the centre and
	// its lift, toward the viewer, leans it outward: further down.
	g := newGlobeProj(30, 45, 0, 0, 100)
	from, to := unitVec(0, 0), unitVec(0, 90)
	var a arc
	a.build(g, from, to, arcLift(from, to, 0), arcSamples)
	x0, y0, _ := a.pointAt(0)
	x1, y1, _ := a.pointAt(1)
	fx, fy, _ := g.project(from)
	tx, ty, _ := g.project(to)
	if !near(float64(x0), float64(fx), 1e-3) || !near(float64(y0), float64(fy), 1e-3) {
		t.Errorf("start %v,%v want %v,%v", x0, y0, fx, fy)
	}
	if !near(float64(x1), float64(tx), 1e-3) || !near(float64(y1), float64(ty), 1e-3) {
		t.Errorf("end %v,%v want %v,%v", x1, y1, tx, ty)
	}
	if chord := math.Hypot(float64(tx-fx), float64(ty-fy)); float64(a.length) <= chord {
		t.Errorf("arc length %v should exceed its chord %v", a.length, chord)
	}
	_, sy, _ := g.project(slerp(from, to, 0.5))
	if xm, ym, vis := a.pointAt(0.5); !near(float64(xm), 0, 1) || ym <= sy || vis <= 0 {
		t.Errorf("midpoint %v,%v vis=%v should be outside the surface point at y=%v", xm, ym, vis, sy)
	}
	// The lifted midpoint projects beyond the surface point under it.
	mid := slerp(from, to, 0.5).scale(1 + arcLift(from, to, 0))
	if _, _, z := g.rotate(mid); z <= 1 {
		t.Errorf("lifted midpoint should be above the surface, z=%v", z)
	}
	// An arc to the far side is hidden in the middle.
	a.build(g, unitVec(0, 0), unitVec(0, -150), arcLift(unitVec(0, 0), unitVec(0, -150), 0), arcSamples)
	hiddenPts := 0
	for _, p := range a.hitPoints(nil) {
		if math.IsNaN(float64(p[0])) {
			hiddenPts++
		}
	}
	if hiddenPts == 0 || hiddenPts == len(a.pts) {
		t.Errorf("an arc round the back should be partly hidden, %d of %d", hiddenPts, len(a.pts))
	}
}

// TestFitGlobeFramesPlaces: the framing centres between you and the exits
// and keeps all of them in front, inside the box.
func TestFitGlobeFramesPlaces(t *testing.T) {
	you := model.Place{City: "上海", Lat: 31.23, Lon: 121.47}
	exits := []model.Place{{City: "东京", Lat: 35.68, Lon: 139.69}, {City: "洛杉矶", Lat: 34.05, Lon: -118.24}}
	f := fitGlobe(you, exits, nil, 480, 150)
	if !(f.lon > 121.47 || f.lon < -118.24) {
		t.Errorf("centre lon %v should lie between Shanghai and Los Angeles over the Pacific", f.lon)
	}
	g := newGlobeProj(f.lat, f.lon, 240, 75, float32(f.R))
	for _, pl := range append(exits, you) {
		x, y, vis := g.project(placeVec(pl))
		if vis <= 0 || x < 0 || x > 480 || y < 0 || y > 150 {
			t.Errorf("%s projected to %v,%v vis=%v", pl.City, x, y, vis)
		}
	}
	xs, _, _ := g.project(placeVec(you))
	xl, _, _ := g.project(placeVec(exits[1]))
	if xl <= xs {
		t.Errorf("Los Angeles (x=%v) should be right of Shanghai (x=%v)", xl, xs)
	}
	// The whole sphere shows whatever the places: the radius is half the
	// smaller side less the margin, and the limb stays inside the box.
	if f.R != 75-globeMargin {
		t.Errorf("radius %v should show the whole sphere in a 480×150 box", f.R)
	}
	close := fitGlobe(you, []model.Place{{Lat: 22.3, Lon: 114.17}}, nil, 480, 150)
	if close.R != f.R {
		t.Errorf("a nearby exit should not zoom: %v vs %v", close.R, f.R)
	}
	if tall := fitGlobe(you, exits, nil, 300, 500); tall.R != 150-globeMargin {
		t.Errorf("tall box radius %v", tall.R)
	}
	// Destinations lean the centre their way but cannot push the exits
	// out of the front.
	far := fitGlobe(you, exits[:1], []model.Place{{Lat: 52.37, Lon: 4.9}}, 480, 150)
	g = newGlobeProj(far.lat, far.lon, 0, 0, 1)
	if !g.front(placeVec(you)) || !g.front(placeVec(exits[0])) {
		t.Errorf("you and the exit should face the viewer with a far destination, centre %v,%v", far.lat, far.lon)
	}
}

// TestZoom: the framing zooms in on close exits (capped at maxUserZoom),
// the whole sphere shows with none or far ones, the wheel zooms between
// 1× and maxUserZoom, a drag turns less when zoomed in, and recentre
// goes back to the framing.
func TestZoom(t *testing.T) {
	you := model.Place{City: "上海", Lat: 31.23, Lon: 121.47}
	tokyo := model.Place{Lat: 35.68, Lon: 139.69}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if alone := fitGlobe(you, nil, nil, 600, 500); alone.zoom != 1 {
		t.Errorf("with no exit the whole sphere shows, zoom %v", alone.zoom)
	}
	a := fitGlobe(you, []model.Place{tokyo}, nil, 600, 500)
	if a.zoom <= 2 || a.zoom > maxUserZoom {
		t.Fatalf("close exits should frame tightly, zoom %v", a.zoom)
	}
	// The framed points, with their label room, fill about fitFill of
	// the map at that zoom, and their box is centred in it.
	g := newGlobeProj(a.lat, a.lon, 300, 250, float32(a.R*a.zoom))
	xs, ys, _ := g.project(placeVec(you))
	xt, yt, _ := g.project(placeVec(tokyo))
	if span := math.Abs(float64(xt-xs)) + fitLabelW + fitLabelPad; span > fitFill*600+1 || span < fitFill*600*0.5 {
		t.Errorf("framed span %v px should be about %v", span, fitFill*600)
	}
	cx := (math.Min(float64(xs), float64(xt)) - fitLabelPad + math.Max(float64(xs), float64(xt)) + fitLabelW) / 2
	cy := (float64(ys) + float64(yt)) / 2
	if !near(cx, 300, 2) || !near(cy, 250, 2) {
		t.Errorf("the framed box should be centred: %v,%v", cx, cy)
	}
	var v viewState
	v.fit(a, now, false)
	if !near(v.radius(), a.R*a.zoom, 1e-9) {
		t.Errorf("starting radius %v want %v", v.radius(), a.R*a.zoom)
	}
	v.zoomBy(0.01, now)
	if v.radius() != a.R {
		t.Errorf("cannot zoom out past the whole sphere, got %v", v.radius())
	}
	v.zoomBy(1.5, now)
	if !near(v.radius(), a.R*1.5, 1e-9) {
		t.Errorf("zoomed radius %v want %v", v.radius(), a.R*1.5)
	}
	v.zoomBy(100, now)
	if !near(v.radius(), a.R*maxUserZoom, 1e-9) {
		t.Errorf("zoom should cap at %v×, got %v", maxUserZoom, v.radius()/a.R)
	}
	v.drag(40, 0, now)
	turned := a.lon - 40/(a.R*maxUserZoom)*180/math.Pi
	if !near(v.cur.lon, turned, 1e-6) {
		t.Errorf("a drag when zoomed turns by the zoomed radius: %v want %v", v.cur.lon, turned)
	}
	// The user's zoom stays through a refit of the same routes.
	if f, _ := v.fit(a, now.Add(time.Second), false); f.lat != v.cur.lat || !near(v.radius(), a.R*maxUserZoom, 1e-9) {
		t.Errorf("refit should keep the user's view: %+v radius %v", f, v.radius())
	}
	v.recenter(now, true)
	if !near(v.radius(), a.R*a.zoom, 1e-9) {
		t.Errorf("recentre should go back to the framing, radius %v", v.radius())
	}
	// The whole sphere on demand, and new routes frame afresh.
	v.zoomTo(1, now)
	if v.radius() != a.R {
		t.Errorf("zoomTo(1) should show the whole sphere, radius %v", v.radius())
	}
	b := fitGlobe(you, []model.Place{{Lat: 22.3, Lon: 114.17}}, nil, 600, 500)
	v.fit(b, now.Add(2*time.Second), true)
	if !near(v.radius(), b.R*b.zoom, 1e-9) {
		t.Errorf("new routes should frame afresh: radius %v want %v", v.radius(), b.R*b.zoom)
	}
	// Far exits keep the whole sphere.
	wide := fitGlobe(you, []model.Place{{Lat: 34.05, Lon: -118.24}, {Lat: 52.37, Lon: 4.9}}, nil, 600, 500)
	if wide.zoom != 1 {
		t.Errorf("a wide framing should start at 1×, got %v", wide.zoom)
	}
}

// TestZoomAtKeepsPointer: zooming with the wheel keeps the ground under
// the pointer where it is, off the disc it zooms about the centre.
func TestZoomAtKeepsPointer(t *testing.T) {
	you := model.Place{City: "上海", Lat: 31.23, Lon: 121.47}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var v viewState
	a := fitGlobe(you, []model.Place{{Lat: 35.68, Lon: 139.69}}, nil, 600, 400)
	v.fit(a, now, false)
	v.zoomTo(1.5, now)
	px, py := float32(380), float32(150) // up and right of the centre
	g := newGlobeProj(v.cur.lat, v.cur.lon, 300, 200, float32(v.radius()))
	under, ok := g.unproject(px, py)
	if !ok {
		t.Fatal("pointer should be on the disc")
	}
	if back, _, _ := g.project(under); !near(float64(back), 380, 1e-3) {
		t.Errorf("unproject should invert project, x=%v", back)
	}
	v.zoomAt(1.6, px, py, now)
	g = newGlobeProj(v.cur.lat, v.cur.lon, 300, 200, float32(v.radius()))
	x, y, _ := g.project(under)
	if !near(float64(x), 380, 1.5) || !near(float64(y), 150, 1.5) {
		t.Errorf("the ground under the pointer moved to %v,%v", x, y)
	}
	if !near(v.radius(), a.R*1.5*1.6, 1e-9) {
		t.Errorf("radius %v", v.radius())
	}
	// Off the disc: the centre stays.
	lat, lon := v.cur.lat, v.cur.lon
	v.zoomAt(1.2, float32(300+v.radius()+20), 200, now)
	if v.cur.lat != lat || v.cur.lon != lon {
		t.Errorf("zoom off the disc turned the globe to %v,%v", v.cur.lat, v.cur.lon)
	}
}

// TestWheelZoomsInWindow scrolls over the rendered globe: it zooms in,
// and a double click brings the whole sphere back.
func TestWheelZoomsInWindow(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 960, 640)
	tt.Frame()
	if len(p.hits) == 0 {
		t.Fatalf("no arcs drawn")
	}
	mid := p.hits[0].pts[len(p.hits[0].pts)/2]
	base := p.view.radius()
	if base <= p.view.cur.R {
		t.Fatalf("close exits should start zoomed in: radius %v, sphere %v", base, p.view.cur.R)
	}
	tt.Scroll(mid[0], mid[1], 0, 120)
	tt.Frame()
	out := p.view.radius()
	if out >= base {
		t.Errorf("scrolling down should zoom out: %v -> %v", base, out)
	}
	tt.Pinch(mid[0], mid[1], 0.8)
	tt.Frame()
	if p.view.radius() >= out {
		t.Errorf("a pinch in should zoom out further: %v", p.view.radius()/base)
	}
	tt.ClickAt(mid[0], mid[1])
	tt.ClickAt(mid[0], mid[1])
	tt.Frame()
	// Let the glide finish; the framing itself may move a little, and
	// glide again, as the destination countries are recomputed meanwhile.
	for i := 0; i < 6 && (i == 0 || !p.view.start.IsZero()); i++ {
		tt.Advance(glideDur + 100*time.Millisecond)
		tt.Frame()
	}
	if !near(p.view.zoomFactor(), p.view.target.zoom, 1e-6) || p.view.target.zoom <= 1 {
		t.Errorf("a double click should reset the zoom to the framing: %v vs %v", p.view.zoomFactor(), p.view.target.zoom)
	}
}

// TestViewGlide: the first framing jumps, a new framing glides from the
// old one, a size change jumps, and reduced motion jumps.
func TestViewGlide(t *testing.T) {
	you := model.Place{City: "上海", Lat: 31.23, Lon: 121.47}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var v viewState
	a := fitGlobe(you, []model.Place{{Lat: 35.68, Lon: 139.69}}, nil, 480, 150)
	if f, moving := v.fit(a, now, false); moving || f != a {
		t.Fatalf("first fit should jump")
	}
	b := fitGlobe(you, []model.Place{{Lat: 34.05, Lon: -118.24}}, nil, 480, 150)
	v.fit(b, now.Add(time.Second), false)
	f, moving := v.fit(b, now.Add(time.Second+glideDur/2), false)
	if !moving || f.R != a.R {
		t.Errorf("midway the view should be moving at the same radius: %+v moving=%v", f, moving)
	}
	if lon := unwrap(f.lon, a.lon); lon <= a.lon || lon >= unwrap(b.lon, a.lon) {
		t.Errorf("midway lon %v should be between %v and %v", f.lon, a.lon, b.lon)
	}
	if f, moving := v.fit(b, now.Add(time.Second+glideDur), false); moving || f != b {
		t.Errorf("after the glide the view should be the target, got %+v", f)
	}
	c := fitGlobe(you, []model.Place{{Lat: 34.05, Lon: -118.24}}, nil, 600, 150)
	if f, moving := v.fit(c, now.Add(2*time.Second), false); moving || f != c {
		t.Errorf("resize should jump")
	}
	if f, moving := v.fit(a, now.Add(3*time.Second), true); moving || f.lon != a.lon {
		t.Errorf("reduced motion should jump, got %+v moving=%v", f, moving)
	}
}

// TestDragRotatesGlobe: a drag turns the globe so that the ground follows
// the pointer, the latitude clamps at the poles, the framing of the same
// places leaves the rotation alone, and a double click glides back.
func TestDragRotatesGlobe(t *testing.T) {
	you := model.Place{City: "上海", Lat: 31.23, Lon: 121.47}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var v viewState
	a := fitGlobe(you, []model.Place{{Lat: 35.68, Lon: 139.69}}, nil, 480, 150)
	v.fit(a, now, false)
	want := a.lon - 40/v.radius()*180/math.Pi
	v.drag(40, 0, now.Add(time.Second))
	if !near(v.cur.lon, want, 1e-6) {
		t.Errorf("dragging 40px right: lon %v, want %v", v.cur.lon, want)
	}
	if v.cur.lat != a.lat {
		t.Errorf("a horizontal drag changed lat to %v", v.cur.lat)
	}
	v.drag(0, 20, now.Add(time.Second))
	if v.cur.lat <= a.lat {
		t.Errorf("dragging down should raise the centre's latitude, got %v", v.cur.lat)
	}
	v.drag(0, 1e6, now.Add(time.Second))
	if v.cur.lat != maxLat {
		t.Errorf("lat should clamp at %v, got %v", maxLat, v.cur.lat)
	}
	// The same framing again keeps the user's rotation.
	turned := v.cur
	if f, _ := v.fit(a, now.Add(2*time.Second), false); f != turned {
		t.Errorf("refit of the same places moved the globe to %+v", f)
	}
	if f, _ := v.shown(now.Add(2*time.Second), false); f != turned {
		t.Errorf("shown right after input should be where the drag left it, got %+v", f)
	}
	// A double click glides back to the framing.
	v.recenter(now.Add(3*time.Second), false)
	if f, moving := v.glide(now.Add(3*time.Second + glideDur/2)); !moving || f.lat >= turned.lat || f.lat <= a.lat {
		t.Errorf("recentre midway lat %v should be between %v and %v", f.lat, a.lat, turned.lat)
	}
	if f, moving := v.glide(now.Add(3*time.Second + glideDur)); moving || f != a {
		t.Errorf("recentre should end at the framing, got %+v", f)
	}
	// A fling keeps turning, then slows to a stop.
	v.fling(-200, 0, now.Add(4*time.Second), false)
	f1, moving := v.shown(now.Add(4*time.Second+50*time.Millisecond), false)
	if !moving || f1.lon <= a.lon {
		t.Errorf("fling left should turn the globe east: %+v moving=%v", f1, moving)
	}
	f2, _ := v.shown(now.Add(4*time.Second+100*time.Millisecond), false)
	if f2.lon <= f1.lon {
		t.Errorf("fling should keep turning: %v then %v", f1.lon, f2.lon)
	}
	v.shown(now.Add(10*time.Second), false)
	if v.vLon != 0 {
		t.Errorf("inertia should have stopped, vLon=%v", v.vLon)
	}
	// Reduced motion: no inertia.
	v.fling(-200, 0, now.Add(20*time.Second), true)
	if v.vLon != 0 {
		t.Error("reduced motion should not fling")
	}
}

// TestAutoRotateIdleAndReducedMotion: left alone, the globe sways once
// the idle delay has passed, not while held, and never under reduced
// motion; input folds the sway in so that nothing jumps.
func TestAutoRotateIdleAndReducedMotion(t *testing.T) {
	you := model.Place{City: "上海", Lat: 31.23, Lon: 121.47}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var v viewState
	a := fitGlobe(you, nil, nil, 480, 150)
	v.fit(a, now, false)
	if f, moving := v.shown(now.Add(autoDelay/2), false); moving || !near(f.lon, a.lon, 1e-9) {
		t.Errorf("no sway before the idle delay: %+v moving=%v", f, moving)
	}
	at := now.Add(autoDelay + swayPeriod/4)
	f, moving := v.shown(at, false)
	if !moving || !near(f.lon, a.lon+swayAmp, 1e-6) {
		t.Errorf("a quarter period into the sway the globe should be %v° east: %+v moving=%v", swayAmp, f, moving)
	}
	if v.cur.lon != a.lon {
		t.Errorf("the sway must not move the base view, cur.lon=%v", v.cur.lon)
	}
	// Grabbing it bakes the sway in: the view does not jump.
	v.held = true
	if f, moving := v.shown(at, false); moving || !near(f.lon, a.lon, 1e-9) {
		t.Errorf("held globe should not sway on: %+v moving=%v", f, moving)
	}
	v.held = false
	v.shown(at, false)
	v.drag(0, 0, at)
	if !near(v.cur.lon, a.lon+swayAmp, 1e-6) {
		t.Errorf("a drag should start from where the sway left the globe, cur.lon=%v", v.cur.lon)
	}
	// Reduced motion: never.
	var r viewState
	r.fit(a, now, true)
	if f, moving := r.shown(now.Add(autoDelay+swayPeriod/4), true); moving || !near(f.lon, a.lon, 1e-9) {
		t.Errorf("reduced motion should disable the sway: %+v moving=%v", f, moving)
	}
}

// TestHitRoute: a point near a visible sample of an arc, or near the exit
// marker, hits the route; a hidden sample (NaN) and a far point miss.
func TestHitRoute(t *testing.T) {
	p := &Page{hitOrigin: [2]float32{10, 10}}
	pts := [][2]float32{{0, 100}, {50, 80}, {100, 70}, hidden, {150, 80}, {200, 100}}
	p.hits = []hitArc{{group: "节点选择", pts: pts, ex: 200, ey: 100}}
	if g := p.hitRoute(100-10, 70-10+3); g != "节点选择" {
		t.Errorf("point near the arc should hit, got %q", g)
	}
	if g := p.hitRoute(190, 90); g != "节点选择" {
		t.Errorf("the exit marker should hit, got %q", g)
	}
	if g := p.hitRoute(100, 150); g != "" {
		t.Errorf("far point should miss, got %q", g)
	}
	// An exit behind the globe: its marker and the end of its arc are hidden.
	p.hits[0].ex, p.hits[0].ey = hidden[0], hidden[1]
	pts[len(pts)-1] = hidden
	if g := p.hitRoute(190, 90); g != "" {
		t.Errorf("a hidden exit marker should not hit, got %q", g)
	}
}

// TestLandPointsAreUnitVectors: the land cache holds unit vectors at the
// requested spacing, rebuilt only when the spacing changes, and a finer
// spacing gives more dots.
func TestLandPointsAreUnitVectors(t *testing.T) {
	var lc landCache
	coarse := lc.points(4)
	if len(coarse) == 0 {
		t.Fatal("no land dots")
	}
	for _, v := range coarse[:50] {
		if !near(v.norm(), 1, 1e-9) {
			t.Fatalf("land dot %v is not a unit vector", v)
		}
	}
	n := len(coarse)
	if again := lc.points(4); len(again) != n || &again[0] != &coarse[0] {
		t.Error("same spacing should reuse the dots")
	}
	if fine := lc.points(2); len(fine) < 3*n {
		t.Errorf("halving the spacing should about quadruple the dots: %d vs %d", len(fine), n)
	}
	if s := landStep(70); s < 2 || s > 2.75 {
		t.Errorf("step at R=70 is %v°", s)
	}
	if s := landStep(140); s >= landStep(70) {
		t.Errorf("a bigger globe should have a finer step: %v vs %v", s, landStep(70))
	}
}

// TestDragInWindowRotatesAndDoesNotClick renders the overview and drags
// the globe: the view turns, and letting go after a drag opens nothing.
func TestDragInWindowRotatesAndDoesNotClick(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 960, 640)
	tt.Frame()
	if len(p.hits) == 0 {
		t.Fatalf("no arcs drawn")
	}
	before := p.view.cur
	var main hitArc
	for _, h := range p.hits {
		if h.group == "节点选择" {
			main = h
		}
	}
	mid := main.pts[len(main.pts)/2]
	tt.Press(mid[0], mid[1])
	tt.Move(mid[0]+30, mid[1])
	tt.Frame()
	tt.Move(mid[0]+60, mid[1]+10)
	tt.Frame()
	if p.view.cur.lon == before.lon || p.view.cur.lat == before.lat {
		t.Errorf("dragging should turn the globe: before %+v after %+v", before, p.view.cur)
	}
	if p.view.cur.lon >= before.lon {
		t.Errorf("dragging right should turn the centre west: %v -> %v", before.lon, p.view.cur.lon)
	}
	tt.Release(mid[0]+60, mid[1]+10)
	tt.Frame()
	tt.Frame()
	if len(r.nav) != 0 {
		t.Errorf("a drag must not count as a click on the arc, got %v", r.nav)
	}
	turned := p.view.cur
	// The same routes again leave the user's rotation alone.
	tt.Frame()
	if p.view.cur.lat != turned.lat {
		t.Errorf("refit moved the globe: %+v -> %+v", turned, p.view.cur)
	}
}
