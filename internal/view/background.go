package view

import (
	"log"
	"sync"

	"github.com/egoist/mygo"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Background is the part of Lanflow that runs without a window, in the
// service process (docs/service-split.md S2): the tray, the global
// shortcuts, the system notifications and the agents' consent requests.
// The window, when open, is another process; Background reaches it
// through BackgroundOptions.
type Background struct {
	api app.API
	o   BackgroundOptions

	tray *tray
	keys *keys
	off  func()

	mu       sync.Mutex
	started  bool
	consents []*bgConsent

	// notify shows a system notification; mygo by default, replaced in tests.
	notify func(title, body string, onClick func())
	// ask shows a dialog with buttons and returns the one chosen (-1:
	// none); mygo's message box by default, replaced in tests.
	ask func(title, body string, buttons []string, cancel int) int
}

// BackgroundOptions connect Background to the service process.
type BackgroundOptions struct {
	Mac bool
	// ShowWindow brings up the window process (starting it when needed);
	// arg is what it should open (OpenFeedback, OpenTUN, OpenLogs, a link).
	ShowWindow func(arg string)
	// ToggleWindow is the window shortcut: close the window when it is in
	// front, else show it.
	ToggleWindow func()
	// Quit is the tray's 退出澜流.
	Quit func()
	// Settings is called on start and after every settings change (the
	// login item follows them).
	Settings func(model.Settings)
}

type bgConsent struct {
	AgentConsent
	answer func(string)
	done   bool
}

// NewBackground prepares the windowless part over api; Start shows it.
func NewBackground(api app.API, o BackgroundOptions) *Background {
	b := &Background{api: api, o: o, notify: systemNotify, ask: messageBox}
	b.keys = newKeys(b)
	return b
}

// Start creates the tray, registers the shortcuts and follows the
// settings; call it on the main thread once the app is ready.
func (b *Background) Start() {
	b.mu.Lock()
	b.started = true
	b.mu.Unlock()
	set := b.api.Settings()
	b.applySettings(set)
	if tr, err := startTray(b); err != nil {
		log.Printf("lanflow: tray: %v", err)
	} else {
		b.mu.Lock()
		b.tray = tr
		b.mu.Unlock()
	}
	for action, err := range b.keys.sync(set, true) {
		log.Printf("lanflow: shortcut %s: %v", action, err)
	}
	b.off = b.api.OnChange(b.onChange)
}

// Close removes the tray and stops following the service.
func (b *Background) Close() {
	if b.off != nil {
		b.off()
	}
	b.mu.Lock()
	tr := b.tray
	b.tray = nil
	b.mu.Unlock()
	if tr != nil {
		tr.destroy()
	}
}

func (b *Background) onChange(t app.Topic) {
	if t == app.TopicSettings {
		set := b.api.Settings()
		b.applySettings(set)
		for action, err := range b.keys.sync(set, true) {
			log.Printf("lanflow: shortcut %s: %v", action, err)
		}
	}
	b.mu.Lock()
	tr := b.tray
	b.mu.Unlock()
	if tr != nil {
		tr.changed(t)
	}
}

func (b *Background) applySettings(set model.Settings) {
	if lang := i18n.Normalize(set.Language); lang != i18n.Lang() {
		i18n.SetLang(lang)
	}
	if b.live() {
		ApplyLanguage(set.Language) // mygo's own strings: the dialogs' buttons
	}
	if b.o.Settings != nil {
		b.o.Settings(set)
	}
}

// SetShortcut registers a global shortcut for the window's settings page
// (rpc.Host).
func (b *Background) SetShortcut(action, acc string) error { return b.keys.set(action, acc) }

// SystemNotice shows a service notice the user should see while no
// window is open (app.Notice.System) as a system notification; a click
// opens the window, on the 日志 page after a core crash.
func (b *Background) SystemNotice(n app.Notice) {
	if !n.System || b.notify == nil {
		return
	}
	title := firstNonEmpty(n.Title, n.Detail)
	body := n.Detail
	if title == body {
		body = ""
	}
	arg := ""
	if n.Kind == "core-crash" {
		arg = OpenLogs
	}
	b.notify(title, body, func() { b.showWindow(arg) })
}

// ---- agents' consent (docs/service-split.md §4) ----

// AskAgentConsent asks the user with a system notification and an item in
// the tray menu, either of which opens a dialog with the answers. answer
// is called once with "once", "always" or "deny"; the returned function
// withdraws the request (the caller stopped waiting) without an answer.
func (b *Background) AskAgentConsent(req AgentConsent, answer func(string)) (withdraw func()) {
	r := &bgConsent{AgentConsent: req, answer: answer}
	b.mu.Lock()
	b.consents = append(b.consents, r)
	b.mu.Unlock()
	go b.refreshTray() // the menu is set on the main thread, which may be busy
	if b.notify != nil {
		b.notify(i18n.T("有 agent 请求授权"), consentTitle(req), func() { go b.decide(r) })
	}
	return func() { b.finish(r, "") }
}

// Pending is the number of consent requests waiting for an answer.
func (b *Background) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.consents)
}

