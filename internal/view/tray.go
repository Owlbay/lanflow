package view

import (
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/trayart"
)

// tray is the menu bar / notification area icon and its menu. It acts
// through its host: the window's shell in the single process, the
// service process's Background otherwise (host.go).
type tray struct {
	h     host
	t     *mygo.Tray
	mu    sync.Mutex
	menu  *mygo.Menu
	sig   string
	look  trayart.Look              // what the icon shows
	drawn string                    // look.key() + ink of the icon set last
	node  string                    // the main group's current node, for the title
	title string                    // text beside the icon (macOS)
	icons map[string]*mygo.MenuIcon // group icons by reference (traycmd.go)
}

// StartTray creates the tray icon; call it once the app is ready.
func (s *Shell) StartTray() error {
	tr, err := startTray(s)
	if err != nil {
		return err
	}
	s.tray = tr
	return nil
}

func startTray(h host) (*tray, error) {
	tr := &tray{h: h}
	t, err := mygo.NewTray(mygo.TrayOptions{ID: "lanflow", ToolTip: i18n.T("澜流 Lanflow")})
	if err != nil {
		return nil, err
	}
	tr.t = t
	t.OnAppearanceChange(func() { tr.drawIcon(true) })
	if runtime.GOOS == "windows" {
		// Left click opens the window, right click the menu.
		t.OnClick(func() { h.showWindow("") })
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
	return tr, nil
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
		// Every second: only the speed beside the icon and the graph can
		// have changed.
		st := tr.h.hostAPI().Status()
		tr.setTitle(st)
		tr.updateGraph()
	case app.TopicStatus, app.TopicProxies, app.TopicSubs, app.TopicSettings, app.TopicCapture:
		tr.refresh(false)
	}
}

