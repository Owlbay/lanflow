// Package trayart draws the menu bar / notification area icon and
// composes the text beside it (docs/design.md §7), for the tray and the
// settings preview.
package trayart

import (
	"bytes"
	"fmt"
	"image/color"
	"image/png"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// State is one of the four icon states (docs/design.md §7).
type State int

const (
	Off   State = iota // core stopped: faint
	On                 // running, system proxy on: solid
	Half               // running, system proxy off: translucent
	Error              // error: with a red badge
)

// The menu bar / notification area icon (docs/design.md §7): the Lanflow
// mark, three waves, drawn at one weight in every state. The state shows
// in which waves are solid: all three when traffic is taken over (system
// proxy or TUN), the middle one when only the core runs, none when it is
// stopped. A dot in the top right corner marks HTTPS decryption, a red one
// an error. On macOS a small traffic graph may follow the mark.

// Look is everything the icon shows.
type Look struct {
	State   State
	Capture bool      // HTTPS decryption on
	Graph   []float64 // nil = no graph; else bar heights 0..1, oldest first
}

func (l Look) Key() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%v|%v|", l.State, l.Capture, l.Graph != nil)
	for _, v := range l.Graph {
		fmt.Fprintf(&b, "%.2f,", v)
	}
	return b.String()
}

// Ink is the icon's colour scheme (docs/design.md §3.7, §7): macOS
// uses template icons (black with alpha, the bar tints them); Windows
// draws in colour: the system accent when running, grey when stopped.
type Ink struct {
	Windows bool
	Dark    bool        // the bar is dark (error icon on macOS, grey on Windows)
	Accent  color.NRGBA // Windows accent; zero = default blue
}

const (
	waveWidth = "1.6"
	faint     = 0.32 // opacity of an inactive wave
	GraphBars = 8
	graphW    = 20.8 // the graph's width in points, after a 3-point gap
)

// The waves; the top one in full, and cut short to leave room for a badge.
const (
	waveTop    = "M2 5.5c2.3-2 4.7-2 7 0s4.7 2 7 0"
	waveTopCut = "M2 5.5c2.3-2 4.7-2 7 0"
	waveMid    = "M2 9.5c2.3-2 4.7-2 7 0s4.7 2 7 0"
	waveBottom = "M2 13.5c2.3-2 4.7-2 7 0s4.7 2 7 0"
)

func waveOpacity(state State) [3]float64 {
	switch state {
	case On:
		return [3]float64{1, 1, 1}
	case Half:
		return [3]float64{faint, 1, faint}
	default: // off, error
		return [3]float64{faint, faint, faint}
	}
}

