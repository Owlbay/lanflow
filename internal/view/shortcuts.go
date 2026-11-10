package view

import (
	"fmt"
	"log"
	"sync"

	"github.com/egoist/mygo"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/hotkey"
)

// Global shortcuts beyond the system proxy toggle (Settings.Shortcuts):
// TUN, the routing mode, the window and the delay test. The system-proxy
// shortcut keeps its own path (Settings.GlobalShortcut, SetGlobalShortcut,
// which the tray's menu also shows); the actions here are registered
// beside it with the same rules: a refused combination keeps the previous
// keys and is reported, and the settings page asks before it saves
// (hotkey.Register). Settings changed behind the page (an import, a
// restored backup) are followed through OnChange.

// shortcutState is what a shell registered, kept beside the shell.
type shortcutState struct {
	mu      sync.Mutex
	bound   map[string]string // action → accelerator registered with the system
	syncing bool
}

var (
	shortcutMu     sync.Mutex
	shortcutStates = map[*Shell]*shortcutState{}
)

func (s *Shell) shortcutReg() *shortcutState {
	shortcutMu.Lock()
	defer shortcutMu.Unlock()
	st := shortcutStates[s]
	if st == nil {
		st = &shortcutState{bound: map[string]string{}}
		shortcutStates[s] = st
	}
	return st
}

// RegisterShortcuts binds every action of the settings other than the
// system proxy (RegisterGlobalShortcut does that one) once the app is
// ready, installs the registrar the settings page uses and follows later
// settings changes. It returns the actions the system refused, by action.
func (s *Shell) RegisterShortcuts() map[string]error {
	hotkey.SetRegistrar(s.setShortcut)
	errs := s.syncShortcuts(s.api.Settings())
	s.api.OnChange(func(t app.Topic) {
		if t != app.TopicSettings {
			return
		}
		for action, err := range s.syncShortcuts(s.api.Settings()) {
			log.Printf("lanflow: shortcut %s: %v", action, err)
		}
	})
	return errs
}

// syncShortcuts makes the registered keys match set.
func (s *Shell) syncShortcuts(set model.Settings) map[string]error {
	st := s.shortcutReg()
	st.mu.Lock()
	if st.syncing {
		st.mu.Unlock()
		return nil
	}
	st.syncing = true
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		st.syncing = false
		st.mu.Unlock()
	}()
	var errs map[string]error
	for _, action := range model.ShortcutActions {
		if action == model.ShortcutSystemProxy {
			continue
		}
		want := set.ShortcutOf(action)
		st.mu.Lock()
		have := st.bound[action]
		st.mu.Unlock()
		if want == have {
			continue
		}
		if err := s.setShortcut(action, want); err != nil {
			if errs == nil {
				errs = map[string]error{}
			}
			errs[action] = err
		}
	}
	return errs
}

// setShortcut is the hotkey.Registrar: it binds acc ("" = none) to
// action, replacing the keys bound before. The system-proxy action goes
// through SetGlobalShortcut. Before Attach (tests) it only remembers.
func (s *Shell) setShortcut(action, acc string) error {
	if action == model.ShortcutSystemProxy {
		return s.SetGlobalShortcut(acc)
	}
	st := s.shortcutReg()
	st.mu.Lock()
	prev := st.bound[action]
	st.mu.Unlock()
	set := func(v string) {
		st.mu.Lock()
		if v == "" {
			delete(st.bound, action)
		} else {
			st.bound[action] = v
		}
		st.mu.Unlock()
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
	fn := func() { s.runShortcut(action) }
	if err := mygo.GlobalShortcut.Register(acc, fn); err != nil {
		log.Printf("lanflow: shortcut %s %q: %v", action, acc, err)
		kept := i18n.T("该动作的快捷键保持关闭。")
		if prev != "" && mygo.GlobalShortcut.Register(prev, fn) == nil {
			set(prev)
			kept = i18n.Tf("仍使用原来的快捷键 %s。", hotkey.Display(prev, s.opts.Mac))
		}
		return fmt.Errorf(i18n.T("无法注册快捷键 %s：可能已被其他应用占用。%s可以换一个组合键再试。"), hotkey.Display(acc, s.opts.Mac), kept)
	}
	set(acc)
	return nil
}

// runShortcut performs action; it is called on the main thread by the
// key press, so the work goes through run (off the UI thread) like the
// tray's commands.
func (s *Shell) runShortcut(action string) {
	st := s.api.Status()
	switch action {
	case model.ShortcutSystemProxy:
		s.ToggleSystemProxy()
	case model.ShortcutTUN:
		if !st.TUN && !s.api.TUNHelperInstalled() {
			s.ShowWindow() // the helper's confirmation is a dialog in the window
		}
		env.SetTUN(s.env, !st.TUN)
	case model.ShortcutCycleMode:
		next := map[model.Mode]model.Mode{model.ModeRule: model.ModeGlobal, model.ModeGlobal: model.ModeDirect, model.ModeDirect: model.ModeRule}[st.Mode]
		if next == "" {
			next = model.ModeRule
		}
		s.setModeShortcut(next)
	case model.ShortcutModeRule:
		s.setModeShortcut(model.ModeRule)
	case model.ShortcutModeGlobal:
		s.setModeShortcut(model.ModeGlobal)
	case model.ShortcutModeDirect:
		s.setModeShortcut(model.ModeDirect)
	case model.ShortcutWindow:
		if s.win == nil {
			return
		}
		if s.win.IsVisible() && s.win.IsFocused() {
			s.win.Hide()
			return
		}
		s.ShowWindow()
	case model.ShortcutTestDelay:
		g, ok := mainGroup(s.api.Proxies(), st)
		if !ok {
			s.errorToast(i18n.T("没有可测速的代理组"), "")
			return
		}
		name := g.Name
		s.run(func() error { return s.api.TestGroupDelay(name) }, i18n.Tf("「%s」测速完成", name))
	}
}

func (s *Shell) setModeShortcut(mode model.Mode) {
	s.run(func() error { return s.api.SetMode(mode) }, i18n.Tf("已切换到%s模式", modeName(mode)))
}
