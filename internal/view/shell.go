// Package view is the window shell of 澜流 Lanflow: the sidebar with its
// status bar, page routing, toasts, confirmations, service notices, the
// shared component kit (env.Kit) and the tray menu. Pages live in the
// sub-packages and talk to the shell through env.Env.
package view

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/conns"
	"lanflow/internal/view/env"
	"lanflow/internal/view/hotkey"
	"lanflow/internal/view/logs"
	"lanflow/internal/view/netpage"
	"lanflow/internal/view/overview"
	"lanflow/internal/view/probe"
	"lanflow/internal/view/proxies"
	"lanflow/internal/view/rules"
	"lanflow/internal/view/settings"
	"lanflow/internal/view/stats"
	"lanflow/internal/view/subs"
)

// Window sizes in DIPs (docs/design.md §3.3 / §4): the default fits every
// page's primary content without page-level scrolling; the minimum keeps
// the sidebar's status bar and the pages' two-column layouts readable.
const (
	DefaultWidth  = 1180
	DefaultHeight = 780
	MinWidth      = 1000
	MinHeight     = 680
	SidebarWidth  = 200
)

// FitWorkArea returns the default window size clamped to the work area
// (a display without the menu bar, Dock and taskbars) for a first launch,
// leaving a margin around the window; it never goes below the minimum. A
// zero work area (no display information) gives the default.
func FitWorkArea(wa mygo.Rectangle) (width, height int) {
	width, height = DefaultWidth, DefaultHeight
	if wa.Width <= 0 || wa.Height <= 0 {
		return width, height
	}
	const margin = 40
	width = max(min(width, wa.Width-margin), MinWidth)
	height = max(min(height, wa.Height-margin), MinHeight)
	return width, height
}

// Options configure the shell.
type Options struct {
	// Mac selects macOS labels and modifiers.
	Mac bool
	// Sync runs Env.Run jobs on the calling thread, for tests.
	Sync bool
}

// pageView is what every page package exports.
type pageView interface{ View(c *ui.Context) }

// topicPage is a page that names the service topics its view reads
// (Topics). A change of another topic does not rebuild it while it is
// shown; a page without Topics is rebuilt on every change.
type topicPage interface{ Topics() []app.Topic }

// shellTopics are the topics the shell's own parts draw: the sidebar's
// status bar and badge (Status), the queued notices (Notice) and the
// language and appearance (Settings). They rebuild whatever page shows.
var shellTopics = []app.Topic{app.TopicStatus, app.TopicNotice, app.TopicSettings}

// toastReq is a queued toast, shown on the next build.
type toastReq struct {
	msg, detail string
	isError     bool
	action      string
	onAction    func()
}

// confirmReq is a pending confirmation dialog.
type confirmReq struct {
	title, body, ok string
	danger          bool
	onOK            func()
}

// Shell is the state of the main window.
type Shell struct {
	api  app.API
	env  *env.Env
	opts Options

	win        *mygo.Window
	invalidate func() // asks the window for a frame (Window.Invalidate; tests count calls)
	tray       *tray
	off        func()
	quitT      func()

	page  env.Page
	args  map[env.Page]string
	pages map[env.Page]pageView

	// topics are the topics that rebuild each page (nil: all), fixed in
	// New; shown is the page shown, for onChange on other goroutines.
	topics map[env.Page]map[app.Topic]bool
	shown  atomic.Value // env.Page

	mu      sync.Mutex
	pending []func()
	toasts  []toastReq
	notices []app.Notice

	confirm     *confirmReq
	confirmOpen bool
	invalid     *app.Notice
	invalidOpen bool
	recovery    bool
	recoveryHit bool
	hiddenOnce  bool
	hiddenFlag  string // file that records the tray notice was shown (ConfigDir); "" = memory only
	updateStamp string // file that records the last launch-time update check; "" = check every launch

	hkMu   sync.Mutex
	hotkey string // the global shortcut registered with the system; "" = none

	// Frames for data changes are rate-limited (redrawData): the streams
	// tick several times a second, a frame rebuilds the whole page.
	active  atomic.Bool // the window has focus (OnFocus / OnBlur)
	rdMu    sync.Mutex
	rdTimer *time.Timer
	rdLast  time.Time

	detail     *detailReq // 「查看详情」 dialog of an error toast
	detailOpen bool

	// notify shows a system notification; mygo by default, replaced in tests.
	notify func(title, body string, onClick func())
}

// detailReq is the full text behind an error toast's 「查看详情」.
type detailReq struct {
	title, text string
}

