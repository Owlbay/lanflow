package fake

import (
	"bytes"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
)

// ProcessIcon returns a sample icon for an application bundle path (a
// rounded square in a colour the path picks), nil for anything else, so
// the views show icons for the fake's .app processes and the generic
// glyph for its daemons.
func (f *Fake) ProcessIcon(path string) []byte {
	path = strings.TrimSpace(path)
	if !strings.HasSuffix(strings.ToLower(path), ".app") {
		return nil
	}
	return sampleIcon(path)
}

var (
	sampleIconsMu sync.Mutex
	sampleIcons   = map[string][]byte{}
)

// sampleIcon draws a 32×32 PNG of a rounded square whose hue depends on
// key, cached by key.
func sampleIcon(key string) []byte {
	sampleIconsMu.Lock()
	defer sampleIconsMu.Unlock()
	if d, ok := sampleIcons[key]; ok {
		return d
	}
	h := fnv.New32a()
	h.Write([]byte(key))
	hue := float64(h.Sum32()%360) / 360
	fill := hsl(hue, 0.55, 0.55)
	const n, r = 32, 7
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if inRoundedSquare(x, y, n, r) {
				img.SetNRGBA(x, y, fill)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	sampleIcons[key] = buf.Bytes()
	return buf.Bytes()
}

func inRoundedSquare(x, y, n, r int) bool {
	cx, cy := x, y
	if x < r {
		cx = r
	} else if x >= n-r {
		cx = n - r - 1
	}
	if y < r {
		cy = r
	} else if y >= n-r {
		cy = n - r - 1
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

// hsl converts a colour from HSL (0…1 each) to NRGBA.
func hsl(h, s, l float64) color.NRGBA {
	c := (1 - abs(2*l-1)) * s
	x := c * (1 - abs(mod(h*6, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch int(h * 6) {
	case 0:
		r, g, b = c, x, 0
	case 1:
		r, g, b = x, c, 0
	case 2:
		r, g, b = 0, c, x
	case 3:
		r, g, b = 0, x, c
	case 4:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return color.NRGBA{R: uint8((r + m) * 255), G: uint8((g + m) * 255), B: uint8((b + m) * 255), A: 255}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func mod(v, m float64) float64 {
	for v >= m {
		v -= m
	}
	return v
}
