package overview

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// Map geometry (docs/design.md §5.1 「出口地图规则」).
const (
	// cardHeight is the whole 出口地图 card (docs/design.md §5.1: 「出口地图卡高
	// 200，右侧路由列表宽 220」); the drawing takes what remains under the
	// card's padding and title row.
	cardHeight = 200
	listWidth  = 196
	fadeDur    = 520 * time.Millisecond
	rippleFrom = 5.0
	rippleTo   = 20.0
	arcSamples = 48
	// speedTau is the time constant over which a route's shown speed
	// follows the measured one, whatever the build rate.
	speedTau = time.Second
	// pulseDur is the period of the ring that pulses around 「你」.
	pulseDur = 2200 * time.Millisecond
	// Marker and label sizes.
	youRadius  = 6.0
	exitRadius = 5.0
	labelSize  = 12.0
	destSize   = 11.0
	// dotTrail is how far behind one another the requests of a busy route
	// travel, as a fraction of the arc.
	dotTrail = 0.16
	// shortArc is an arc too short for a speed label of its own, in DIPs.
	shortArc = 60.0
	// youPulse is how far the ring around 「你」 spreads past its marker.
	youPulse = 10.0
	// glideDur is how long an exit marker and the view take to move to a
	// new city after a node switch (docs/design.md §5.1 切换节点).
	glideDur = 500 * time.Millisecond
	// Destination countries (docs/exit-map.md §4 layer B): at most maxDests
	// per route, kept destKeep after their last connection closed so the
	// view does not jump at every short request; recomputed every destEvery.
	maxDests  = 3
	destKeep  = 8 * time.Second
	destEvery = time.Second
	hitRadius = 7.0
)

// flow is the request animation of one route: one request at a time,
// drawn from "you" to the exit, fading with a ripple, then a pause.
type flow struct {
	start time.Time // when the current request left; zero = not armed
	speed float64   // smoothed bytes/s
	eased time.Time // when speed was last eased
	armed bool

	// The displayed exit glides from `from` to `to` after a node switch.
	at, from, to model.Place
	hasAt        bool
	moveStart    time.Time

	arc arc // the route's arc of the last frame, its memory reused
}

// place returns where to draw the exit now and whether it is still
// moving. A new target starts a glide from the current position; with
// reduced motion the marker jumps.
func (f *flow) place(target model.Place, now time.Time, reduce bool) (model.Place, bool) {
	if !f.hasAt || reduce {
		f.at, f.to, f.hasAt = target, target, true
		f.moveStart = time.Time{}
		return f.at, false
	}
	if !samePlace(target, f.to) {
		f.from, f.to, f.moveStart = f.at, target, now
	}
	el := now.Sub(f.moveStart)
	if f.moveStart.IsZero() || el >= glideDur {
		f.at = f.to
		return f.at, false
	}
	f.at = lerpPlace(f.from, f.to, float64(easeInOut(float32(el)/float32(glideDur))))
	return f.at, true
}

func samePlace(a, b model.Place) bool {
	return a.City == b.City && a.Country == b.Country && a.Lat == b.Lat && a.Lon == b.Lon
}

// lerpPlace interpolates between two places along the shorter way round.
func lerpPlace(a, b model.Place, k float64) model.Place {
	lon := unwrap(b.Lon, a.Lon)
	return model.Place{Country: b.Country, City: b.City, Lat: a.Lat + (b.Lat-a.Lat)*k, Lon: normLon(a.Lon + (lon-a.Lon)*k)}
}

func unwrap(lon, around float64) float64 {
	for lon-around > 180 {
		lon -= 360
	}
	for lon-around < -180 {
		lon += 360
	}
	return lon
}

func normLon(lon float64) float64 {
	return math.Mod(lon+540, 360) - 180
}

// arcWidth maps a speed to a stroke width of 2–10 px, logarithmically.
func arcWidth(speed float64) float32 {
	return float32(2 + math.Min(8, math.Log10(1+speed/2e3)*3))
}

// flowGap is the pause between two requests, by speed.
func flowGap(speed float64) time.Duration {
	switch {
	case speed > 700e3:
		return 120 * time.Millisecond
	case speed > 120e3:
		return 320 * time.Millisecond
	default:
		return 700 * time.Millisecond
	}
}

