package trayart

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/model"
)

func TestRate(t *testing.T) {
	cases := map[int64]string{0: "  0B", 512: "512B", 1536: "1.5K", 12 * 1024: " 12K", 1300 * 1024: "1.3M", 45 << 20: " 45M", 1 << 30: "1.0G"}
	for b, want := range cases {
		if got := Rate(b); got != want {
			t.Errorf("Rate(%d) = %q, want %q", b, got, want)
		}
	}
	if got := Speed(12*1024, 1300*1024); got != "↓1.3M ↑ 12K" {
		t.Errorf("Speed = %q", got)
	}
}

func TestPNGStates(t *testing.T) {
	for _, ink := range []Ink{{}, {Dark: true}, {Windows: true}, {Windows: true, Dark: true}} {
		for st := Off; st <= Error; st++ {
			for _, l := range []Look{{State: st}, {State: st, Capture: true}, {State: st, Graph: make([]float64, GraphBars)}} {
				b := PNG(l, ink)
				img, err := png.Decode(bytes.NewReader(b))
				if err != nil {
					t.Fatalf("%+v: %v", l, err)
				}
				if h := img.Bounds().Dy(); h != 36 {
					t.Errorf("%+v: height %d", l, h)
				}
				if l.Graph != nil && img.Bounds().Dx() <= 36 {
					t.Errorf("graph icon not wider: %d", img.Bounds().Dx())
				}
			}
		}
	}
}

func TestStateOf(t *testing.T) {
	run := model.CoreRunning
	for _, c := range []struct {
		st   model.Status
		want State
	}{
		{model.Status{Core: run, SystemProxy: true}, On},
		{model.Status{Core: run, TUN: true}, On}, // TUN takes traffic over too
		{model.Status{Core: run}, Half},
		{model.Status{Core: model.CoreStarting}, Half},
		{model.Status{Core: model.CoreError}, Error},
		{model.Status{}, Off},
	} {
		if got := StateOf(c.st); got != c.want {
			t.Errorf("%+v: %v, want %v", c.st, got, c.want)
		}
	}
}

func TestGraph(t *testing.T) {
	g := Graph(nil, nil)
	if len(g) != GraphBars {
		t.Fatalf("len %d", len(g))
	}
	for _, v := range g {
		if v != 0 {
			t.Fatalf("idle graph %v", g)
		}
	}
	g = Graph([]int64{0, 0, 0}, []int64{100 << 10, 0, 400 << 10})
	if g[GraphBars-1] != 1 || g[GraphBars-3] != 0.5 || g[GraphBars-2] != 0 {
		t.Fatalf("%v", g)
	}
	// tiny traffic stays low against the 32 KiB floor
	if g := Graph(nil, []int64{1 << 10}); g[GraphBars-1] > 0.2 {
		t.Fatalf("noise %v", g)
	}
}

func TestTitle(t *testing.T) {
	st := model.Status{Core: model.CoreRunning, Mode: model.ModeGlobal, Up: 12 << 10, Down: 1300 << 10}
	set := model.Settings{TraySpeed: model.TraySpeedBoth}
	if got := Title(st, "香港 01", set); got != "↓1.3M ↑ 12K" {
		t.Errorf("default %q", got)
	}
	set.TraySpeed = model.TraySpeedDown
	if got := Title(st, "", set); got != "↓1.3M" {
		t.Errorf("down %q", got)
	}
	set = model.Settings{TraySpeed: model.TraySpeedOff, TrayNode: true, TrayMode: true}
	if got := Title(st, "一个非常非常长的节点名称超过十二个字", set); got != "全局  一个非常非常长的节点名…" {
		t.Errorf("mode+node %q", got)
	}
	st.Mode = model.ModeRule
	if got := Title(st, "HK", set); got != "HK" {
		t.Errorf("rule mode hidden %q", got)
	}
	if got := Title(model.Status{}, "HK", set); got != "" {
		t.Errorf("stopped %q", got)
	}
}

// TestPreview writes a contact sheet of the icons to $TRAY_PREVIEW.
func TestPreview(t *testing.T) {
	out := os.Getenv("TRAY_PREVIEW")
	if out == "" {
		t.Skip("set TRAY_PREVIEW to a .png path")
	}
	g := Graph([]int64{0, 0, 1e3, 5e3, 2e4, 8e4, 2e5, 4e5, 3e5, 9e4, 3e4, 1e5, 6e5, 2e5}, []int64{0, 1e3, 3e4, 9e4, 3e5, 9e5, 2e6, 1.5e6, 8e5, 2e5, 9e4, 5e5, 2.4e6, 1e6})
	looks := []Look{{State: Off}, {State: Half}, {State: On}, {State: Error}, {State: On, Capture: true}, {State: On, Graph: g}, {State: Half, Capture: true, Graph: g}}
	W := 1000
	canvas := image.NewNRGBA(image.Rect(0, 0, W, 220))
	for i, dark := range []bool{false, true} {
		bg, col := color.NRGBA{236, 236, 236, 255}, color.NRGBA{0, 0, 0, 255}
		if dark {
			bg, col = color.NRGBA{40, 40, 42, 255}, color.NRGBA{255, 255, 255, 255}
		}
		draw.Draw(canvas, image.Rect(0, i*110, W, i*110+110), &image.Uniform{bg}, image.Point{}, draw.Src)
		x := 20
		for _, l := range looks {
			w := 72
			if l.Graph != nil {
				w = int(math.Round((18 + 3 + graphW) * 4))
			}
			im := ui.MustParseSVG([]byte(SVG(l))).Image(w, 72, col)
			draw.Draw(canvas, image.Rect(x, i*110+19, x+w, i*110+91), im, image.Point{}, draw.Over)
			x += w + 36
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, canvas)
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