// New builds the shell and all pages over api.
func New(api app.API, opts Options) *Shell {
	s := &Shell{api: api, opts: opts, page: env.Overview, args: map[env.Page]string{}}
	s.env = &env.Env{
		API:      api,
		Navigate: s.navigate,
		Arg:      s.takeArg,
		Toast: func(msg string, isError bool) {
			if isError {
				s.errorToast(msg, "")
				return
			}
			s.toast(toastReq{msg: msg})
		},
		ToastDetail: func(msg, detail string, isError bool) {
			if isError {
				s.errorToast(msg, detail)
				return
			}
			s.toast(toastReq{msg: msg, detail: detail})
		},
		Run:     s.run,
		Confirm: s.ask,
		Redraw:  s.Redraw,
		Mac:     opts.Mac,
		Kit:     NewKit(),

		SetGlobalShortcut: s.SetGlobalShortcut,
	}
	if dir := api.ConfigDir(); filepath.IsAbs(dir) {
		s.hiddenFlag = filepath.Join(dir, "tray-notice-shown")
		s.updateStamp = filepath.Join(dir, "update-check")
	}
	s.pages = map[env.Page]pageView{
		env.Overview: overview.New(s.env),
		env.Proxies:  proxies.New(s.env),
		env.Subs:     subs.New(s.env),
		env.Conns:    conns.New(s.env),
		env.Logs:     logs.New(s.env),
		env.Rules:    rules.New(s.env),
		env.Net:      netpage.New(s.env),
		env.Stats:    stats.New(s.env),
		env.Probe:    probe.New(s.env),
		env.Settings: settings.New(s.env),
	}
	s.topics = map[env.Page]map[app.Topic]bool{}
	for id, p := range s.pages {
		tp, ok := p.(topicPage)
		if !ok {
			continue
		}
		set := map[app.Topic]bool{}
		for _, t := range shellTopics {
			set[t] = true
		}
		for _, t := range tp.Topics() {
			set[t] = true
		}
		s.topics[id] = set
	}
	s.shown.Store(s.page)
	s.off = api.OnChange(s.onChange)
	return s
}

// Env returns the environment pages see.
func (s *Shell) Env() *env.Env { return s.env }

// Page returns the page shown.
func (s *Shell) Page() env.Page { return s.page }

// Attach gives the shell its window, for redraws from other goroutines,
// and turns system notifications on (they need the running app).
func (s *Shell) Attach(win *mygo.Window) {
	s.win = win
	s.invalidate = win.Invalidate
	if s.notify == nil {
		s.notify = systemNotify
	}
	// Idle work follows the window: data frames come slower while it is
	// not focused, and the service slows its streams while it is hidden
	// or minimized (app.Service.SetBackground). Nothing draws at all
	// while the window is out of sight; that is MyGo's doing.
	s.active.Store(true)
	win.OnFocus(func() { s.active.Store(true) })
	win.OnBlur(func() { s.active.Store(false) })
	win.OnHide(func() { s.setBackground(true) })
	win.OnMinimize(func() { s.setBackground(true) })
	win.OnShow(func() { s.setBackground(false) })
	win.OnRestore(func() { s.setBackground(false) })
}

// setBackground passes the window's visibility to a service that adapts
// to it (the real one does, the fake does not), and tells the pages that
// hold caches for drawing (a page's Hidden method) when it goes out of
// sight, so that they free them until the window shows again.
func (s *Shell) setBackground(hidden bool) {
	if b, ok := s.api.(interface{ SetBackground(bool) }); ok {
		b.SetBackground(hidden)
	}
	if !hidden {
		return
	}
	for _, p := range s.pages {
		if h, ok := p.(interface{ Hidden() }); ok {
			h.Hidden()
		}
	}
}

// Close unregisters the shell from the API.
func (s *Shell) Close() {
	if s.off != nil {
		s.off()
	}
	s.rdMu.Lock()
	if s.rdTimer != nil {
		s.rdTimer.Stop()
		s.rdTimer = nil
	}
	s.rdMu.Unlock()
	if s.tray != nil {
		s.tray.destroy()
	}
}

// Redraw asks for a new frame; safe from any goroutine.
func (s *Shell) Redraw() {
	if s.invalidate != nil {
		s.invalidate()
	}
}

// Data frames come at most every redrawActive while the window is
// focused and every redrawIdle while it is not: the traffic, connection,
// log and status streams each tick about once a second, out of step, and
// each frame builds the whole page. The first change after a quiet spell
// draws at once; the rest of the window waits for the next slot.
const (
	redrawActive = 250 * time.Millisecond
	redrawIdle   = time.Second
)

// redrawData asks for a frame for a change of the service's data, rate
// limited; safe from any goroutine.
func (s *Shell) redrawData() {
	if s.invalidate == nil {
		return
	}
	gap := redrawActive
	if !s.active.Load() {
		gap = redrawIdle
	}
	s.rdMu.Lock()
	defer s.rdMu.Unlock()
	if s.rdTimer != nil {
		return // a frame is already coming
	}
	now := time.Now()
	if wait := gap - now.Sub(s.rdLast); wait > 0 {
		s.rdTimer = time.AfterFunc(wait, func() {
			s.rdMu.Lock()
			s.rdTimer = nil
			s.rdLast = time.Now()
			s.rdMu.Unlock()
			s.Redraw()
		})
		return
	}
	s.rdLast = now
	s.Redraw()
}