// refresh rebuilds the menu when what it shows changed, and updates the
// status line otherwise.
func (tr *tray) refresh(force bool) {
	api := tr.h.hostAPI()
	st := api.Status()
	px := api.Proxies()
	subs := api.Subscriptions()
	acc := api.Settings().GlobalShortcut
	capOn := api.CaptureStatus().Enabled
	g, ok := mainGroup(px, st)
	tr.mu.Lock()
	tr.look.State, tr.look.Capture = trayart.StateOf(st), capOn
	if ok {
		tr.node = g.Now
	} else {
		tr.node = ""
	}
	tr.mu.Unlock()
	tr.updateGraph() // draws the icon
	tr.setTitle(st)

	extra := tr.h.extraItems()
	var sig strings.Builder
	fmt.Fprintf(&sig, "%s|%v|%s|%s|%s|%s|%v|%d|", st.Core, st.SystemProxy, st.Mode, st.CurrentSubscription, acc, i18n.Lang(), capOn, len(extra))
	for _, it := range extra {
		sig.WriteString(it.Label + ";")
	}
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
		menu = tr.build(st, px, subs, g, ok, acc, extra)
		tr.mu.Lock()
		tr.menu = menu
		tr.mu.Unlock()
		if runtime.GOOS != "windows" {
			tr.t.SetMenu(menu)
		}
		tip := i18n.T("澜流 Lanflow")
		if capOn {
			tip += " · " + i18n.T("HTTPS 解密中")
		}
		tr.t.SetToolTip(tip)
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
func (tr *tray) build(st model.Status, px model.ProxySnapshot, subs []model.Subscription, g model.ProxyGroup, hasGroup bool, acc string, extra []*mygo.MenuItem) *mygo.Menu {
	h := tr.h
	api := h.hostAPI()
	run := st.Core == model.CoreRunning
	busy := st.Core == model.CoreStarting || st.Core == model.CoreMissing
	items := []*mygo.MenuItem{
		{ID: "status", Label: statusLine(st), Disabled: true},
		mygo.Separator(),
	}
	if len(extra) > 0 {
		items = append(items, extra...)
		items = append(items, mygo.Separator())
	}
	items = append(items, []*mygo.MenuItem{
		{ID: "core", Label: i18n.T("内核"), Type: mygo.MenuItemCheckbox, Checked: run || st.Core == model.CoreStarting, Disabled: busy,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) {
				if it.Checked {
					h.runJob(api.StartCore, i18n.T("内核已启动"))
				} else {
					h.runJob(api.StopCore, i18n.T("内核已停止"))
				}
			}},
		{ID: "sysproxy", Label: i18n.T("系统代理"), Type: mygo.MenuItemCheckbox, Checked: run && st.SystemProxy, Disabled: busy, Accelerator: acc,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) {
				on := it.Checked
				msg := i18n.T("系统代理已关闭")
				if on {
					msg = i18n.T("系统代理已开启")
				}
				h.runJob(func() error { return api.SetSystemProxy(on) }, msg)
			}},
		{ID: "tun", Label: i18n.T("TUN 模式"), Type: mygo.MenuItemCheckbox, Checked: st.TUN, Disabled: busy,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) {
				h.setTUN(it.Checked)
			}},
		{Label: i18n.T("模式"), Submenu: tr.modeItems(st)},
		tr.captureItem(),
	}...)
	if hasGroup {
		items = append(items,
			&mygo.MenuItem{Label: i18n.Tf("%s：%s", g.Name, g.Now), Disabled: !run, Icon: tr.menuIcon(g.Icon), Submenu: tr.nodeItems(g, px)},
			&mygo.MenuItem{Label: i18n.T("测试当前组延迟"), Disabled: !run, Click: func(*mygo.MenuItem, *mygo.Window) {
				name := g.Name
				h.runJob(func() error { return api.TestGroupDelay(name) }, i18n.Tf("「%s」测速完成", name))
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
		&mygo.MenuItem{Label: i18n.T("打开澜流"), Accelerator: "CmdOrCtrl+O", Click: func(*mygo.MenuItem, *mygo.Window) { h.showWindow("") }},
		&mygo.MenuItem{ID: "feedback", Label: i18n.T("反馈问题…"), Click: func(*mygo.MenuItem, *mygo.Window) { h.showWindow(OpenFeedback) }},
		mygo.Separator(),
		&mygo.MenuItem{Label: i18n.T("退出澜流"), Accelerator: "CmdOrCtrl+Q", Click: func(*mygo.MenuItem, *mygo.Window) { h.quit() }},
	)
	return mygo.NewMenu(items)
}

// captureItem is 「HTTPS 解密」: unchecking turns decryption off at once;
// checking opens the 抓包 page, where turning on is confirmed.
func (tr *tray) captureItem() *mygo.MenuItem {
	h := tr.h
	api := h.hostAPI()
	on := api.CaptureStatus().Enabled
	return &mygo.MenuItem{ID: "capture", Label: i18n.T("HTTPS 解密"), Type: mygo.MenuItemCheckbox, Checked: on,
		Click: func(it *mygo.MenuItem, _ *mygo.Window) {
			if !it.Checked {
				h.runJob(func() error { return api.SetCapture(false) }, i18n.T("已关闭解密"))
				return
			}
			// Turning it on asks in the window (the 抓包 page), never from
			// the menu alone.
			it.Checked = false
			h.showWindow(OpenCapture)
		}}
}

// commandItems is the 复制终端代理命令 submenu: one entry per shell family.
func (tr *tray) commandItems(port int) []*mygo.MenuItem {
	h := tr.h
	var out []*mygo.MenuItem
	for _, sc := range shellCommands(port) {
		cmd := sc.Cmd
		out = append(out, &mygo.MenuItem{Label: sc.Label, ToolTip: cmd, Click: func(*mygo.MenuItem, *mygo.Window) {
			if err := mygo.Clipboard.Write(transfer.TextData(cmd)); err == nil {
				h.note(i18n.T("已复制"))
			}
		}})
	}
	return out
}

func (tr *tray) modeItems(st model.Status) []*mygo.MenuItem {
	h := tr.h
	var out []*mygo.MenuItem
	for _, m := range []model.Mode{model.ModeRule, model.ModeGlobal, model.ModeDirect} {
		mode := m
		out = append(out, &mygo.MenuItem{Label: env.ModeName(m), Type: mygo.MenuItemRadio, Checked: st.Mode == m,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				h.runJob(func() error { return h.hostAPI().SetMode(mode) }, i18n.Tf("已切换到%s模式", env.ModeName(mode)))
			}})
	}
	return out
}

func (tr *tray) nodeItems(g model.ProxyGroup, px model.ProxySnapshot) []*mygo.MenuItem {
	h := tr.h
	var out []*mygo.MenuItem
	for _, n := range g.All {
		name := n
		label := n
		if p, ok := px.Proxies[n]; ok && p.Delay != 0 {
			label = n + "  " + env.FmtDelay(p.Delay, false)
		}
		out = append(out, &mygo.MenuItem{Label: label, Type: mygo.MenuItemRadio, Checked: g.Now == n, Disabled: g.Type != model.GroupSelect,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				h.runJob(func() error { return h.hostAPI().SelectProxy(g.Name, name) }, i18n.Tf("已切换到 %s", name))
			}})
	}
	return out
}