// flowDots is how many requests travel an arc at once, by speed.
func flowDots(speed float64) int {
	switch {
	case speed > 700e3:
		return 3
	case speed > 120e3:
		return 2
	default:
		return 1
	}
}

// active reports whether a route carries traffic worth showing.
func active(speed float64) bool { return speed >= 512 }

// flowDur is how long a request takes along an arc of length px.
func flowDur(length float32) time.Duration {
	ms := 650 + float64(length)*2.6
	return time.Duration(math.Max(900, math.Min(1600, ms))) * time.Millisecond
}

func easeInOut(t float32) float32 {
	if t < .5 {
		return 4 * t * t * t
	}
	return 1 - float32(math.Pow(float64(-2*t+2), 3))/2
}

// ---- data ----

// ---- the card ----

func (p *Page) exitCard(c *ui.Context, st model.Status, rs []route) {
	t := c.Theme()
	run := st.Core == model.CoreRunning
	direct := st.Mode == model.ModeDirect
	you, guessed := p.youPlace()
	var dests map[string][]dest
	if run && !direct {
		dests = p.destinations(rs, st.Mode, time.Now())
	}
	p.mu.Lock()
	probing := p.probing
	p.mu.Unlock()

	// The map card takes the left two thirds of the page and all its
	// height (docs/design.md §5.1), so the globe grows with the window.
	ui.Column(c).Radius(10).Background(t.Surface).Border(1, t.Border).Clip().Grow(64).Basis(0).MinHeight(cardHeight).Padding(8, 12, 8, 12).Gap(2).Children(func() {
		cardTitle(c, i18n.T("出口地图"), func() {
			if guessed {
				p.youUnknown(c, t)
			}
			switch {
			case !run:
				ui.Text(c, i18n.T("内核未启动")).FontSize(11).TextColor(t.TextMuted).Padding(1, 6).Radius(999).Background(t.TextMuted.Alpha(0.12))
			case direct:
			default:
				b := ui.Button(c, "").Label(i18n.T("实测出口")).Tooltip(i18n.T("经各出口访问 Cloudflare trace，测出真实出口位置")).Disabled(probing)
				b.Children(func() {
					if probing {
						ui.Spinner(c).Size(12, 12)
						ui.Text(c, i18n.T("实测中…")).SingleLine()
					} else {
						ui.Text(c, i18n.T("实测出口")).SingleLine()
					}
				})
				if b.Clicked() {
					p.probe()
				}
			}
		})
		m := ui.Box(c).Grow(1).Label(i18n.T("出口地图")).Description(mapDescription(you, rs, dests) + i18n.T("。拖动可旋转地球，滚轮缩放，双击复位"))
		if !run {
			m.Opacity(0.45)
		}
		// The globe turns under a drag and keeps turning after a fling;
		// the wheel or a pinch zooms about the pointer; a double click
		// brings the framing of the exits back.
		now := time.Now()
		reduce := c.Preferences().ReduceMotion
		m.HandleInput(func(ev ui.InputEvent) bool {
			switch ev.Kind {
			case ui.InputScroll:
				if ev.Momentum {
					return true
				}
				step := float64(ev.DY)
				if !ev.Precise {
					step *= 12 // a notch of the wheel
				}
				p.view.zoomAt(math.Exp(-step*wheelZoom), ev.X, ev.Y, time.Now())
				return true
			case ui.InputZoom:
				p.view.zoomAt(float64(ev.Scale), ev.X, ev.Y, time.Now())
				return true
			}
			return false
		})
		// Zoom buttons in the corner of the map, about its centre: in,
		// out, back to the framing of the exits, and the whole sphere.
		m.Children(func() {
			ui.Row(c).Absolute().Right(6).Bottom(6).Gap(4).Children(func() {
				p.zoomButton(c, "plus", "+", i18n.T("放大"), func() { p.view.zoomBy(buttonZoom, time.Now()) })
				p.zoomButton(c, "", "−", i18n.T("缩小"), func() { p.view.zoomBy(1/buttonZoom, time.Now()) })
				p.zoomButton(c, "locate", "⌖", i18n.T("复位视角"), func() { p.view.recenter(time.Now(), reduce) })
				p.zoomButton(c, "proxies", "◯", i18n.T("地球全景"), func() { p.view.zoomTo(1, time.Now()) })
			})
		})
		dx, dy, held := m.Dragged()
		dragged := p.view.dragDist > dragSlop // this press turned the globe: no click
		p.view.held = held
		if held {
			if dx != 0 || dy != 0 {
				p.view.drag(dx, dy, now)
			}
		} else {
			p.view.dragDist = 0
		}
		if vx, vy, ok := m.DragEnded(); ok {
			p.view.fling(vx, vy, now, reduce)
		}
		if m.DoubleClicked() {
			p.view.recenter(now, reduce)
		}
		cursor := ui.CursorGrab
		if held {
			cursor = ui.CursorGrabbing
		}
		// Clicking an arc or exit opens the connections page filtered
		// to that route (docs/exit-map.md §4 layer D).
		if run && !direct && !held && len(p.hits) > 0 {
			if x, y, over := m.PointerPosition(); over {
				if g := p.hitRoute(x, y); g != "" {
					cursor = ui.CursorPointer
					m.Tooltip(i18n.Tf("查看经 %s 的连接", g))
					if !dragged && m.Clicked() {
						p.e.Navigate(env.Conns, g)
					}
				}
			}
		}
		m.Cursor(cursor)
		p.drawMap(c, m, rs, dests, you, guessed, run && !direct)
	})
}