// onChange is called by the API from any goroutine.
func (s *Shell) onChange(t app.Topic) {
	switch t {
	case app.TopicNotice:
		ns := s.api.TakeNotices()
		if len(ns) > 0 {
			s.mu.Lock()
			s.notices = append(s.notices, ns...)
			s.mu.Unlock()
		}
		for _, n := range ns {
			if n.System {
				// Core crashes and failed auto-updates reach the user while
				// the window is hidden (docs/design.md §9); the toast above
				// still shows when the window is visible.
				s.systemNotice(n)
			}
		}
	case app.TopicSettings:
		set := s.api.Settings()
		if lang := i18n.Normalize(set.Language); lang != i18n.Lang() {
			// The service sets the language itself; a backend that does
			// not (the fake) is followed here. Pages say the new language
			// on the next frame, the tray rebuilds below.
			i18n.SetLang(lang)
		}
		if s.win != nil { // mygo's theme needs the running app; tests have none
			ApplyAppearance(set.Appearance)
			ApplyLanguage(set.Language)
			s.win.SetTitle(i18n.T("澜流 Lanflow"))
			ApplyOpenAtLogin(set.OpenAtLogin)
			// Settings changed behind the settings page (an import, another
			// window): follow the stored shortcut.
			s.hkMu.Lock()
			cur := s.hotkey
			s.hkMu.Unlock()
			if set.GlobalShortcut != cur {
				if err := s.SetGlobalShortcut(set.GlobalShortcut); err != nil {
					log.Printf("lanflow: global shortcut: %v", err)
				}
			}
		}
	}
	if s.tray != nil {
		s.tray.changed(t)
	}
	if s.wants(t) {
		s.redrawData()
	}
}

// wants reports whether a change of t rebuilds the page shown: the
// shell's own topics always do, the rest only when the page reads them.
func (s *Shell) wants(t app.Topic) bool {
	page, _ := s.shown.Load().(env.Page)
	set, ok := s.topics[page]
	return !ok || set[t]
}

// post runs fn on the UI thread before the next build and redraws.
func (s *Shell) post(fn func()) {
	s.mu.Lock()
	s.pending = append(s.pending, fn)
	s.mu.Unlock()
	s.Redraw()
}

func (s *Shell) toast(t toastReq) {
	s.mu.Lock()
	s.toasts = append(s.toasts, t)
	s.mu.Unlock()
	s.Redraw()
}

// run is Env.Run: the job off the UI thread, its outcome as a toast.
func (s *Shell) run(job func() error, okMsg string) {
	do := func() {
		err := job()
		if err != nil {
			s.errorToast(err.Error(), "")
		} else if okMsg != "" {
			s.toast(toastReq{msg: okMsg})
		}
		s.Redraw()
	}
	if s.opts.Sync {
		do()
		return
	}
	go do()
}

// ask is Env.Confirm.
func (s *Shell) ask(title, body, okLabel string, danger bool, onOK func()) {
	s.confirm = &confirmReq{title: title, body: body, ok: okLabel, danger: danger, onOK: onOK}
	s.confirmOpen = true
	s.Redraw()
}

func (s *Shell) navigate(p env.Page, arg string) {
	if _, ok := s.pages[p]; !ok {
		return
	}
	if p != s.page {
		// The page left frees what it keeps for drawing (Hidden).
		if h, ok := s.pages[s.page].(interface{ Hidden() }); ok {
			h.Hidden()
		}
	}
	s.page = p
	s.shown.Store(p)
	if arg != "" {
		s.args[p] = arg
	}
	s.Redraw()
}

func (s *Shell) takeArg(p env.Page) string {
	a := s.args[p]
	delete(s.args, p)
	return a
}

// ShowWindow shows and focuses the main window.
func (s *Shell) ShowWindow() {
	if s.win == nil {
		return
	}
	s.win.Show()
	s.win.Restore()
	s.win.Focus()
}

// NeedsQuitConfirm reports whether quitting should ask first: the system
// proxy is still on (docs/design.md §6.4).
func (s *Shell) NeedsQuitConfirm() bool {
	return s.api.Status().SystemProxy
}

// AskQuit shows the quit confirmation; quit runs on the UI thread if the
// user agrees.
func (s *Shell) AskQuit(quit func()) {
	s.ask(i18n.T("退出后将关闭系统代理。"), i18n.T("由澜流开启的系统代理会恢复为关闭，内核也会停止。"), i18n.T("退出"), false, quit)
}

