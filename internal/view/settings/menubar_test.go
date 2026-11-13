package settings

import (
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

// 菜单栏显示: each option saves, and the speed choice keeps the older
// HideTraySpeed in step.
func TestMenuBarSection(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1300)
	if !tt.HasText("菜单栏显示") || !tt.HasText("流量图") {
		t.Fatalf("section missing: %q", tt.Texts())
	}
	for _, label := range []string{"流量图", "当前节点", "代理模式"} {
		clickRole(t, tt, ui.RoleSwitch, label)
		tt.Frame()
	}
	s := f.Settings()
	if !s.TrayGraph || !s.TrayNode || !s.TrayMode {
		t.Fatalf("switches not saved: %+v", s)
	}
	if err := tt.Click("上下行"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("不显示"); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if s := f.Settings(); s.TraySpeed != model.TraySpeedOff || !s.HideTraySpeed {
		t.Fatalf("speed: %q hide=%v", s.TraySpeed, s.HideTraySpeed)
	}
	if out := os.Getenv("MENUBAR_PREVIEW"); out != "" {
		tt.SetScale(2)
		tt.Advance(time.Second)
		tt.Frame()
		w, _ := os.Create(out)
		png.Encode(w, tt.Image())
		w.Close()
	}
}