// zoomButton is one of the small round buttons over the map: an icon of
// the kit, or a glyph where it has none.
func (p *Page) zoomButton(c *ui.Context, icon, glyph, label string, fn func()) {
	t := c.Theme()
	b := ui.Button(c.Key("zoom:"+label), "").Label(label).Tooltip(label).Size(26, 26).Padding(0).Radius(13).
		Background(t.Surface.Alpha(0.85)).Border(1, t.Border)
	b.Children(func() {
		if icon != "" && p.e.Kit != nil {
			p.e.Kit.Icon(c, icon, 14)
		} else {
			ui.Text(c, glyph).FontSize(14).TextColor(t.Text).SingleLine()
		}
	})
	if b.Clicked() {
		fn()
	}
}

func (p *Page) probe() {
	p.mu.Lock()
	p.probing = true
	p.mu.Unlock()
	p.e.Run(func() error {
		err := p.e.API.ProbeExits()
		p.mu.Lock()
		p.probing = false
		p.mu.Unlock()
		return err
	}, i18n.T("已更新出口位置"))
}

// exitDraw is an exit of this frame: its route, where its marker glides,
// and where that is on the screen.
type exitDraw struct {
	r         route
	place     model.Place
	f         *flow
	vec       vec3
	x, y, vis float32
	hasArc    bool
}

