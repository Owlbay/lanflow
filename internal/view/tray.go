package view

import (
	"bytes"
	"fmt"
	"image/color"
	"image/png"
	"runtime"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// trayState is one of the four icon states (docs/design.md §7).
type trayState int

const (
	trayOff      trayState = iota // core stopped: faint
	trayOn                        // running, system proxy on: solid
	trayHalf                      // running, system proxy off: translucent
	trayErrState                  // error: with a red badge
)

// tray is the menu bar / notification area icon and its menu.
type tray struct {
	s     *Shell
	t     *mygo.Tray
	mu    sync.Mutex
	menu  *mygo.Menu
	sig   string
	icon  trayState
	set   bool
	pngs  map[trayState][]byte
	title string                    // text beside the icon (macOS live speed)
	icons map[string]*mygo.MenuIcon // group icons by reference (traycmd.go)
}

// StartTray creates the tray icon; call it once the app is ready.
func (s *Shell) StartTray() error {
	tr := &tray{s: s, pngs: map[trayState][]byte{}}
	t, err := mygo.NewTray(mygo.TrayOptions{ID: "lanflow", ToolTip: i18n.T("澜流 Lanflow")})
	if err != nil {
		return err
	}
	tr.t = t
	s.tray = tr
	t.OnAppearanceChange(func() { tr.setIcon(tr.icon, true) })
	if runtime.GOOS == "windows" {
		// Left click opens the window, right click the menu.
		t.OnClick(s.ShowWindow)
		t.OnRightClick(func() {
			tr.mu.Lock()
			m := tr.menu
			tr.mu.Unlock()
			if m == nil {
				return
			}
			t.SetMenu(m)
			t.PopUpMenu()
			t.SetMenu(nil)
		})
	}
	tr.refresh(true)
	return nil
}

func (tr *tray) destroy() {
	if tr.t != nil {
		tr.t.Destroy()
	}
}

// changed is called from the API's OnChange, any goroutine.
func (tr *tray) changed(t app.Topic) {
	switch t {
	case app.TopicTraffic:
		// Every second: only the speed beside the icon can have changed.
		tr.setTitle(tr.s.api.Status())
	case app.TopicStatus, app.TopicProxies, app.TopicSubs, app.TopicSettings:
		tr.refresh(false)
	}
}

// refresh rebuilds the menu when what it shows changed, and updates the
// status line otherwise.
func (tr *tray) refresh(force bool) {
	st := tr.s.api.Status()
	px := tr.s.api.Proxies()
	subs := tr.s.api.Subscriptions()
	acc := tr.s.api.Settings().GlobalShortcut
	state := trayOff
	switch {
	case st.Core == model.CoreError || st.Core == model.CoreMissing:
		state = trayErrState
	case st.Core == model.CoreRunning && st.SystemProxy:
		state = trayOn
	case st.Core == model.CoreRunning || st.Core == model.CoreStarting:
		state = trayHalf
	}
	tr.setIcon(state, false)
	tr.setTitle(st)

	var sig strings.Builder
	fmt.Fprintf(&sig, "%s|%v|%s|%s|%s|%s|", st.Core, st.SystemProxy, st.Mode, st.CurrentSubscription, acc, i18n.Lang())
	g, ok := mainGroup(px, st)
	if ok {
		fmt.Fprintf(&sig, "%s=%s;%v;", g.Name, g.Now, tr.menuIcon(g.Icon) != nil)
		for _, n := range g.All {
			fmt.Fprintf(&sig, "%s:%d,", n, px.Proxies[n].Delay)
		}
	}
	for _, s := range subs {
		fmt.Fprintf(&sig, "%s=%s,", s.ID, s.Name)
	}
	tr.mu.Lock()
	rebuild := force || sig.String() != tr.sig || tr.menu == nil
	tr.sig = sig.String()
	menu := tr.menu
	tr.mu.Unlock()
	if rebuild {
		menu = tr.build(st, px, subs, g, ok, acc)
		tr.mu.Lock()
		tr.menu = menu
		tr.mu.Unlock()
		if runtime.GOOS != "windows" {
			tr.t.SetMenu(menu)
		}
		tr.t.SetToolTip(i18n.T("澜流 Lanflow"))
	} else if it := menu.ItemByID("status"); it != nil {
		it.SetLabel(statusLine(st))
	}
}

// mainGroup returns the group the tray's node submenu lists: GLOBAL in
// global mode, else the main group.
func mainGroup(px model.ProxySnapshot, st model.Status) (model.ProxyGroup, bool) {
	name := px.MainGroup
	if st.Mode == model.ModeGlobal {
		name = "GLOBAL"
	}
	for _, g := range px.Groups {
		if g.Name == name {
			return g, true
		}
	}
	for _, g := range px.Groups {
		if g.Type == model.GroupSelect && !g.Hidden {
			return g, true
		}
	}
	return model.ProxyGroup{}, false
}

func statusLine(st model.Status) string {
	switch st.Core {
	case model.CoreRunning:
		return i18n.Tf("● 运行中 · ↓ %s", env.FmtSpeed(st.Down))
	case model.CoreStarting:
		return i18n.T("◌ 启动中…")
	case model.CoreError:
		return i18n.T("● 内核异常")
	case model.CoreMissing:
		return i18n.T("● 未找到内核")
	default:
		return i18n.T("○ 未启动")
	}
}

// build makes the menu template (docs/design.md §7). acc is the global
// shortcut of the settings, shown beside 系统代理 ("" = none).
func (tr *tray) build(st model.Status, px model.ProxySnapshot, subs []model.Subscription, g model.ProxyGroup, hasGroup bool, acc string) *mygo.Menu {
	s := tr.s
	run := st.Core == model.CoreRunning
	busy := st.Core == model.CoreStarting || st.Core == model.CoreMissing
	items := []*mygo.MenuItem{
		{ID: "status", Label: statusLine(st), Disabled: true},
		mygo.Separator(),
		{ID: "core", Label: i18n.T("内核"), Type: mygo.MenuItemCheckbox, Checked: run || st.Core == model.CoreStarting, Disabled: busy,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) {
				if it.Checked {
					s.run(s.api.StartCore, i18n.T("内核已启动"))
				} else {
					s.run(s.api.StopCore, i18n.T("内核已停止"))
				}
			}},
		{ID: "sysproxy", Label: i18n.T("系统代理"), Type: mygo.MenuItemCheckbox, Checked: run && st.SystemProxy, Disabled: busy, Accelerator: acc,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) {
				on := it.Checked
				msg := i18n.T("系统代理已关闭")
				if on {
					msg = i18n.T("系统代理已开启")
				}
				s.run(func() error { return s.api.SetSystemProxy(on) }, msg)
			}},
		{ID: "tun", Label: i18n.T("TUN 模式"), Type: mygo.MenuItemCheckbox, Checked: st.TUN, Disabled: busy,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) {
				on := it.Checked
				if on && !s.api.TUNHelperInstalled() {
					s.ShowWindow() // the helper install asks in the window first
				}
				env.SetTUN(s.Env(), on)
			}},
		{Label: i18n.T("模式"), Submenu: tr.modeItems(st)},
	}
	if hasGroup {
		items = append(items,
			&mygo.MenuItem{Label: i18n.Tf("%s：%s", g.Name, g.Now), Disabled: !run, Icon: tr.menuIcon(g.Icon), Submenu: tr.nodeItems(g, px)},
			&mygo.MenuItem{Label: i18n.T("测试当前组延迟"), Disabled: !run, Click: func(*mygo.MenuItem, *mygo.Window) {
				name := g.Name
				s.run(func() error { return s.api.TestGroupDelay(name) }, i18n.Tf("「%s」测速完成", name))
			}},
		)
	}
	curSub := i18n.T("无")
	for _, sub := range subs {
		if sub.Current {
			curSub = sub.Name
		}
	}
	items = append(items,
		&mygo.MenuItem{Label: i18n.Tf("订阅：%s", curSub), Disabled: len(subs) == 0, Submenu: tr.subItems(subs)},
		mygo.Separator(),
		&mygo.MenuItem{Label: i18n.T("复制终端代理命令"), Submenu: tr.commandItems(st.MixedPort)},
		&mygo.MenuItem{Label: i18n.T("打开澜流"), Accelerator: "CmdOrCtrl+O", Click: func(*mygo.MenuItem, *mygo.Window) { s.ShowWindow() }},
		&mygo.MenuItem{ID: "feedback", Label: i18n.T("反馈问题…"), Click: func(*mygo.MenuItem, *mygo.Window) { s.OpenFeedback("") }},
		mygo.Separator(),
		&mygo.MenuItem{Label: i18n.T("退出澜流"), Accelerator: "CmdOrCtrl+Q", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	)
	return mygo.NewMenu(items)
}

