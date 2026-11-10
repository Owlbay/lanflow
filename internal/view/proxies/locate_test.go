package proxies

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
)

// 定位当前节点 expands the group and tints its current member for a
// moment; group icons decode from the fake's inline SVG.
func TestLocateAndGroupIcons(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	p.open["流媒体"], p.openInit["流媒体"] = false, true
	tt.Frame()
	info, ok := tt.AccessByRole(ui.RoleButton, "定位当前节点 流媒体")
	if !ok {
		t.Fatalf("no locate button: %q", tt.Texts())
	}
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	if !p.open["流媒体"] || p.locate.group != "流媒体" || p.locate.until.IsZero() {
		t.Fatalf("locate state: open=%v %+v", p.open["流媒体"], p.locate)
	}
	tt.Frame()
	if !p.locate.shown {
		t.Fatal("the current member's row should have been revealed")
	}
	if _, ok := p.icons[api.Proxies().Groups[0].Icon]; !ok {
		t.Fatal("the group's inline SVG icon should decode and be cached")
	}
	if decodeIcon([]byte("not an image")) != nil {
		t.Fatal("garbage must not decode")
	}
}
