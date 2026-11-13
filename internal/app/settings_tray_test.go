package app

import (
	"testing"

	"lanflow/internal/model"
)

// Settings from before TraySpeed keep their choice; the old flag follows
// the new field.
func TestNormalizeTraySpeed(t *testing.T) {
	for _, c := range []struct {
		in   model.Settings
		want string
		hide bool
	}{
		{model.Settings{}, model.TraySpeedBoth, false},
		{model.Settings{HideTraySpeed: true}, model.TraySpeedOff, true},
		{model.Settings{TraySpeed: model.TraySpeedDown, HideTraySpeed: true}, model.TraySpeedDown, false},
		{model.Settings{TraySpeed: "bogus"}, model.TraySpeedBoth, false},
	} {
		got := normalizeSettings(c.in)
		if got.TraySpeed != c.want || got.HideTraySpeed != c.hide {
			t.Errorf("%+v → %q hide=%v", c.in.TraySpeed, got.TraySpeed, got.HideTraySpeed)
		}
	}
	if defaultSettings().TraySpeed != model.TraySpeedBoth {
		t.Error("default")
	}
}