// SVG is the icon as SVG in points (18 tall).
func SVG(l Look) string {
	w := 18.0
	if l.Graph != nil {
		w += 3 + graphW
	}
	op := waveOpacity(l.State)
	badge := l.Capture || l.State == Error
	top := waveTop
	if badge {
		top = waveTopCut
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %g 18" fill="none" stroke="currentColor" stroke-width="%s" stroke-linecap="round">`, w, waveWidth)
	for i, d := range []string{top, waveMid, waveBottom} {
		fmt.Fprintf(&b, `<path d="%s" stroke-opacity="%.2f"/>`, d, op[i])
	}
	switch {
	case l.State == Error:
		b.WriteString(`<circle cx="14" cy="4.6" r="2.4" fill="#FF3B30" stroke="none"/>`)
	case l.Capture:
		b.WriteString(`<circle cx="14" cy="4.6" r="2.1" fill="currentColor" stroke="none"/>`)
	}
	if l.Graph != nil {
		// bars on a faint baseline, bottom-aligned with the lowest wave
		x0, base, h := 21.0, 15.5, 12.0
		step := graphW / GraphBars
		bw := step * 0.62
		fmt.Fprintf(&b, `<line x1="%g" y1="%g" x2="%g" y2="%g" stroke-width="1" stroke-opacity="0.3"/>`, x0, base+0.6, x0+graphW-(step-bw), base+0.6)
		alpha := 1.0
		if l.State != On && l.State != Half {
			alpha = faint
		}
		for i, v := range l.Graph {
			if v <= 0 {
				continue
			}
			bh := math.Max(1, math.Min(1, v)*h)
			fmt.Fprintf(&b, `<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" rx="0.5" fill="currentColor" fill-opacity="%.2f" stroke="none"/>`,
				x0+float64(i)*step, base-bh, bw, bh, alpha)
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// PNG renders the icon at 2× (36 pixels tall).
func PNG(l Look, ink Ink) []byte {
	var col color.NRGBA
	if ink.Windows {
		accent := ink.Accent
		if accent.A == 0 {
			accent = color.NRGBA{0, 120, 212, 255} // Windows default blue
		}
		grey := color.NRGBA{110, 110, 110, 255}
		if ink.Dark {
			grey = color.NRGBA{160, 160, 160, 255}
		}
		col = grey
		if l.State == On || l.State == Half {
			col = accent
		}
	} else {
		col = color.NRGBA{0, 0, 0, 255}
		if ink.Dark {
			col = color.NRGBA{255, 255, 255, 255}
		}
	}
	s := ui.MustParseSVG([]byte(SVG(l)))
	w := 36
	if l.Graph != nil {
		w = int(math.Round((18 + 3 + graphW) * 2))
	}
	img := s.Image(w, 36, col)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// Graph turns the traffic history (bytes/s, oldest first) into bar
// heights: the last GraphBars seconds of up + down, square-root scaled
// against the window's peak (at least 32 KiB/s, so idle noise stays low).
func Graph(up, down []int64) []float64 {
	n := max(len(up), len(down))
	out := make([]float64, GraphBars)
	var peak float64 = 32 << 10
	vals := make([]float64, GraphBars)
	for i := 0; i < GraphBars; i++ {
		j := n - GraphBars + i
		if j < 0 {
			continue
		}
		var v int64
		if j < len(up) {
			v += up[j]
		}
		if j < len(down) {
			v += down[j]
		}
		vals[i] = float64(max(v, 0))
		peak = math.Max(peak, vals[i])
	}
	for i, v := range vals {
		out[i] = math.Round(math.Sqrt(v/peak)*20) / 20 // 5% steps: fewer redraws
	}
	return out
}

// StateOf is the icon state of a status: on when traffic is taken over
// by the system proxy or TUN.
func StateOf(st model.Status) State {
	switch {
	case st.Core == model.CoreError || st.Core == model.CoreMissing:
		return Error
	case st.Core == model.CoreRunning && (st.SystemProxy || st.TUN):
		return On
	case st.Core == model.CoreRunning || st.Core == model.CoreStarting:
		return Half
	}
	return Off
}

// Title is the text beside the macOS icon, from the settings: mode
// (when not rule), current node, speed, in that order.
func Title(st model.Status, node string, set model.Settings) string {
	if st.Core != model.CoreRunning {
		return ""
	}
	var parts []string
	if set.TrayMode && st.Mode != "" && st.Mode != model.ModeRule {
		parts = append(parts, env.ModeName(st.Mode))
	}
	if set.TrayNode && node != "" {
		parts = append(parts, ellipsize(node, 12))
	}
	switch set.TraySpeed {
	case model.TraySpeedOff:
	case model.TraySpeedDown:
		parts = append(parts, "↓"+Rate(st.Down)) // padded: the width stays put
	default:
		if !set.HideTraySpeed {
			parts = append(parts, Speed(st.Up, st.Down))
		}
	}
	return strings.Join(parts, "  ")
}

func ellipsize(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// Speed formats both directions compactly for the menu bar: "↓1.2M ↑ 12K".
// Values are padded with figure spaces so the title keeps its width.
func Speed(up, down int64) string {
	return "↓" + Rate(down) + " ↑" + Rate(up)
}

// Rate is a 4-character rate: "  0B", "512B", "1.5K", " 12K", "999K", "1.2M", " 45M", "1.0G".
func Rate(b int64) string {
	if b < 0 {
		b = 0
	}
	var s string
	switch {
	case b < 1000:
		s = fmt.Sprintf("%dB", b)
	case b < 1<<20:
		s = unit(float64(b)/1024, "K")
	case b < 1<<30:
		s = unit(float64(b)/(1<<20), "M")
	default:
		s = unit(float64(b)/(1<<30), "G")
	}
	for n := len([]rune(s)); n < 4; n++ {
		s = " " + s
	}
	return s
}

func unit(v float64, unit string) string {
	if v < 9.95 {
		return fmt.Sprintf("%.1f%s", v, unit)
	}
	if v > 999 {
		v = 999
	}
	return fmt.Sprintf("%.0f%s", v, unit)
}
