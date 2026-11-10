package overview

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/model"
)

func TestFlowTimings(t *testing.T) {
	if g := flowGap(800e3); g != 120*time.Millisecond {
		t.Errorf("gap at 800 KB/s = %v", g)
	}
	if g := flowGap(200e3); g != 320*time.Millisecond {
		t.Errorf("gap at 200 KB/s = %v", g)
	}
	if g := flowGap(10); g != 700*time.Millisecond {
		t.Errorf("gap at 10 B/s = %v", g)
	}
	if flowDots(800e3) != 3 || flowDots(200e3) != 2 || flowDots(10) != 1 {
		t.Errorf("dots %d %d %d", flowDots(800e3), flowDots(200e3), flowDots(10))
	}
	if active(100) || !active(2048) {
		t.Error("a route is active from 512 B/s")
	}
	if d := flowDur(10); d != 900*time.Millisecond {
		t.Errorf("short arc dur %v", d)
	}
	if d := flowDur(1000); d != 1600*time.Millisecond {
		t.Errorf("long arc dur %v", d)
	}
	if w := arcWidth(0); w < 1.99 || w > 2.01 {
		t.Errorf("width at rest %v", w)
	}
	if w := arcWidth(1e9); w > 10.01 {
		t.Errorf("width capped %v", w)
	}
	if w := arcWidth(500e3); w < 5 || w > 10 {
		t.Errorf("width under load %v", w)
	}
}

func TestProxyCommand(t *testing.T) {
	if got := ProxyCommand(7890, true); got != "export https_proxy=http://127.0.0.1:7890 http_proxy=http://127.0.0.1:7890 all_proxy=socks5://127.0.0.1:7890" {
		t.Errorf("mac command %q", got)
	}
	if got := ProxyCommand(0, false); got == "" || got[0] != '$' {
		t.Errorf("windows command %q", got)
	}
}

func TestExitGlidesToNewCity(t *testing.T) {
	tokyo := model.Place{Country: "JP", City: "东京", Lat: 35.68, Lon: 139.69}
	hk := model.Place{Country: "HK", City: "香港", Lat: 22.3, Lon: 114.17}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &flow{}
	if pl, moving := f.place(tokyo, now, false); moving || pl != tokyo {
		t.Fatalf("first place should jump: %v %v", pl, moving)
	}
	// Switching node: halfway through the glide the marker is between the cities.
	pl, moving := f.place(hk, now.Add(time.Second), false)
	if !moving || pl.Lat != tokyo.Lat {
		t.Fatalf("glide should start at the old place: %+v moving=%v", pl, moving)
	}
	pl, moving = f.place(hk, now.Add(time.Second+glideDur/2), false)
	if !moving || pl.Lat <= hk.Lat || pl.Lat >= tokyo.Lat || pl.Lon <= hk.Lon || pl.Lon >= tokyo.Lon {
		t.Errorf("midway place %+v should lie between 香港 and 东京", pl)
	}
	if pl.City != "香港" {
		t.Errorf("label should be the target city, got %q", pl.City)
	}
	pl, moving = f.place(hk, now.Add(time.Second+glideDur), false)
	if moving || pl != hk {
		t.Errorf("after the glide: %+v moving=%v", pl, moving)
	}
	// Reduced motion jumps.
	f = &flow{}
	f.place(tokyo, now, true)
	if pl, moving := f.place(hk, now.Add(time.Second), true); moving || pl != hk {
		t.Errorf("reduced motion should jump: %+v %v", pl, moving)
	}
}

func TestLerpPlaceShortestWay(t *testing.T) {
	a := model.Place{Lat: 35, Lon: 170}
	b := model.Place{Lat: 35, Lon: -170}
	m := lerpPlace(a, b, 0.5)
	if math.Abs(math.Abs(m.Lon)-180) > 0.01 {
		t.Errorf("midpoint across the antimeridian should be at ±180, got %v", m.Lon)
	}
}