// HiddenToTray tells the user once ever that the app keeps running in the
// tray (docs/design.md §9 「首次隐藏时显示一次说明」). A marker file in the
// data directory remembers it across launches.
func (s *Shell) HiddenToTray() {
	if s.hiddenOnce {
		return
	}
	s.hiddenOnce = true
	if s.hiddenFlag != "" {
		if _, err := os.Stat(s.hiddenFlag); err == nil {
			return // shown in an earlier run
		}
		if err := os.WriteFile(s.hiddenFlag, []byte("1\n"), 0o600); err != nil {
			log.Printf("lanflow: tray notice marker: %v", err)
		}
	}
	where := i18n.T("通知区域")
	if s.opts.Mac {
		where = i18n.T("菜单栏")
	}
	if s.notify != nil {
		s.notify(i18n.Tf("澜流仍在%s运行", where), i18n.Tf("点击%s图标可以打开窗口或退出澜流。", where), nil)
	}
}

// systemNotify shows a system notification through mygo; a click brings
// the window up. Notifications need the running, packaged app, so failures
// are only logged.
func systemNotify(title, body string, onClick func()) {
	n := mygo.NewNotification(mygo.NotificationOptions{Title: title, Body: body})
	n.OnClick(func() {
		if onClick != nil {
			onClick()
		}
		n.Close()
	})
	go func() {
		if err := n.Show(); err != nil {
			log.Printf("lanflow: notification %q: %v", title, err)
		}
	}()
}

// systemNotice turns a service notice into a system notification.
func (s *Shell) systemNotice(n app.Notice) {
	if s.notify == nil {
		return // before Attach (tests have no app)
	}
	title := firstNonEmpty(n.Title, n.Detail)
	body := n.Detail
	if title == body {
		body = ""
	}
	s.notify(title, body, func() {
		s.ShowWindow()
		if n.Kind == "core-crash" {
			s.post(func() { s.navigate(env.Logs, "") })
		}
	})
}

// RegisterGlobalShortcut binds the system-proxy hotkey of the settings
// app-wide (docs/design.md §9, default ⌘⇧S / Ctrl+Shift+S, 可修改); call it
// once the app is ready. Registration fails when another app holds the keys.
func (s *Shell) RegisterGlobalShortcut() error {
	return s.SetGlobalShortcut(s.api.Settings().GlobalShortcut)
}

// SetGlobalShortcut is Env.SetGlobalShortcut: it replaces the registered
// shortcut with acc ("" = none). When the system refuses acc the previous
// shortcut is registered again and the error says so in Chinese. Before
// Attach (tests, no running app) it only remembers acc.
func (s *Shell) SetGlobalShortcut(acc string) error {
	// The lock only guards the field: mygo's calls wait for the main
	// thread, which may itself be calling here.
	s.hkMu.Lock()
	prev := s.hotkey
	s.hkMu.Unlock()
	set := func(v string) {
		s.hkMu.Lock()
		s.hotkey = v
		s.hkMu.Unlock()
	}
	if s.win == nil {
		set(acc)
		return nil
	}
	if acc == prev && (acc == "" || mygo.GlobalShortcut.IsRegistered(acc)) {
		return nil
	}
	if prev != "" {
		mygo.GlobalShortcut.Unregister(prev)
	}
	set("")
	if acc == "" {
		return nil
	}
	if err := mygo.GlobalShortcut.Register(acc, s.ToggleSystemProxy); err != nil {
		log.Printf("lanflow: global shortcut %q: %v", acc, err)
		kept := i18n.T("全局快捷键保持关闭。")
		if prev != "" && mygo.GlobalShortcut.Register(prev, s.ToggleSystemProxy) == nil {
			set(prev)
			kept = i18n.Tf("仍使用原来的快捷键 %s。", hotkey.Display(prev, s.opts.Mac))
		}
		return fmt.Errorf(i18n.T("无法注册快捷键 %s：可能已被其他应用占用。%s可以换一个组合键再试。"), hotkey.Display(acc, s.opts.Mac), kept)
	}
	set(acc)
	return nil
}

// CheckUpdatesAtLaunch checks for a new version of the app once a day,
// when the build can update itself and the setting allows (docs/design.md
// §9 应用自动更新). A found version is announced with a toast that opens
// the settings; the check itself runs off the UI thread.
func (s *Shell) CheckUpdatesAtLaunch() {
	if s.api.Settings().DisableUpdateCheck {
		return
	}
	page, ok := s.pages[env.Settings].(*settings.Page)
	if !ok || !page.UpdatesEnabled() || !s.updateCheckDue(time.Now()) {
		return
	}
	page.CheckAppUpdate(true, func(version string) {
		s.toast(toastReq{msg: i18n.Tf("澜流有新版本 %s", version), detail: i18n.T("在「设置 › 常规 › 关于」中下载并重启。"), action: i18n.T("前往设置"),
			onAction: func() { s.post(func() { s.navigate(env.Settings, "general") }) }})
	})
}