// drawMap paints the globe: its disc and limb, the land dots, a faint
// graticule, then "you", the exits with their destination countries and
// the request animation along great circles, the back of the globe hidden
// and the limb fading what nears it.
func (p *Page) drawMap(c *ui.Context, box ui.Element, rs []route, dests map[string][]dest, you *model.Place, guessed bool, live bool) {
	t := c.Theme()
	reduce := c.Preferences().ReduceMotion
	// The idle motion, the sway and the pulse around 「你」, plays only in
	// the active window and only while requests travel the map: behind
	// another app's window it would cost frames nobody watches, and over
	// a still map it would keep the window drawing 30 frames a second for
	// decoration alone (the request animation already draws them then).
	// The request animation itself goes on in an inactive window, slower.
	focused := c.WindowActive()
	ambient := focused && !reduce && live && p.anyLive(rs)
	p.view.paused = !ambient
	box.Draw(func(pt *ui.Painter, r ui.Rect) {
		p.hits = p.hits[:0]
		p.hitOrigin = [2]float32{r.X, r.Y}
		if r.W < 40 || r.H < 40 {
			return
		}
		now := pt.Now()
		animating := false
		b := &p.view.buf
		b.reset()
		// Exits glide to their new city; the view is framed around where
		// they are going, plus the destination countries.
		for _, rt := range rs {
			f := p.flows[rt.Group]
			if f == nil {
				f = &flow{}
				p.flows[rt.Group] = f
			}
			if rt.place == nil {
				f.hasAt = false
				continue
			}
			b.fit = append(b.fit, *rt.place)
			for _, d := range dests[rt.Group] {
				b.fitDests = append(b.fitDests, d.place)
			}
			pl, moving := f.place(*rt.place, now, reduce)
			animating = animating || moving
			b.exits = append(b.exits, exitDraw{r: rt, place: pl, f: f, vec: placeVec(pl)})
		}
		exits := b.exits
		center := model.Place{Lat: 30, Lon: 120}
		if you != nil {
			center = *you
		} else if len(b.fit) > 0 {
			center = b.fit[0]
		}
		_, moving := p.view.fit(b.fitMemo.fit(center, b.fit, b.fitDests, r.W, r.H), now, reduce)
		animating = animating || moving
		view, idle := p.view.shown(now, reduce)
		g := newGlobeProj(view.lat, view.lon, r.X+r.W/2, r.Y+r.H/2, float32(p.view.radius()))
		b.labels = b.labels[:0]

		b.clip = ui.Rect{X: r.X - clipMargin, Y: r.Y - clipMargin, W: r.W + 2*clipMargin, H: r.H + 2*clipMargin}
		pt.Clip(r, 0, func() {
			// The disc, darker toward the limb, with a thin atmosphere.
			disc := circle(g.cx, g.cy, g.R)
			pt.Fill(disc, t.TextMuted.Alpha(0.07), g.R)
			pt.FillRadialGradient(disc, ui.RadialGradient{Radius: g.R, Stops: []ui.GradientStop{
				{Color: t.TextMuted.Alpha(0), At: 0.72}, {Color: t.TextMuted.Alpha(0.16), At: 1},
			}}, g.R)
			pt.StrokePath(visibleCircle(&b.path, g.cx, g.cy, g.R+3, r, clipMargin), 6, t.Accent.Alpha(0.08))
			pt.StrokePath(visibleCircle(&b.path, g.cx, g.cy, g.R+0.5, r, clipMargin), 1, t.TextMuted.Alpha(0.3))
			// The graticule and the land, the front half of the dots,
			// fainter toward the limb, larger when zoomed in so far that
			// they spread apart: from the ground canvases, redrawn only
			// when the globe turned (globe_ground.go).
			step := landStep(g.R)
			dotR := float32(clamp(step*float64(g.R)*math.Pi/180/4, 1.25, 3))
			ground := over(t.Surface, t.TextMuted.Alpha(0.07))
			p.ground.draw(pt, b, r, g, p.land.points(step), step, dotR, t.TextMuted, over(ground, t.TextMuted.Alpha(gratAlpha)))

			fr := &mapFrame{pt: pt, t: t, b: b, r: r, g: g, now: now, reduce: reduce, focused: focused, ambient: ambient, live: live,
				you: you, guessed: guessed, exits: exits, dests: dests}
			p.drawRoutes(fr)
			p.drawFlows(fr)
			p.drawLabels(fr)
			animating = animating || fr.animating
		})
		switch {
		case animating && focused:
			// Requests travel and markers glide smoothly at 60 fps; the
			// display's own rate (120 on ProMotion) adds nothing visible.
			pt.AnimationFrameRate(60)
		case animating:
			pt.AnimationFrameRate(30)
		case idle || (ambient && you != nil):
			// The sway and the pulse around 「你」 look as smooth at 30 fps.
			pt.AnimationFrameRate(30)
		case ambient:
			// Nothing moves yet: wake up for the sway.
			pt.After(autoDelay)
		}
	})
}

// mapFrame is what the parts of a frame of the map share: drawRoutes
// places everything and draws the routes to the destination countries,
// drawFlows the routes and their request animation, drawLabels the
// markers and labels over them.
type mapFrame struct {
	pt                             *ui.Painter
	t                              *ui.Theme
	b                              *globeBuffers
	r                              ui.Rect
	g                              globeProj
	now                            time.Time
	reduce, focused, ambient, live bool
	you                            *model.Place
	guessed                        bool
	exits                          []exitDraw
	dests                          map[string][]dest
	yv                             vec3    // 「你」 on the sphere
	yx, yy, yvis                   float32 // and on the screen
	animating                      bool    // set by drawFlows
}