func TestExpiryText(t *testing.T) {
	th := &ui.Theme{}
	th.Danger, th.Warning, th.TextMuted = ui.Hex("#FF3B30"), ui.Hex("#FF9500"), ui.Hex("#8E8E93")
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	if s, c := expiryText(now.Add(-time.Hour), now, th); s != "已过期" || c != th.Danger {
		t.Errorf("expired: %q %v", s, c)
	}
	if s, c := expiryText(now.Add(3*24*time.Hour), now, th); s != "到期 2026-11-12" || c != th.Warning {
		t.Errorf("soon: %q %v", s, c)
	}
	if s, c := expiryText(now.Add(90*24*time.Hour), now, th); s != "到期 2027-02-07" || c != th.TextMuted {
		t.Errorf("later: %q %v", s, c)
	}
}

// TestPathNoteStyling: only a contradiction is an orange warning; a relay
// or chain note of a measured exit is muted information, and a path note
// of a name-guessed exit is not shown (docs/design.md §5.1 名不副实提示,
// docs/exit-map.md §4A).
func TestPathNoteStyling(t *testing.T) {
	th := ui.LightTheme()
	note, col := pathNote(model.ExitInfo{Source: model.LocateProbe, Path: model.PathSuspicious, PathNote: "名称为台湾，实测出口在香港"}, th)
	if note != "名称为台湾，实测出口在香港" || col != th.Warning {
		t.Errorf("suspicious: %q %v", note, col)
	}
	note, col = pathNote(model.ExitInfo{Source: model.LocateProbe, Path: model.PathRelay, PathNote: "入口 上海 中国电信 → 落地 洛杉矶"}, th)
	if note != "经中转 · 入口 上海 中国电信 → 落地 洛杉矶" || col != th.TextMuted {
		t.Errorf("relay: %q %v", note, col)
	}
	note, col = pathNote(model.ExitInfo{Source: model.LocateProbe, Path: model.PathChain, PathNote: "3 跳链式代理"}, th)
	if note != "3 跳链式代理" || col != th.TextMuted {
		t.Errorf("chain: %q %v", note, col)
	}
	if note, _ = pathNote(model.ExitInfo{Source: model.LocateName, Path: model.PathRelay, PathNote: "入口 上海 → 落地 香港"}, th); note != "" {
		t.Errorf("name-guessed relay shown: %q", note)
	}
	if note, _ = pathNote(model.ExitInfo{Source: model.LocateProbe, Path: model.PathDirect}, th); note != "" {
		t.Errorf("empty note shown: %q", note)
	}
}

// TestSpeedEaseByTime: the shown speed follows the measured one by time,
// not by how many builds there were.
func TestSpeedEaseByTime(t *testing.T) {
	t0 := time.Unix(1000, 0)
	a := &flow{}
	a.ease(0, t0)
	a.ease(1000, t0.Add(time.Second))
	b := &flow{}
	b.ease(0, t0)
	for i := 1; i <= 10; i++ {
		b.ease(1000, t0.Add(time.Duration(i)*100*time.Millisecond))
	}
	if math.Abs(a.speed-b.speed) > 1e-6 {
		t.Fatalf("one step %v, ten steps %v", a.speed, b.speed)
	}
	if want := 1000 * (1 - math.Exp(-1)); math.Abs(a.speed-want) > 1e-6 {
		t.Fatalf("speed %v, want %v", a.speed, want)
	}
}

// TestFitMemo: the framing is computed again only when its inputs change.
func TestFitMemo(t *testing.T) {
	var m fitMemo
	you := model.Place{City: "上海", Lat: 31.2, Lon: 121.5}
	exits := []model.Place{{City: "东京", Lat: 35.7, Lon: 139.7}}
	a := m.fit(you, exits, nil, 400, 300)
	exits[0].Lat = 0 // the memo keeps its own copy
	exits[0].Lat = 35.7
	if b := m.fit(you, exits, nil, 400, 300); b != a {
		t.Fatalf("same inputs, new framing: %+v vs %+v", a, b)
	}
	if want := fitGlobe(you, exits, nil, 400, 300); a != want {
		t.Fatalf("memo %+v, fitGlobe %+v", a, want)
	}
	exits = append(exits, model.Place{City: "香港", Lat: 22.3, Lon: 114.2})
	if c := m.fit(you, exits, nil, 400, 300); c == a || c != fitGlobe(you, exits, nil, 400, 300) {
		t.Fatalf("a new exit must frame again: %+v", c)
	}
}