// updateCheckInterval is how often the launch-time update check runs.
const updateCheckInterval = 24 * time.Hour

// updateCheckDue reports whether the daily check is due at now, and
// records now as the last check when it is. Without a stamp file every
// launch checks.
func (s *Shell) updateCheckDue(now time.Time) bool {
	if s.updateStamp == "" {
		return true
	}
	if data, err := os.ReadFile(s.updateStamp); err == nil {
		if last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); err == nil {
			if d := now.Sub(last); d >= 0 && d < updateCheckInterval {
				return false
			}
		}
	}
	if err := os.WriteFile(s.updateStamp, []byte(now.Format(time.RFC3339)+"\n"), 0o600); err != nil {
		log.Printf("lanflow: update check marker: %v", err)
	}
	return true
}

// ToggleSystemProxy flips the system proxy (tray, hotkey).
func (s *Shell) ToggleSystemProxy() {
	st := s.api.Status()
	on := !(st.Core == model.CoreRunning && st.SystemProxy)
	msg := i18n.T("系统代理已关闭")
	if on {
		msg = i18n.T("系统代理已开启")
	}
	s.run(func() error { return s.api.SetSystemProxy(on) }, msg)
}

// OpenImportLink shows the window and opens the import-from-link dialog
// with rawURL prefilled; nothing is imported until the user confirms.
// It accepts the app's own deep links and the Clash convention:
//
//	lanflow://import?url=<subscription>&name=<name>
//	clash://install-config?url=<subscription>&name=<name>
//
// Anything else is ignored. It is safe from any goroutine.
func (s *Shell) OpenImportLink(rawURL string) {
	target, name, ok := ParseImportLink(rawURL)
	if !ok {
		log.Printf("lanflow: ignoring link %q", rawURL)
		return
	}
	s.post(func() { s.navigate(env.Subs, env.ImportURLArg(target, name)) })
	s.ShowWindow()
}

// ParseImportLink extracts the subscription URL (http/https) and optional
// name from a lanflow:// or clash:// link.
func ParseImportLink(rawURL string) (target, name string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "lanflow":
		if h := strings.ToLower(u.Host + u.Path); h != "import" && h != "import/" && h != "install-config" && h != "install-config/" {
			return "", "", false
		}
	case "clash", "clashmeta", "mihomo":
		if h := strings.ToLower(u.Host + u.Path); h != "install-config" && h != "install-config/" {
			return "", "", false
		}
	default:
		return "", "", false
	}
	q := u.Query()
	target = strings.TrimSpace(q.Get("url"))
	t, err := url.Parse(target)
	if err != nil || (t.Scheme != "http" && t.Scheme != "https") || t.Host == "" {
		return "", "", false
	}
	return target, strings.TrimSpace(q.Get("name")), true
}

// errorToast shows an error toast with a short first line and 「查看详情」
// opening the full text (docs/design.md §6.5).
func (s *Shell) errorToast(msg, detail string) {
	short, full := splitError(msg)
	if detail != "" {
		full = strings.TrimSpace(msg + "\n\n" + detail)
	}
	s.toast(toastReq{msg: short, isError: true, action: i18n.T("查看详情"), onAction: func() { s.showDetail(full) }})
}

// splitError returns the toast line of an error message and the full text:
// the first line, cut at the first sentence end once it gets long.
func splitError(msg string) (short, full string) {
	full = strings.TrimSpace(msg)
	short = full
	if i := strings.IndexAny(short, "\n\r"); i >= 0 {
		short = strings.TrimSpace(short[:i])
	}
	const limit = 64
	if r := []rune(short); len(r) > limit {
		cut := -1
		for i, c := range r[:limit] {
			if i >= 16 && (c == '。' || c == '：' || c == '，' || c == '；' || c == '.' || c == ':') {
				cut = i
			}
		}
		if cut > 0 {
			short = string(r[:cut]) + "…"
		} else {
			short = string(r[:limit]) + "…"
		}
	}
	return short, full
}

// showDetail opens the detail dialog; safe from any goroutine.
func (s *Shell) showDetail(text string) {
	s.post(func() {
		s.detail = &detailReq{title: i18n.T("错误详情"), text: text}
		s.detailOpen = true
	})
}