// drawRoutes projects 「你」 and the exits, builds the arcs between them
// and draws the dashed lines to the destination countries with their
// markers.
func (p *Page) drawRoutes(fr *mapFrame) {
	pt, t, b, g := fr.pt, fr.t, fr.b, fr.g
	you, exits, dests, live := fr.you, fr.exits, fr.dests, fr.live
	b.destMarks = b.destMarks[:0]
	// Where everything is on the screen.
	var yv vec3
	var yx, yy, yvis float32
	if you != nil {
		yv = placeVec(*you)
		yx, yy, yvis = g.project(yv)
		if yvis > 0 {
			b.reserve(yx, yy, 6)
		}
	}
	seen, cities := b.seen, b.cities
	for i := range exits {
		ed := &exits[i]
		ed.x, ed.y, ed.vis = g.project(ed.vec)
		cities[ed.place.City] = true
		if ed.vis > 0 {
			b.reserve(ed.x, ed.y, 5)
		}
		if you != nil && live {
			n := seen[ed.place.City]
			seen[ed.place.City] = n + 1
			ed.f.arc.build(g, yv, ed.vec, arcLift(yv, ed.vec, n), arcSamples)
			ed.hasArc = true
		}
	}
	// Destination countries: a thin dashed great circle from the
	// exit to the country's representative city, fainter once no
	// connection is left; drawn under the request animation.
	destMarks := b.destMarks
	for _, ed := range exits {
		col := routeColor(ed.r, t)
		for _, d := range dests[ed.r.Group] {
			if d.place.Country == ed.place.Country {
				continue
			}
			dv := placeVec(d.place)
			alpha := float32(0.3)
			if d.conns == 0 {
				alpha = 0.14
			}
			b.dest.build(g, ed.vec, dv, arcLift(ed.vec, dv, 0)*0.6, destSamples)
			b.dest.dashed(pt, b, 1, col.Alpha(alpha))
			if dx, dy, vis := g.project(dv); vis > 0 && !cities[d.place.City] {
				cities[d.place.City] = true
				destMarks = append(destMarks, destMark{dx, dy, vis, d.place.City, ed.r.Group, d.conns > 0})
				b.reserve(dx, dy, 4)
			}
		}
	}
	for _, m := range destMarks {
		a := 0.4 + 0.6*m.vis
		pt.Fill(circle(m.x, m.y, 3.5), t.Surface, 3.5)
		pt.StrokePath(b.path.Reset().Circle(m.x, m.y, 3.5), 1.4, t.TextMuted.Alpha(a))
	}
	fr.yv, fr.yx, fr.yy, fr.yvis = yv, yx, yy, yvis
	b.destMarks = destMarks
}

