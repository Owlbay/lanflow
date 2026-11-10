package view

import (
	"log"

	"lanflow/internal/app"
	"lanflow/internal/model"
	"lanflow/internal/view/hotkey"
)

// Global shortcuts beyond the system proxy toggle (Settings.Shortcuts):
// TUN, the routing mode, the window and the delay test. The system-proxy
// shortcut keeps its own path (Settings.GlobalShortcut, SetGlobalShortcut,
// which the tray's menu also shows); the actions here are registered
// beside it with the same rules (keys.go). Settings changed behind the
// page (an import, a restored backup) are followed through OnChange.
//
// With the window in its own process (Options.Remote) the service process
// holds the shortcuts: the settings page registers through
// Options.SetShortcut and the service follows the settings itself.

// RegisterShortcuts binds every action of the settings other than the
// system proxy (RegisterGlobalShortcut does that one) once the app is
// ready, installs the registrar the settings page uses and follows later
// settings changes. It returns the actions the system refused, by action.
func (s *Shell) RegisterShortcuts() map[string]error {
	if s.opts.Remote {
		hotkey.SetRegistrar(s.opts.SetShortcut)
		return nil
	}
	hotkey.SetRegistrar(s.keys.set)
	errs := s.keys.sync(s.api.Settings(), false)
	s.api.OnChange(func(t app.Topic) {
		if t != app.TopicSettings {
			return
		}
		for action, err := range s.keys.sync(s.api.Settings(), false) {
			log.Printf("lanflow: shortcut %s: %v", action, err)
		}
	})
	return errs
}

// runShortcut performs action (keys.run).
func (s *Shell) runShortcut(action string) { s.keys.run(action) }

// RegisterGlobalShortcut binds the system-proxy hotkey of the settings
// app-wide (docs/design.md §9, default ⌘⇧S / Ctrl+Shift+S, 可修改); call it
// once the app is ready. Registration fails when another app holds the keys.
func (s *Shell) RegisterGlobalShortcut() error {
	if s.opts.Remote {
		return nil
	}
	return s.SetGlobalShortcut(s.api.Settings().GlobalShortcut)
}

// SetGlobalShortcut is Env.SetGlobalShortcut: it replaces the registered
// shortcut with acc ("" = none). When the system refuses acc the previous
// shortcut is registered again and the error says so in Chinese. Before
// Attach (tests, no running app) it only remembers acc.
func (s *Shell) SetGlobalShortcut(acc string) error {
	if s.opts.Remote {
		return s.opts.SetShortcut(model.ShortcutSystemProxy, acc)
	}
	return s.keys.set(model.ShortcutSystemProxy, acc)
}

// ToggleSystemProxy flips the system proxy (tray, hotkey).
func (s *Shell) ToggleSystemProxy() { toggleSystemProxy(s) }
