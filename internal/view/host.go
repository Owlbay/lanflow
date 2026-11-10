package view

import (
	"github.com/egoist/mygo"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// The tray and the global shortcuts act through a host: the window's
// Shell when one process does everything, the service process's
// Background when the window runs in a process of its own
// (docs/service-split.md S2).
type host interface {
	hostAPI() app.API
	mac() bool
	// runJob runs job off the main thread and reports the outcome.
	runJob(job func() error, okMsg string)
	note(msg string)   // a short confirmation, e.g. 已复制
	failed(msg string) // an error the user should see
	// showWindow brings up the window; arg asks it to open something
	// (OpenFeedback, OpenTUN, OpenLogs, a deep link).
	showWindow(arg string)
	toggleWindow()
	setTUN(on bool)
	// live reports whether the app runs (shortcuts are registered with
	// the system); false in tests, where they are only remembered.
	live() bool
	quit()
	// extraItems are menu items the host adds below the status line
	// (Background: agents waiting for an answer).
	extraItems() []*mygo.MenuItem
}

// What the window is asked to open (host.showWindow, Shell.Open).
const (
	OpenFeedback = "feedback" // the 反馈问题 dialog
	OpenTUN      = "tun"      // turn TUN on, installing the helper first (asks)
	OpenLogs     = "logs"     // the 日志 page
)

// toggleSystemProxy flips the system proxy (tray, shortcut).
func toggleSystemProxy(h host) {
	api := h.hostAPI()
	st := api.Status()
	on := !(st.Core == model.CoreRunning && st.SystemProxy)
	msg := i18n.T("系统代理已关闭")
	if on {
		msg = i18n.T("系统代理已开启")
	}
	h.runJob(func() error { return api.SetSystemProxy(on) }, msg)
}

// ---- the shell as host ----

func (s *Shell) hostAPI() app.API                      { return s.api }
func (s *Shell) mac() bool                             { return s.opts.Mac }
func (s *Shell) runJob(job func() error, okMsg string) { s.run(job, okMsg) }
func (s *Shell) note(msg string)                       { s.toast(toastReq{msg: msg}) }
func (s *Shell) failed(msg string)                     { s.errorToast(msg, "") }
func (s *Shell) live() bool                            { return s.win != nil }
func (s *Shell) quit()                                 { mygo.App.Quit() }
func (s *Shell) extraItems() []*mygo.MenuItem          { return nil }
func (s *Shell) showWindow(arg string)                 { s.ShowWindow(); s.Open(arg) }
func (s *Shell) setTUN(on bool) {
	if on && !s.api.TUNHelperInstalled() {
		s.ShowWindow() // the helper install asks in the window first
	}
	env.SetTUN(s.env, on)
}

// ToggleWindow is the window shortcut: hide (Remote: close) the window
// when it is in front, else show it.
func (s *Shell) ToggleWindow() { s.toggleWindow() }

func (s *Shell) toggleWindow() {
	if s.win == nil {
		return
	}
	if s.win.IsVisible() && s.win.IsFocused() {
		if s.opts.Remote && s.closeWindow != nil {
			s.closeWindow() // the window's process exits; the service stays
			return
		}
		s.win.Hide()
		return
	}
	s.ShowWindow()
}

// Open acts on what the window was asked to open (OpenFeedback, OpenTUN,
// OpenLogs, a lanflow:// or clash:// link); "" does nothing. Safe from any
// goroutine.
func (s *Shell) Open(arg string) {
	switch arg {
	case "":
	case OpenFeedback:
		s.OpenFeedback("")
	case OpenTUN:
		s.post(func() { env.SetTUN(s.env, true) })
	case OpenLogs:
		s.post(func() { s.navigate(env.Logs, "") })
	default:
		s.OpenImportLink(arg)
	}
}

// ServiceConnection tells the user that the window lost the service
// process (up false) or got it back (Remote).
func (s *Shell) ServiceConnection(up bool) {
	if up {
		s.toast(toastReq{msg: i18n.T("已重新连接澜流后台服务")})
		return
	}
	s.toast(toastReq{msg: i18n.T("与澜流后台服务的连接已断开，正在重新连接"), isError: true})
}

// AskRestartService offers to restart a service process of another
// version (an update replaced the app while the service ran).
func (s *Shell) AskRestartService(service, window string, restart func()) {
	s.ask(i18n.T("需要重启澜流后台服务"),
		i18n.Tf("后台服务的版本（%s）与界面（%s）不一致，通常是更新后旧的后台服务还在运行。重启后台服务时内核会短暂停止。", service, window),
		i18n.T("重启后台服务"), false, func() { go restart() })
}