// drawFlows draws each route's arc and its request animation, and the
// rings at the destinations of an arriving request.
func (p *Page) drawFlows(fr *mapFrame) {
	pt, t, b, now := fr.pt, fr.t, fr.b, fr.now
	exits, reduce, focused := fr.exits, fr.reduce, fr.focused
	destMarks := b.destMarks
	animating := false
	// The request animation along each route's arc: the route
	// solid in its colour, requests travelling over it as glowing
	// dots (more at once on a busy route), a ring spreading at the
	// exit and at the route's destinations as one arrives.
	arrivals, speeds, speedIn := b.arrivals, b.speeds, b.speedIn
	for i := range exits {
		ed := &exits[i]
		if !ed.hasArc {
			continue
		}
		ex, ey := ed.x, ed.y
		if ed.vis <= 0 {
			ex, ey = hidden[0], hidden[1]
		}
		p.addHit(ed.r.Group, &ed.f.arc, ex, ey)
		col := routeColor(ed.r, t)
		f := ed.f
		a := &f.arc
		w := arcWidth(f.speed)
		live := active(f.speed)
		// The route itself, the main one strongest; a route with
		// traffic stronger and thicker still.
		base := float32(0.4)
		if ed.r.color == (ui.Color{}) {
			base = 0.55
		}
		if !live {
			w = 1.6
		} else {
			base += 0.2
		}
		a.stroke(pt, b, 1, w, col.Alpha(base))
		if live {
			// The speed beside the arc's middle, after the markers
			// and their labels have taken their room; a short arc
			// carries it in the exit's label instead.
			txt := "↓ " + env.FmtSpeed(int64(f.speed))
			if a.length < shortArc {
				speedIn[ed.r.Group] = txt
			} else if mx, my, mv := a.pointAt(0.5); mv > 0 {
				x0, y0, _ := a.pointAt(0.45)
				x1, y1, _ := a.pointAt(0.55)
				nx, ny := -(y1 - y0), x1-x0
				if n := dist(0, 0, nx, ny); n > 0 {
					nx, ny = nx/n, ny/n
				}
				speeds = append(speeds, speedLabel{mx, my, nx, ny, w, mv, txt, col})
			}
		}
		if reduce || !focused || !live {
			// No request animation behind another app's window, nor
			// on a route without traffic (under 512 B/s): the route,
			// its width and its speed say what moves. Played for a
			// quiet route too, the animation would restart at every
			// data frame and keep the window drawing all day.
			continue
		}
		if !f.armed {
			f.armed = true
			f.start = now.Add(time.Duration(200+len(ed.r.Group)*97%600) * time.Millisecond)
		}
		dur := flowDur(a.length)
		total := dur + fadeDur + flowGap(f.speed)
		el := now.Sub(f.start)
		dot := func(x, y, vis, o float32) {
			glow := w*2 + 8
			pt.Fill(circle(x, y, glow), col.Alpha(0.14*vis*o), glow)
			pt.Fill(circle(x, y, w+4), col.Alpha(0.3*vis*o), w+4)
			pt.Fill(circle(x, y, w/2+2.5), t.Surface.Alpha(vis*o), w/2+2.5)
			pt.Fill(circle(x, y, w/2+1.2), col.Alpha(vis*o), w/2+1.2)
		}
		switch {
		case el < 0:
			// Waiting for the first request.
			animating = animating || live
		case el < dur:
			e := easeInOut(float32(el) / float32(dur))
			a.stroke(pt, b, e, w, col)
			for k := flowDots(f.speed) - 1; k >= 0; k-- {
				if at := e - float32(k)*dotTrail; at > 0 {
					if hx, hy, hv := a.pointAt(at); hv > 0 {
						dot(hx, hy, hv, 1)
					}
				}
			}
			animating = true
		case el < dur+fadeDur:
			fp := float32(el-dur) / float32(fadeDur)
			o := 1 - fp
			a.stroke(pt, b, 1, w, col.Alpha(o))
			if ed.vis > 0 {
				dot(ed.x, ed.y, ed.vis, o)
				rr := float32(rippleFrom + (rippleTo-rippleFrom)*float64(easeInOut(fp)))
				pt.StrokePath(b.path.Reset().Circle(ed.x, ed.y, rr), 2, col.Alpha(0.9*o))
			}
			arrivals = append(arrivals, arrival{ed.r.Group, o, fp})
			animating = true
		case el < total:
			// Pause between requests.
			animating = true
		default:
			if live {
				f.start = now
				animating = true
			} else {
				f.start = now.Add(200 * time.Millisecond)
			}
		}
	}
	// The ring at the destinations of an arriving request.
	for _, ar := range arrivals {
		for _, m := range destMarks {
			if m.group != ar.group || !m.live {
				continue
			}
			var col ui.Color
			for _, ed := range exits {
				if ed.r.Group == ar.group {
					col = routeColor(ed.r, t)
				}
			}
			rr := float32(rippleFrom-1) + float32(rippleTo-rippleFrom)*0.7*easeInOut(ar.fp)
			pt.StrokePath(b.path.Reset().Circle(m.x, m.y, rr), 1.5, col.Alpha(0.8*ar.o*m.vis))
		}
	}
	fr.animating = animating
	b.arrivals, b.speeds = arrivals, speeds
}