func (tr *tray) subItems(subs []model.Subscription) []*mygo.MenuItem {
	h := tr.h
	var out []*mygo.MenuItem
	for _, sub := range subs {
		id, name := sub.ID, sub.Name
		out = append(out, &mygo.MenuItem{Label: sub.Name, Type: mygo.MenuItemRadio, Checked: sub.Current,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				h.runJob(func() error { return h.hostAPI().UseSubscription(id) }, i18n.Tf("已切换到 %s", name))
			}})
	}
	return out
}

// ---- title ----

// setTitle shows the text chosen in settings beside the menu bar icon
// (macOS only, docs/design.md §7) while the core runs. Decryption shows as
// a dot on the icon (docs/capture.md §7.2 指示).
func (tr *tray) setTitle(st model.Status) {
	if tr.t == nil || !tr.h.mac() {
		return
	}
	tr.mu.Lock()
	node := tr.node
	tr.mu.Unlock()
	title := trayart.Title(st, node, tr.h.hostAPI().Settings())
	tr.mu.Lock()
	same := title == tr.title
	tr.title = title
	tr.mu.Unlock()
	if !same {
		tr.t.SetTitle(title)
	}
}

// updateGraph refreshes the traffic graph in the icon (macOS, when turned
// on in settings) and redraws the icon if anything it shows changed.
func (tr *tray) updateGraph() {
	api := tr.h.hostAPI()
	var graph []float64
	if tr.h.mac() && api.Settings().TrayGraph {
		up, down := api.TrafficHistory()
		graph = trayart.Graph(up, down)
	}
	tr.mu.Lock()
	tr.look.Graph = graph
	tr.mu.Unlock()
	tr.drawIcon(false)
}

// drawIcon sets the icon for tr.look; force redraws after an appearance
// change.
func (tr *tray) drawIcon(force bool) {
	if tr.t == nil {
		return
	}
	tr.mu.Lock()
	look := tr.look
	tr.mu.Unlock()
	windows := runtime.GOOS == "windows"
	ink := trayart.Ink{Windows: windows, Dark: tr.t.Dark()}
	if windows {
		ink.Accent = mygo.Theme.Preferences().Accent
	}
	key := fmt.Sprintf("%s|%v|%v", look.Key(), ink.Dark, ink.Accent)
	tr.mu.Lock()
	if !force && key == tr.drawn {
		tr.mu.Unlock()
		return
	}
	tr.drawn = key
	tr.mu.Unlock()
	// macOS: template icons (the bar tints them), except the red badge
	template := !windows && look.State != trayart.Error
	_ = tr.t.SetIcon(trayart.PNG(look, ink), template)
}
