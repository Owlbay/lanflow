// The exit map's data: the routes with their places, colours and eased
// speeds, where 「你」 is, and the destination countries of each route.

package overview

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// routeColors are the colors of the routes after the main one (系统紫、粉、青、橙).
var routeColors = []ui.Color{ui.Hex("#AF52DE"), ui.Hex("#FF2D55"), ui.Hex("#30B0C7"), ui.Hex("#FF9500")}

// dest is one destination country of a route.
type dest struct {
	place model.Place
	conns int
	speed int64
	seen  time.Time
}

// destState caches the destination countries by route and the IP lookups.
type destState struct {
	at    time.Time
	by    map[string]map[string]*dest // group → country → dest
	lists map[string][]dest           // group → top destinations
	geo   map[string]*model.Place     // IP → country place (nil = unknown)
}

// route is one line of the map and the list.
type route struct {
	model.ExitRoute
	place *model.Place
	color ui.Color
}

// youPlace returns the user's place and whether it is a guess. Only an
// endpoint measured over the physical interface (BoundIface) is the
// user's location: anything else may be a TUN's proxy exit.
func (p *Page) youPlace() (*model.Place, bool) {
	if y := p.e.API.You(); y != nil && y.Place != nil && y.BoundIface != "" {
		return y.Place, false
	}
	// Before a measurement 「你」 sits at the system time zone's city,
	// marked 「推测」 (docs/design.md §5.1 页面状态); geo.LocalZone knows
	// where each OS keeps the zone, Windows registry included.
	if pl := localPlace(); pl != nil {
		return pl, true
	}
	return nil, true
}

// localPlace is the city of the system time zone, nil if unknown; looked
// up once, as finding the zone reads files (and on Windows the registry).
var localPlace = sync.OnceValue(func() *model.Place {
	if pl, ok := geo.FromTimeZone(geo.LocalZone()); ok {
		return &pl
	}
	return nil
})

// routes collects the exit routes with their places and colors, and
// eases the smoothed speeds towards the measured ones by the time since
// the last call (k = 1 − e^(−dt/τ)), so the easing does not depend on
// how often the page is built.
func (p *Page) routes(now time.Time) []route {
	rs := p.e.API.ExitRoutes()
	out := make([]route, 0, len(rs))
	k := 0
	seen := map[string]bool{}
	for i, r := range rs {
		rt := route{ExitRoute: r, place: r.Exit.Place}
		if i == 0 || r.Main {
			rt.color = ui.Color{}
		} else {
			rt.color = routeColors[k%len(routeColors)]
			k++
		}
		out = append(out, rt)
		seen[r.Group] = true
		f := p.flows[r.Group]
		if f == nil {
			f = &flow{speed: float64(r.Speed), eased: now}
			p.flows[r.Group] = f
		}
		f.ease(float64(r.Speed), now)
	}
	for g := range p.flows {
		if !seen[g] {
			delete(p.flows, g)
		}
	}
	return out
}

// ease moves the shown speed towards target by the time since the last
// easing; a flow drawn before it had a speed (eased zero) takes it at once.
func (f *flow) ease(target float64, now time.Time) {
	if f.eased.IsZero() {
		f.speed, f.eased = target, now
		return
	}
	dt := now.Sub(f.eased)
	if dt <= 0 {
		return
	}
	f.eased = now
	k := 1 - math.Exp(-float64(dt)/float64(speedTau))
	f.speed += (target - f.speed) * k
}

// destinations returns the destination countries of each route's live
// connections (docs/exit-map.md §4 layer B), resolved offline through the
// GeoIP database and cached by IP. It recomputes at most every destEvery.
func (p *Page) destinations(rs []route, mode model.Mode, now time.Time) map[string][]dest {
	d := &p.dests
	if d.by == nil {
		d.by = map[string]map[string]*dest{}
		d.geo = map[string]*model.Place{}
	}
	if !d.at.IsZero() && now.Sub(d.at) < destEvery {
		return d.lists
	}
	d.at = now
	routeOf := map[string]bool{}
	for _, r := range rs {
		routeOf[r.Group] = true
	}
	for _, m := range d.by {
		for _, x := range m {
			x.conns, x.speed = 0, 0
		}
	}
	for _, cn := range p.e.API.Connections() {
		if len(cn.Chains) == 0 || cn.DstIP == "" {
			continue
		}
		g := cn.Chains[0]
		if mode == model.ModeGlobal {
			g = "GLOBAL"
		}
		if !routeOf[g] {
			continue
		}
		pl := p.countryOf(cn.DstIP)
		if pl == nil {
			continue
		}
		m := d.by[g]
		if m == nil {
			m = map[string]*dest{}
			d.by[g] = m
		}
		x := m[pl.Country]
		if x == nil {
			x = &dest{place: *pl}
			m[pl.Country] = x
		}
		x.conns++
		x.speed += cn.UpSpeed + cn.DownSpeed
		x.seen = now
	}
	lists := map[string][]dest{}
	for g, m := range d.by {
		if !routeOf[g] {
			delete(d.by, g)
			continue
		}
		var ds []dest
		for c, x := range m {
			if now.Sub(x.seen) > destKeep {
				delete(m, c)
				continue
			}
			ds = append(ds, *x)
		}
		sort.Slice(ds, func(i, j int) bool {
			if ds[i].conns != ds[j].conns {
				return ds[i].conns > ds[j].conns
			}
			if !ds[i].seen.Equal(ds[j].seen) {
				return ds[i].seen.After(ds[j].seen)
			}
			return ds[i].place.Country < ds[j].place.Country
		})
		if len(ds) > maxDests {
			ds = ds[:maxDests]
		}
		if len(ds) > 0 {
			lists[g] = ds
		}
	}
	d.lists = lists
	return lists
}

// countryOf resolves a destination IP to its country's place, nil when
// unknown; results are cached per IP.
func (p *Page) countryOf(ip string) *model.Place {
	if pl, ok := p.dests.geo[ip]; ok {
		return pl
	}
	if len(p.dests.geo) > 4096 {
		p.dests.geo = map[string]*model.Place{}
	}
	var pl *model.Place
	if ep := p.e.API.LookupIP(ip); ep.Place != nil && ep.Place.Country != "" {
		cp := *ep.Place
		pl = &cp
	}
	p.dests.geo[ip] = pl
	return pl
}

func mapDescription(you *model.Place, rs []route, dests map[string][]dest) string {
	var b strings.Builder
	b.WriteString(i18n.T("出口地图"))
	if you != nil {
		b.WriteString(i18n.Tf("：从你所在的%s", you.City))
	}
	var cities, countries []string
	seen := map[string]bool{}
	for _, r := range rs {
		if r.place != nil {
			cities = append(cities, r.place.City)
		}
		for _, d := range dests[r.Group] {
			if !seen[d.place.Country] {
				seen[d.place.Country] = true
				countries = append(countries, d.place.City)
			}
		}
	}
	if len(cities) > 0 {
		b.WriteString(i18n.Tf("到%s", strings.Join(cities, i18n.T("、"))))
	}
	if len(countries) > 0 {
		b.WriteString(i18n.Tf("；目标国家：%s", strings.Join(countries, i18n.T("、"))))
	}
	return b.String()
}

func routeColor(r route, t *ui.Theme) ui.Color {
	if r.color == (ui.Color{}) {
		return t.Accent
	}
	return r.color
}