// drawLabels draws the markers of 「你」 and the exits, then every label.
func (p *Page) drawLabels(fr *mapFrame) {
	pt, t, b, r, now := fr.pt, fr.t, fr.b, fr.r, fr.now
	you, guessed, exits, ambient := fr.you, fr.guessed, fr.exits, fr.ambient
	yx, yy, yvis := fr.yx, fr.yy, fr.yvis
	destMarks, speeds, speedIn := b.destMarks, b.speeds, b.speedIn
	// Marks, then every label last over a surface-coloured pill so
	// that it reads over arcs and dots: you first, then the exits,
	// the speeds and the destination countries, each where it is
	// clear of the others; a speed or a country's label gives way.
	// The pulse around 「你」 stays under its label, which sits
	// outside the pulse's reach.
	back := t.Surface.Alpha(0.82)
	if you != nil && yvis > 0 {
		if ambient {
			ph := float32(now.UnixNano()%int64(pulseDur)) / float32(pulseDur)
			rr := float32(youRadius+2) + youPulse*ph
			pt.StrokePath(b.path.Reset().Circle(yx, yy, rr), 1.5, t.Text.Alpha(0.5*(1-ph)*yvis))
		}
		pt.Fill(circle(yx, yy, youRadius+2), t.Surface.Alpha(yvis), youRadius+2)
		pt.Fill(circle(yx, yy, youRadius), t.Text.Alpha(0.4+0.6*yvis), youRadius)
	}
	for _, ed := range exits {
		if ed.vis <= 0 {
			continue
		}
		a := 0.4 + 0.6*ed.vis
		pt.Fill(circle(ed.x, ed.y, exitRadius+2), t.Surface.Alpha(a), exitRadius+2)
		pt.Fill(circle(ed.x, ed.y, exitRadius), routeColor(ed.r, t).Alpha(a), exitRadius)
	}
	if you != nil && yvis > 0 {
		txt := i18n.Tf("你 · %s", you.City)
		if guessed {
			txt = i18n.Tf("你 · %s（推测）", you.City)
		}
		placeLabel(pt, b, r, yx, yy, youRadius+2+youPulse, txt, t.Text.Alpha(0.5+0.5*yvis), back, labelSize, true, true)
	}
	for _, ed := range exits {
		if ed.vis <= 0 {
			continue
		}
		name := ed.place.City
		if f := geo.Flag(ed.place.Country); f != "" {
			name = f + " " + name
		}
		if s := speedIn[ed.r.Group]; s != "" {
			name += " · " + s
		}
		placeLabel(pt, b, r, ed.x, ed.y, exitRadius+6, name, t.Text.Alpha(0.4+0.6*ed.vis), back, labelSize, false, true)
	}
	for _, s := range speeds {
		labelBeside(pt, b, r, s.x, s.y, s.nx, s.ny, s.w/2, s.txt, s.col.Alpha(0.6+0.4*s.vis), back, destSize, true)
	}
	for _, m := range destMarks {
		placeLabel(pt, b, r, m.x, m.y, 8, m.city, t.TextMuted.Alpha(0.4+0.6*m.vis), back, destSize, false, false)
	}
}

// youUnknown notes, beside the map's title, that the user's own location
// was not measured, the reason in the tooltip: the 「你」 on the map is
// then the time zone's city, not a measurement.
func (p *Page) youUnknown(c *ui.Context, t *ui.Theme) {
	n := p.e.API.NetInfo()
	if n.PublicIPStatus != "failed" {
		return
	}
	tip := i18n.T("未能经物理网卡实测你的位置，地图上的「你」按系统时区推测")
	if n.PublicIPError != "" {
		tip += "\n" + n.PublicIPError
	}
	ui.Text(c, i18n.T("你的位置：未知")).FontSize(11).TextColor(t.TextMuted).SingleLine().Tooltip(tip)
}

// anyLive reports whether a route carries traffic now (active): the
// request animation plays, and the idle motion with it.
func (p *Page) anyLive(rs []route) bool {
	for _, rt := range rs {
		if f := p.flows[rt.Group]; f != nil && active(f.speed) {
			return true
		}
	}
	return false
}

// Hidden is called by the shell when the window is hidden or minimized:
// the globe's ground canvas, a texture the size of the map, is dropped
// and drawn anew when the page shows again. The land dots stay: they are
// small vectors, and finding them again walks the whole land mask.
func (p *Page) Hidden() {
	p.ground.release()
}

func circle(cx, cy, r float32) ui.Rect { return ui.Rect{X: cx - r, Y: cy - r, W: 2 * r, H: 2 * r} }

// clipMargin is how far past the map's card strokes still draw: past the
// card's clip, so that no cut shows at its edge.
const clipMargin = 8
