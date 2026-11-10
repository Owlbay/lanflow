// Click testing of the exit map: the arcs and exit markers of the last
// frame, and which route lies under the pointer.

package overview

import (
	"math"
)

// hitArc is a drawn arc kept for click testing, in absolute coordinates.
type hitArc struct {
	group  string
	pts    [][2]float32
	ex, ey float32
}

// hitRoute returns the group whose arc or exit marker is under the
// pointer (coordinates relative to the map box), or "".
func (p *Page) hitRoute(x, y float32) string {
	x += p.hitOrigin[0]
	y += p.hitOrigin[1]
	best, bestD := "", float32(hitRadius)
	for _, h := range p.hits {
		// The nearest of the route's marker (which counts a little
		// closer) and its visible samples; a hidden one is NaN and never
		// nearer.
		d := float32(math.Inf(1))
		if m := dist(x, y, h.ex, h.ey) - 2; m < d {
			d = m
		}
		for _, pt := range h.pts {
			if s := dist(x, y, pt[0], pt[1]); s < d {
				d = s
			}
		}
		if d < bestD {
			best, bestD = h.group, d
		}
	}
	return best
}

func dist(x0, y0, x1, y1 float32) float32 {
	return float32(math.Hypot(float64(x1-x0), float64(y1-y0)))
}

// addHit keeps an arc of this frame for click testing, reusing the memory
// of the frame before.
func (p *Page) addHit(group string, a *arc, ex, ey float32) {
	n := len(p.hits)
	if cap(p.hits) > n {
		p.hits = p.hits[:n+1]
	} else {
		p.hits = append(p.hits, hitArc{})
	}
	h := &p.hits[n]
	h.group, h.ex, h.ey = group, ex, ey
	h.pts = a.hitPoints(h.pts)
}
