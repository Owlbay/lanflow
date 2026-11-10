package view

import "testing"

func TestTrayRate(t *testing.T) {
	cases := map[int64]string{0: "  0B", 512: "512B", 1536: "1.5K", 12 * 1024: " 12K", 1300 * 1024: "1.3M", 45 << 20: " 45M", 1 << 30: "1.0G"}
	for b, want := range cases {
		if got := trayRate(b); got != want {
			t.Errorf("trayRate(%d) = %q, want %q", b, got, want)
		}
	}
	if got := traySpeed(12*1024, 1300*1024); got != "↓1.3M ↑ 12K" {
		t.Errorf("traySpeed = %q", got)
	}
}

func TestTrayPNGStates(t *testing.T) {
	for _, ink := range []trayInk{{}, {dark: true}, {windows: true}, {windows: true, dark: true}} {
		for st := trayOff; st <= trayErrState; st++ {
			if len(trayPNG(st, ink)) == 0 {
				t.Errorf("empty icon for state %d ink %+v", st, ink)
			}
		}
	}
}