// ApplyAppearance applies the appearance setting ("system", "light", "dark").
func ApplyAppearance(appearance string) {
	switch appearance {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

// ApplyLanguage makes MyGo's own strings (its controls, menus and dialogs)
// follow the language setting, at once and for the next launches; the
// app's own texts follow i18n.Lang.
func ApplyLanguage(lang string) {
	if tag := i18n.Tag(lang); mygo.App.Locale() != tag {
		mygo.App.SetLocale(tag)
	}
}

// ApplyOpenAtLogin registers or removes the login item when it differs
// from the system's current state (macOS login item / Windows Run entry).
func ApplyOpenAtLogin(open bool) {
	if mygo.App.OpenAtLogin() == open {
		return
	}
	if err := mygo.App.SetOpenAtLogin(open); err != nil {
		log.Printf("lanflow: open at login: %v", err)
	}
}

// ---- the view ----

// View draws the whole window.
func (s *Shell) View(c *ui.Context) {
	t := c.Theme()
	s.drain(c)
	st := s.api.Status()
	vib := c.Vibrancy()
	if vib {
		c.Root().Background(ui.Transparent)
	}
	bar := c.TitleBar()
	root := ui.Row(c).Fill().AlignItems(ui.Stretch)
	root.Children(func() {
		side := ui.Column(c).Width(SidebarWidth).Shrink(0)
		if !vib {
			side.Background(t.Surface)
		}
		side.Children(func() {
			// The strip around the window controls drags the window, as a
			// title bar does (double-click zooms or minimizes per the system).
			strip := ui.Box(c).Height(10).Shrink(0)
			if bar.Height > 0 {
				strip.Height(max(bar.Height, 52)).DragWindow().Label(i18n.T("标题栏"))
			}
			s.sidebar(c, st, vib)
			s.statusBar(c, st)
		})
		ui.Box(c).Width(1).Background(t.Border).Shrink(0)
		ui.Column(c).Grow(1).Background(t.Background).Clip().Children(func() {
			if p := s.pages[s.page]; p != nil {
				p.View(c)
			}
		})
	})
	if files := root.DroppedFiles(); len(files) > 0 {
		s.importFiles(files)
	}
	if root.FileDragOver() {
		s.dropOverlay(c)
	}
	s.shortcuts(c)
	s.dialogs(c, st)
}

// drain runs what other goroutines queued: pending functions, toasts and
// notices.
func (s *Shell) drain(c *ui.Context) {
	s.mu.Lock()
	pending, toasts, notices := s.pending, s.toasts, s.notices
	s.pending, s.toasts, s.notices = nil, nil, nil
	s.mu.Unlock()
	for _, fn := range pending {
		fn()
	}
	for _, n := range notices {
		s.handleNotice(c, n)
	}
	for _, tr := range toasts {
		s.showToast(c, tr)
	}
	if !s.recoveryHit {
		s.recoveryHit = true
		if s.api.Status().PendingRecovery {
			s.recovery = true
		}
	}
}

func (s *Shell) showToast(c *ui.Context, tr toastReq) {
	tt := ui.Toast{Title: tr.msg, Description: tr.detail, Timeout: 3 * time.Second, Action: tr.action, OnAction: tr.onAction}
	if tr.isError {
		tt.Type, tt.Timeout = "error", 5*time.Second
	}
	c.AddToast(tt)
}

func (s *Shell) handleNotice(c *ui.Context, n app.Notice) {
	switch n.Kind {
	case "core-crash":
		s.showToast(c, toastReq{msg: firstNonEmpty(n.Title, i18n.T("Mihomo 内核意外退出")), detail: n.Detail, isError: true,
			action: i18n.T("查看日志"), onAction: func() { s.navigate(env.Logs, "") }})
	case "invalid-config":
		nn := n
		s.invalid, s.invalidOpen = &nn, true
	case "recovery":
		s.recovery = true
	case "error":
		s.errorToast(firstNonEmpty(n.Title, n.Detail), detailIf(n))
	default:
		s.showToast(c, toastReq{msg: firstNonEmpty(n.Title, n.Detail), detail: detailIf(n)})
	}
}

func detailIf(n app.Notice) string {
	if n.Title == "" {
		return ""
	}
	return n.Detail
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// sidebarGroups are the sidebar's groups of destinations (docs/design.md
// §4): daily use, what the core is doing, and diagnosis; 设置 sits apart
// at the bottom.
var sidebarGroups = [][]env.Page{
	{env.Overview, env.Proxies, env.Subs},
	{env.Conns, env.Logs, env.Rules},
	{env.Net, env.Stats, env.Probe},
}

// sidebar draws the groups of destinations.
func (s *Shell) sidebar(c *ui.Context, st model.Status, vib bool) {
	t := c.Theme()
	page := string(s.page)
	item := func(p env.Page) ui.Element {
		e := ui.SidebarItem(c, string(p), icons[pageIcons[string(p)]], env.Title(p))
		if p == env.Conns && st.Connections > 0 && st.Core == model.CoreRunning {
			e.Children(func() {
				ui.Badge(c, fmt.Sprint(st.Connections)).FontFeatures("tnum")
			})
		}
		return e
	}
	divider := func() {
		ui.Box(c).Height(1).Background(t.Border).Margin(6, 8).Shrink(0)
	}
	main := ui.Sidebar(c, &page, func() {
		for i, group := range sidebarGroups {
			if i > 0 {
				divider()
			}
			for _, p := range group {
				item(p)
			}
		}
	}).Grow(1).Label(i18n.T("页面"))
	if vib {
		main.Background(ui.Transparent)
	}
	if main.Changed() {
		s.navigate(env.Page(page), "")
	}
	divider()
	bottom := ui.Sidebar(c, &page, func() { item(env.Settings) }).Shrink(0).Label(i18n.T("更多"))
	if vib {
		bottom.Background(ui.Transparent)
	}
	if bottom.Changed() {
		s.navigate(env.Page(page), "")
	}
}

// statusBar is the sidebar's bottom status (docs/design.md §4 table).
func (s *Shell) statusBar(c *ui.Context, st model.Status) {
	t := c.Theme()
	k := s.env.Kit
	bar := ui.Column(c).Padding(10, 18, 14, 18).Gap(3).Shrink(0).Cursor(ui.CursorPointer).
		Label(i18n.T("状态：回到概览")).Role(ui.RoleButton)
	bar.Children(func() {
		ui.Box(c).Height(1).Background(t.Border).Margin(0, -8, 8, -8)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			switch st.Core {
			case model.CoreRunning:
				k.Dot(c, "green")
				ui.Text(c, i18n.T("运行中 · ")+modeName(st.Mode)).FontSize(12).SingleLine()
			case model.CoreStarting:
				ui.Spinner(c).Size(12, 12).Label(i18n.T("启动中"))
				ui.Text(c, i18n.T("启动中…")).FontSize(12)
			case model.CoreError:
				k.Dot(c, "red")
				ui.Text(c, i18n.T("内核异常")).FontSize(12).TextColor(t.Danger)
			case model.CoreMissing:
				k.Dot(c, "red")
				ui.Text(c, i18n.T("未找到内核")).FontSize(12).TextColor(t.Danger)
			default:
				k.Dot(c, "gray")
				ui.Text(c, i18n.T("未启动")).FontSize(12).TextColor(t.TextMuted)
			}
		})
		if st.Core == model.CoreRunning {
			if st.SystemProxy {
				ui.Text(c, i18n.T("系统代理已开启")).FontSize(11).TextColor(t.TextMuted).Margin(0, 0, 0, 14)
			}
			// The active core, only when the version fits the 200 pt sidebar
			// on one line (release tags and Alpha hashes do; a long custom
			// build string would be cut, so it is left out).
			if v := st.CoreVersion; v != "" && len(v) <= 20 {
				ui.Text(c, "Mihomo "+v).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine().Margin(0, 0, 0, 14)
			}
			ui.Text(c, "↑ "+env.FmtSpeed(st.Up)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").Margin(0, 0, 0, 14)
			ui.Text(c, "↓ "+env.FmtSpeed(st.Down)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").Margin(0, 0, 0, 14)
		}
	})
	if bar.Clicked() {
		s.navigate(env.Overview, "")
	}
}

func modeName(m model.Mode) string {
	switch m {
	case model.ModeGlobal:
		return i18n.T("全局")
	case model.ModeDirect:
		return i18n.T("直连")
	default:
		return i18n.T("规则")
	}
}

// shortcuts are ⌘1–⌘7 for the first seven pages and ⌘, for settings.
func (s *Shell) shortcuts(c *ui.Context) {
	for i, p := range env.Order[:7] {
		if c.Shortcut(ui.Cmd, ui.Key1+ui.Key(i)) {
			s.navigate(p, "")
		}
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		s.navigate(env.Settings, "")
	}
}

// dropOverlay highlights the window while YAML files are dragged over it.
func (s *Shell) dropOverlay(c *ui.Context) {
	t := c.Theme()
	ui.Overlay(c, func() {
		ui.Column(c).Absolute().Left(12).Top(12).Right(12).Bottom(12).Center().Gap(6).Radius(12).
			Border(2, t.Accent).BorderStyle(ui.BorderDashed).Background(t.Background.Alpha(0.88)).PassThrough().Children(func() {
			ui.Box(c).TextColor(t.Accent).Children(func() { s.env.Kit.Icon(c, "down", 28) })
			ui.Text(c, i18n.T("松开以导入")).FontSize(15).FontWeight(600)
			ui.Text(c, i18n.T("支持 .yaml / .yml 文件")).FontSize(12).TextColor(t.TextMuted)
		})
	})
}

// importFiles imports dropped YAML files as subscriptions.
func (s *Shell) importFiles(paths []string) {
	var yaml []string
	for _, p := range paths {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".yaml", ".yml":
			yaml = append(yaml, p)
		}
	}
	if len(yaml) == 0 {
		s.toast(toastReq{msg: i18n.T("只能导入 .yaml / .yml 配置文件"), isError: true})
		return
	}
	// Without a subscription yet, the first dropped file becomes the
	// current one and starts the core, like the other import paths
	// (docs/design.md §8.1: Toast「已导入并启动」).
	none := !s.api.Status().HasSubscriptions
	s.navigate(env.Subs, "")
	for i, p := range yaml {
		path, first := p, none && i == 0
		s.run(func() error { return subs.ImportFile(s.api, path, first) }, subs.ImportToast(path, first))
	}
}

// dialogs draws the confirmation, the invalid-config alert and the
// recovery dialog.
func (s *Shell) dialogs(c *ui.Context, st model.Status) {
	t := c.Theme()
	k := s.env.Kit
	if s.confirm != nil {
		cf := s.confirm
		if ui.AlertDialog(c, &s.confirmOpen, cf.title, cf.body, i18n.T("取消"), cf.ok) == 1 {
			s.confirm = nil
			if cf.onOK != nil {
				cf.onOK()
			}
		} else if !s.confirmOpen {
			s.confirm = nil
		}
	}
	if s.detail != nil {
		d := s.detail
		ui.Modal(c, &s.detailOpen, func() {
			ui.Column(c).Width(460).Gap(12).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).TextColor(t.Danger).Children(func() { k.Icon(c, "warn", 18) })
					ui.Text(c, d.title).FontSize(15).FontWeight(600)
				})
				ui.Scroll(c).MaxHeight(260).Children(func() {
					ui.Text(c, d.text).FontSize(13).Padding(8, 10).Radius(6).
						Background(t.Surface).Border(1, t.Border).Selectable()
				})
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, i18n.T("复制")).Clicked() {
						c.WriteClipboard(d.text)
						c.Toast(i18n.T("已复制"))
					}
					if ui.PrimaryButton(c, i18n.T("好")).Clicked() {
						s.detailOpen = false
					}
				})
			})
		})
		if !s.detailOpen {
			s.detail = nil
		}
	}
	if s.invalid != nil {
		n := s.invalid
		ui.Modal(c, &s.invalidOpen, func() {
			ui.Column(c).Width(440).Gap(12).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).TextColor(t.Warning).Children(func() { k.Icon(c, "warn", 18) })
					ui.Text(c, firstNonEmpty(n.Title, i18n.T("配置无法使用"))).FontSize(15).FontWeight(600)
				})
				ui.Text(c, s.invalidBody(n)).FontSize(13)
				if n.Detail != "" {
					ui.Text(c, n.Detail).Font("monospace").FontSize(12).Padding(8, 10).Radius(6).
						Background(t.Surface).Border(1, t.Border).Selectable()
				}
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, i18n.T("复制错误")).Clicked() {
						c.WriteClipboard(n.Detail)
						c.Toast(i18n.T("已复制"))
					}
					if ui.PrimaryButton(c, i18n.T("好")).Clicked() {
						s.invalidOpen = false
					}
				})
			})
		})
		if !s.invalidOpen {
			s.invalid = nil
		}
	}
	if s.recovery {
		ui.Modal(c, &s.recovery, func() {
			ui.Column(c).Width(440).Gap(12).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).TextColor(t.Warning).Children(func() { k.Icon(c, "warn", 18) })
					ui.Text(c, i18n.T("系统代理未恢复")).FontSize(15).FontWeight(600)
				})
				port := st.MixedPort
				if port == 0 {
					port = 7890
				}
				ui.Text(c, fmt.Sprintf(i18n.T("上次澜流未正常退出，系统代理仍指向 127.0.0.1:%d。如果不处理，部分应用可能无法联网。"), port)).FontSize(13)
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, i18n.T("保持开启并启动内核")).Clicked() {
						s.recovery = false
						s.run(func() error { return s.api.ResolveRecovery(false) }, "")
					}
					if ui.PrimaryButton(c, i18n.T("恢复为关闭")).Clicked() {
						s.recovery = false
						s.run(func() error { return s.api.ResolveRecovery(true) }, i18n.T("系统代理已关闭"))
					}
				})
			})
		})
	}
}

// invalidBody words the invalid-config message with the subscription names.
func (s *Shell) invalidBody(n *app.Notice) string {
	var bad, cur string
	for _, sub := range s.api.Subscriptions() {
		if sub.ID == n.Subject {
			bad = sub.Name
		}
		if sub.Current {
			cur = sub.Name
		}
	}
	switch {
	case bad != "" && bad == cur:
		// An update of the current subscription failed validation.
		return fmt.Sprintf(i18n.T("「%s」更新后的配置未通过校验，已继续使用上次的配置。"), bad)
	case bad != "" && cur != "":
		return fmt.Sprintf(i18n.T("「%s」的配置未通过校验，已继续使用「%s」。"), bad, cur)
	case bad != "":
		return fmt.Sprintf(i18n.T("「%s」的配置未通过校验，当前配置保持不变。"), bad)
	default:
		return i18n.T("配置未通过校验，当前配置保持不变。")
	}
}