// commandItems is the 复制终端代理命令 submenu: one entry per shell family.
func (tr *tray) commandItems(port int) []*mygo.MenuItem {
	s := tr.s
	var out []*mygo.MenuItem
	for _, sc := range shellCommands(port) {
		cmd := sc.Cmd
		out = append(out, &mygo.MenuItem{Label: sc.Label, ToolTip: cmd, Click: func(*mygo.MenuItem, *mygo.Window) {
			if err := mygo.Clipboard.Write(transfer.TextData(cmd)); err == nil {
				s.toast(toastReq{msg: i18n.T("已复制")})
			}
		}})
	}
	return out
}

func (tr *tray) modeItems(st model.Status) []*mygo.MenuItem {
	s := tr.s
	var out []*mygo.MenuItem
	for _, m := range []model.Mode{model.ModeRule, model.ModeGlobal, model.ModeDirect} {
		mode := m
		out = append(out, &mygo.MenuItem{Label: env.ModeName(m), Type: mygo.MenuItemRadio, Checked: st.Mode == m,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				s.run(func() error { return s.api.SetMode(mode) }, i18n.Tf("已切换到%s模式", env.ModeName(mode)))
			}})
	}
	return out
}

func (tr *tray) nodeItems(g model.ProxyGroup, px model.ProxySnapshot) []*mygo.MenuItem {
	s := tr.s
	var out []*mygo.MenuItem
	for _, n := range g.All {
		name := n
		label := n
		if p, ok := px.Proxies[n]; ok && p.Delay != 0 {
			label = n + "  " + env.FmtDelay(p.Delay, false)
		}
		out = append(out, &mygo.MenuItem{Label: label, Type: mygo.MenuItemRadio, Checked: g.Now == n, Disabled: g.Type != model.GroupSelect,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				s.run(func() error { return s.api.SelectProxy(g.Name, name) }, i18n.Tf("已切换到 %s", name))
			}})
	}
	return out
}

