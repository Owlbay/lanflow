package procicon

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
)

// stub serves icons from a map and counts the asks.
type stub struct {
	*fake.Fake
	data  map[string][]byte
	calls map[string]int
}

func (s *stub) ProcessIcon(path string) []byte {
	s.calls[path]++
	return s.data[path]
}

func samplePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.SetNRGBA(0, 0, color.NRGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xff})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSource(t *testing.T) {
	api := &stub{Fake: fake.NewInstant(), data: map[string][]byte{}, calls: map[string]int{}}
	c := New(api)
	if c.Source("") != nil || api.calls[""] != 0 {
		t.Fatal("an empty path asked the API")
	}
	// Unknown yet: asked again every build, since the icon may land.
	if c.Source("/Applications/A.app") != nil || c.Source("/Applications/A.app") != nil {
		t.Fatal("icon out of nothing")
	}
	if api.calls["/Applications/A.app"] != 2 {
		t.Fatalf("asked %d times while unknown, want 2", api.calls["/Applications/A.app"])
	}
	// Landed: decoded once and kept.
	api.data["/Applications/A.app"] = samplePNG(t)
	s := c.Source("/Applications/A.app")
	b, ok := s.(*ui.Bitmap)
	if !ok || b == nil {
		t.Fatalf("decoded icon is %T", s)
	}
	if w, h := b.Size(); w != 8 || h != 8 {
		t.Fatalf("decoded icon is %d×%d", w, h)
	}
	if c.Source("/Applications/A.app") != s || api.calls["/Applications/A.app"] != 3 {
		t.Fatalf("decoded icon not cached (asked %d times)", api.calls["/Applications/A.app"])
	}
	// Bytes that are no image: given up on, not asked again.
	api.data["/Applications/B.app"] = []byte("not a png")
	if c.Source("/Applications/B.app") != nil || c.Source("/Applications/B.app") != nil {
		t.Fatal("icon out of garbage")
	}
	if api.calls["/Applications/B.app"] != 1 {
		t.Fatalf("asked %d times about garbage, want 1", api.calls["/Applications/B.app"])
	}
	var none *Cache
	if none.Source("/Applications/A.app") != nil {
		t.Fatal("a nil cache has icons")
	}
}

// TestDrawAndPaint renders an icon and the generic glyph both as elements
// and through a painter.
func TestDrawAndPaint(t *testing.T) {
	api := &stub{Fake: fake.NewInstant(), data: map[string][]byte{"/Applications/A.app": samplePNG(t)}, calls: map[string]int{}}
	c := New(api)
	painted := 0
	tt := ui.NewTester(func(ctx *ui.Context) {
		th := ctx.Theme()
		ui.Column(ctx).Children(func() {
			c.Draw(ctx, th, "/Applications/A.app", Size)
			c.Draw(ctx, th, "/usr/bin/curl", Size)
			ui.Text(ctx, "after")
			ui.Box(ctx).Size(40, 40).Draw(func(pt *ui.Painter, r ui.Rect) {
				c.Paint(pt, "/Applications/A.app", ui.Rect{X: r.X, Y: r.Y, W: Size, H: Size})
				c.Paint(pt, "/usr/bin/curl", ui.Rect{X: r.X + 20, Y: r.Y, W: Size, H: Size})
				painted++
			})
		})
	}, 200, 100)
	tt.Frame()
	if !tt.HasText("after") || painted == 0 {
		t.Fatalf("not rendered: painted %d, texts %q", painted, tt.Texts())
	}
	if c.Source("/Applications/A.app") == nil {
		t.Fatal("the icon was not decoded while drawing")
	}
}