// finish answers r once (a == "": withdrawn) and drops it from the menu.
func (b *Background) finish(r *bgConsent, a string) {
	b.mu.Lock()
	if r.done {
		b.mu.Unlock()
		return
	}
	r.done = true
	for i, q := range b.consents {
		if q == r {
			b.consents = append(b.consents[:i], b.consents[i+1:]...)
			break
		}
	}
	b.mu.Unlock()
	go b.refreshTray()
	if a != "" {
		r.answer(a)
	}
}

// decide shows the dialog of r and answers it; it blocks, so it runs off
// the main thread.
func (b *Background) decide(r *bgConsent) {
	b.mu.Lock()
	done := r.done
	b.mu.Unlock()
	if done || b.ask == nil {
		return
	}
	body := consentBody(r.AgentConsent)
	if r.Summary != "" {
		body += "\n\n" + r.Summary
	}
	var answers []string
	var buttons []string
	if r.Kind == "write" {
		buttons, answers = []string{i18n.T("始终允许"), i18n.T("仅本次"), i18n.T("拒绝")}, []string{"always", "once", "deny"}
	} else {
		buttons, answers = []string{i18n.T("允许"), i18n.T("拒绝")}, []string{"once", "deny"}
	}
	deny := len(buttons) - 1
	i := b.ask(consentTitle(r.AgentConsent), body, buttons, deny)
	if i < 0 || i >= len(answers) {
		i = deny
	}
	b.finish(r, answers[i])
}

func (b *Background) consentItems() []*mygo.MenuItem {
	b.mu.Lock()
	pending := append([]*bgConsent(nil), b.consents...)
	b.mu.Unlock()
	var out []*mygo.MenuItem
	for _, r := range pending {
		r := r
		out = append(out, &mygo.MenuItem{Label: i18n.Tf("待授权：%s…", consentWho(r.AgentConsent)), ToolTip: consentTitle(r.AgentConsent),
			Click: func(*mygo.MenuItem, *mygo.Window) { go b.decide(r) }})
	}
	return out
}

func (b *Background) refreshTray() {
	b.mu.Lock()
	tr := b.tray
	b.mu.Unlock()
	if tr != nil {
		tr.refresh(false)
	}
}

// messageBox asks with a native dialog (no window of Lanflow's own).
func messageBox(title, body string, buttons []string, cancel int) int {
	mygo.App.Focus()
	res, err := mygo.Dialog.Message(mygo.MessageOptions{Type: mygo.MessageQuestion, Title: i18n.T("澜流 Lanflow"), Message: title, Detail: body,
		Buttons: buttons, DefaultButton: cancel, CancelButton: cancel})
	if err != nil {
		log.Printf("lanflow: dialog: %v", err)
		return -1
	}
	return res.Button
}

// ConfirmQuit asks before quitting while the system proxy is on, with a
// native dialog (the window's AskQuit, without a window); true = quit.
// It blocks: call it off the main thread.
func (b *Background) ConfirmQuit() bool {
	if !b.api.Status().SystemProxy {
		return true
	}
	if b.ask == nil {
		return true
	}
	return b.ask(i18n.T("退出后将关闭系统代理。"), i18n.T("由澜流开启的系统代理会恢复为关闭，内核也会停止。"),
		[]string{i18n.T("退出"), i18n.T("取消")}, 1) == 0
}

// ---- Background as host (host.go) ----

func (b *Background) hostAPI() app.API { return b.api }
func (b *Background) mac() bool        { return b.o.Mac }
func (b *Background) live() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started
}
func (b *Background) extraItems() []*mygo.MenuItem { return b.consentItems() }
func (b *Background) note(string)                  {} // the menu's checkmarks show it
func (b *Background) failed(msg string) {
	if b.notify != nil {
		short, full := splitError(msg)
		body := ""
		if full != short {
			body = full
		}
		b.notify(short, body, func() { b.showWindow("") })
	}
}

func (b *Background) runJob(job func() error, okMsg string) {
	go func() {
		if err := job(); err != nil {
			b.failed(err.Error())
		}
	}()
}

func (b *Background) showWindow(arg string) {
	if b.o.ShowWindow != nil {
		b.o.ShowWindow(arg)
	}
}

func (b *Background) toggleWindow() {
	if b.o.ToggleWindow != nil {
		b.o.ToggleWindow()
	}
}

func (b *Background) quit() {
	if b.o.Quit != nil {
		b.o.Quit()
	}
}

// setTUN turns TUN on or off; the helper's installation is confirmed in
// the window first.
func (b *Background) setTUN(on bool) {
	if on && !b.api.TUNHelperInstalled() {
		b.showWindow(OpenTUN)
		return
	}
	b.runJob(func() error { return b.api.SetTUN(on) }, "")
}