func (tr *tray) subItems(subs []model.Subscription) []*mygo.MenuItem {
	s := tr.s
	var out []*mygo.MenuItem
	for _, sub := range subs {
		id, name := sub.ID, sub.Name
		out = append(out, &mygo.MenuItem{Label: sub.Name, Type: mygo.MenuItemRadio, Checked: sub.Current,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				s.run(func() error { return s.api.UseSubscription(id) }, i18n.Tf("已切换到 %s", name))
			}})
	}
	return out
}

// ---- title ----

// setTitle shows the live speed beside the menu bar icon (macOS only,
// docs/design.md §7) while the core runs, unless turned off in settings.
func (tr *tray) setTitle(st model.Status) {
	if tr.t == nil || !tr.s.opts.Mac {
		return
	}
	title := ""
	if st.Core == model.CoreRunning && !tr.s.api.Settings().HideTraySpeed {
		title = traySpeed(st.Up, st.Down)
	}
	if title == tr.title {
		return
	}
	tr.title = title
	tr.t.SetTitle(title)
}

// traySpeed formats both directions compactly for the menu bar: "↓1.2M ↑ 12K".
// Values are padded with figure spaces so the title keeps its width.
func traySpeed(up, down int64) string {
	return "↓" + trayRate(down) + " ↑" + trayRate(up)
}

