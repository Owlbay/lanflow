package overview

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// The ground of the globe, the graticule and the land dots, is the bulk
// of what a frame of the map paints (a few thousand dots, hundreds of
// line segments) and changes only when the globe turns, the box resizes
// or the theme changes. It is drawn into a canvas, a texture where the
// window draws on the GPU, and every other frame shows it for the price
// of an image: the request animation, the pulse and the labels over a
// still globe then cost almost nothing, and a frame that turns the globe
// redraws the canvas.
//
// The graticule is stroked opaque, in its colour blended over the disc's
// beforehand, which the GPU draws as plain segments: stroked translucent,
// a polyline is rasterized on the CPU as one mask the size of the disc,
// which was the most expensive drawing of the whole app. The blend is
// exact over the disc's flat tint and off by a hundredth of the colour's
// contrast where the limb darkens, under the 10 % the lines show at.

// gratAlpha is how faint the graticule shows.
const gratAlpha = 0.1

// groundKey is what the canvas was drawn for.
type groundKey struct {
	lat, lon, R float64
	cx, cy      float32 // the globe's centre within the box, DIPs
	w, h        int     // canvas pixels
	scale       float32
	step        float64
	dotR        float32
	land, grat  ui.Color
}

// ground holds the canvas of the globe's ground.
type ground struct {
	cv    *ui.Canvas
	key   groundKey
	valid bool
}

// draw shows the ground of the globe g in the box r, redrawing the canvas
// when the view, the size, the scale or the colours changed since it was
// drawn. land are the land dots (unit vectors) for step, drawn dotR DIPs
// in radius in landCol, fainter toward the limb; grat is the graticule's
// colour as it shows (gratAlpha of landCol over the disc), opaque.
func (gr *ground) draw(pt *ui.Painter, b *globeBuffers, r ui.Rect, g globeProj, land []vec3, step float64, dotR float32, landCol, grat ui.Color) {
	s := pt.Scale()
	if s <= 0 {
		s = 1
	}
	w, h := int(math.Ceil(float64(r.W*s))), int(math.Ceil(float64(r.H*s)))
	if w <= 0 || h <= 0 {
		return
	}
	key := groundKey{lat: g.lat0, lon: g.lon0, R: float64(g.R), cx: g.cx - r.X, cy: g.cy - r.Y, w: w, h: h, scale: s, step: step, dotR: dotR, land: landCol, grat: grat}
	if gr.cv == nil || gr.key.w != w || gr.key.h != h {
		gr.cv = ui.NewCanvas(w, h)
		gr.valid = false
	}
	if !gr.valid || gr.key != key {
		gr.key, gr.valid = key, true
		// The same projection in the canvas's pixels.
		gp := newGlobeProj(g.lat0, g.lon0, key.cx*s, key.cy*s, g.R*s)
		gr.cv.Draw(pt, func(cp *ui.Painter) {
			graticule(cp, b, gp, grat, 0.75*s)
			landDots(cp, gp, land, dotR*s, landCol, float32(w), float32(h))
		})
	}
	pt.Image(gr.cv.Bitmap(), r, ui.FillBox)
}

// release drops the canvas, for a map that is not shown; the next frame
// draws it anew.
func (gr *ground) release() {
	gr.cv, gr.valid = nil, false
}

// over blends src over dst (straight alpha) and returns the opaque result.
func over(dst, src ui.Color) ui.Color {
	a := float32(src.A) / 255
	mix := func(d, s uint8) uint8 { return uint8(float32(d)*(1-a) + float32(s)*a + 0.5) }
	return ui.Color{R: mix(dst.R, src.R), G: mix(dst.G, src.G), B: mix(dst.B, src.B), A: 255}
}

// landDots paints the front half of the land dots, fainter toward the
// limb, within w×h pixels of the projection gp.
func landDots(pt *ui.Painter, gp globeProj, land []vec3, dotR float32, col ui.Color, w, h float32) {
	for _, v := range land {
		x, y, z := gp.rotate(v)
		if z <= 0 {
			continue
		}
		sx, sy := gp.cx+float32(x)*gp.R, gp.cy-float32(y)*gp.R
		if sx < -2 || sx > w+2 || sy < -2 || sy > h+2 {
			continue
		}
		a := 0.22 + 0.3*float32(math.Min(1, z/0.45))
		pt.Fill(ui.Rect{X: sx - dotR, Y: sy - dotR, W: 2 * dotR, H: 2 * dotR}, col.Alpha(a), dotR)
	}
}
