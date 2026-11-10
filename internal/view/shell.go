// Package view is the window shell of 澜流 Lanflow: the sidebar with its
// status bar, page routing, toasts, confirmations, service notices, the
// shared component kit (env.Kit) and the tray menu. Pages live in the
// sub-packages and talk to the shell through env.Env.
package view

import (
	"log"
	"os"
	"path/filepath"
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
	// Remote: the window runs in its own process and the service process
	// holds the tray, the global shortcuts, the login item and the system
	// notifications (docs/service-split.md S2). SetShortcut registers a
	// global shortcut there.
	Remote      bool
	SetShortcut func(action, acc string) error
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
	consents    []*consentReq // agents waiting for the user (agents.go)
	consentOpen bool
	fb          feedbackDialog // 反馈问题 and the crash prompt (feedback.go)
	hiddenOnce  bool
	hiddenFlag  string // file that records the tray notice was shown (ConfigDir); "" = memory only
	updateStamp string // file that records the last launch-time update check; "" = check every launch

	keys        *keys  // global shortcuts registered by this process (keys.go)
	closeWindow func() // Remote: close the window, ending its process (SetCloseWindow)

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

// New builds the shell and all pages over api.
func New(api app.API, opts Options) *Shell {
	s := &Shell{api: api, opts: opts, page: env.Overview, args: map[env.Page]string{}}
	s.keys = newKeys(s)
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
		OpenFeedback:      func() { s.OpenFeedback("") },
	}
	if !opts.Sync {
		// Tests (Sync) save at once; the app saves half a second after
		// the last change, through one writer.
		s.env.UIState = env.NewUIStateSaver(api, uiStateDelay)
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

// uiStateDelay is how long the UI state waits for more changes before
// it is saved.
const uiStateDelay = 500 * time.Millisecond

// Close unregisters the shell from the API and saves what the pages
// still had to remember.
func (s *Shell) Close() {
	if s.off != nil {
		s.off()
	}
	if s.env.UIState != nil {
		s.env.UIState.Close()
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
			if n.System && !s.opts.Remote { // the service process notifies itself
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
		}
		if s.win != nil && !s.opts.Remote {
			ApplyOpenAtLogin(set.OpenAtLogin)
			// Settings changed behind the settings page (an import, another
			// window): follow the stored shortcut.
			if set.GlobalShortcut != s.keys.get(model.ShortcutSystemProxy) {
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

// SetCloseWindow gives the shell the way to close the window (Remote:
// the window shortcut closes it, which ends the window's process).
func (s *Shell) SetCloseWindow(fn func()) { s.closeWindow = fn }

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
	case "crash-report":
		s.crashNotice()
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