// trayRate is a 4-character rate: "  0B", "512B", "1.5K", " 12K", "999K", "1.2M", " 45M", "1.0G".
func trayRate(b int64) string {
	if b < 0 {
		b = 0
	}
	var s string
	switch {
	case b < 1000:
		s = fmt.Sprintf("%dB", b)
	case b < 1<<20:
		s = trayUnit(float64(b)/1024, "K")
	case b < 1<<30:
		s = trayUnit(float64(b)/(1<<20), "M")
	default:
		s = trayUnit(float64(b)/(1<<30), "G")
	}
	for n := len([]rune(s)); n < 4; n++ {
		s = " " + s
	}
	return s
}

func trayUnit(v float64, unit string) string {
	if v < 9.95 {
		return fmt.Sprintf("%.1f%s", v, unit)
	}
	if v > 999 {
		v = 999
	}
	return fmt.Sprintf("%.0f%s", v, unit)
}

// ---- icon ----

// waves is the Lanflow mark: three waves.
const waves = `<path d="M2 5.5c2.3-2 4.7-2 7 0s4.7 2 7 0"/><path d="M2 9.5c2.3-2 4.7-2 7 0s4.7 2 7 0"/><path d="M2 13.5c2.3-2 4.7-2 7 0s4.7 2 7 0"/>`

// trayInk is the icon's colour scheme (docs/design.md §3.7, §7): macOS
// uses template icons (black with alpha, the bar tints them); Windows
// draws in colour: the system accent when on, half-transparent accent
// when the system proxy is off, grey when stopped, grey with a red badge
// on error.
type trayInk struct {
	windows bool
	dark    bool        // the bar is dark (error icon on macOS, grey on Windows)
	accent  color.NRGBA // Windows accent; zero = default blue
}

// setIcon draws the icon for a state.
func (tr *tray) setIcon(state trayState, force bool) {
	if tr.t == nil {
		return
	}
	if !force && tr.set && state == tr.icon {
		return
	}
	tr.icon, tr.set = state, true
	windows := runtime.GOOS == "windows"
	template := !windows && state != trayErrState
	ink := trayInk{windows: windows, dark: tr.t.Dark()}
	if windows {
		ink.accent = mygo.Theme.Preferences().Accent
	}
	// Template icons never change with the bar; the others do, so they
	// are cached separately for a light and a dark bar.
	key := state
	if !template && ink.dark {
		key = state + trayErrState + 1
	}
	data, ok := tr.pngs[key]
	if !ok || windows { // the accent may change; icons are cheap to draw
		data = trayPNG(state, ink)
		tr.pngs[key] = data
	}
	_ = tr.t.SetIcon(data, template)
}

// trayPNG renders the tray icon of a state as a 36×36 PNG (18 pt at 2×).
func trayPNG(state trayState, ink trayInk) []byte {
	width := "1.4"
	if state == trayOn {
		width = "2"
	}
	var col color.NRGBA
	if ink.windows {
		accent := ink.accent
		if accent.A == 0 {
			accent = color.NRGBA{0, 120, 212, 255} // Windows default blue
		}
		grey := color.NRGBA{110, 110, 110, 255}
		if ink.dark {
			grey = color.NRGBA{160, 160, 160, 255}
		}
		switch state {
		case trayOn:
			col = accent
		case trayHalf:
			col = accent
			col.A = 128
		default: // off, error
			col = grey
		}
	} else {
		alpha := uint8(255)
		switch state {
		case trayOff:
			alpha = 110
		case trayHalf:
			alpha = 150
		}
		col = color.NRGBA{0, 0, 0, alpha}
		if ink.dark {
			col = color.NRGBA{255, 255, 255, alpha}
		}
	}
	badge := ""
	if state == trayErrState {
		badge = `<circle cx="15" cy="3.5" r="2.6" fill="#FF3B30" stroke="none"/>`
	}
	s := ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 18 18" fill="none" stroke="currentColor" stroke-width="` + width + `" stroke-linecap="round">` + waves + badge + `</svg>`))
	img := s.Image(36, 36, col)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
