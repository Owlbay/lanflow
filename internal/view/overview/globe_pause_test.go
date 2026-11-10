package overview

import (
	"testing"
	"time"

	"lanflow/internal/model"
)

// TestSwayPausesWithInactiveWindow: with the window inactive (paused) the
// globe does not sway and asks for no frames; when the window is active
// again it keeps where the sway left it and sways again only after the
// idle delay, so nothing jumps.
func TestSwayPausesWithInactiveWindow(t *testing.T) {
	you := model.Place{City: "上海", Lat: 31.23, Lon: 121.47}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var v viewState
	a := fitGlobe(you, nil, nil, 480, 150)
	v.fit(a, now, false)
	at := now.Add(autoDelay + swayPeriod/4)
	f, moving := v.shown(at, false)
	if !moving || !near(f.lon, a.lon+swayAmp, 1e-6) {
		t.Fatalf("active window should sway: %+v moving=%v", f, moving)
	}
	v.paused = true
	f, moving = v.shown(at.Add(time.Second), false)
	if moving || !near(f.lon, a.lon+swayAmp, 1e-6) {
		t.Errorf("paused: the globe should stand where the sway left it: %+v moving=%v", f, moving)
	}
	f, moving = v.shown(at.Add(time.Hour), false)
	if moving || !near(f.lon, a.lon+swayAmp, 1e-6) {
		t.Errorf("paused an hour: still no motion: %+v moving=%v", f, moving)
	}
	v.paused = false
	back := at.Add(time.Hour)
	if f, moving := v.shown(back, false); moving || !near(f.lon, a.lon+swayAmp, 1e-9) {
		t.Errorf("active again: no jump, the sway waits for the idle delay: %+v moving=%v", f, moving)
	}
	if f, moving := v.shown(back.Add(autoDelay/2), false); moving || !near(f.lon, a.lon+swayAmp, 1e-9) {
		t.Errorf("before the idle delay: %+v moving=%v", f, moving)
	}
	f, moving = v.shown(back.Add(autoDelay+swayPeriod/4), false)
	if !moving || !near(f.lon, a.lon+2*swayAmp, 1e-6) {
		t.Errorf("the sway should resume from where it stopped: %+v moving=%v", f, moving)
	}
}
