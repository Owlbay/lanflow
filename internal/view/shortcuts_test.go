package view

import (
	"testing"

	"lanflow/internal/model"
	"lanflow/internal/view/hotkey"
)

// Shortcut actions run against the API; before Attach the registrar only
// remembers the keys (no window, no system registration).
func TestShortcutActionsRun(t *testing.T) {
	api, s, tt := newTest(t)
	errs := s.RegisterShortcuts()
	t.Cleanup(func() { hotkey.SetRegistrar(nil) })
	if len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	// Cycle: rule → global → direct → rule.
	for _, want := range []model.Mode{model.ModeGlobal, model.ModeDirect, model.ModeRule} {
		s.runShortcut(model.ShortcutCycleMode)
		if got := api.Status().Mode; got != want {
			t.Fatalf("cycle: mode = %s, want %s", got, want)
		}
	}
	s.runShortcut(model.ShortcutModeDirect)
	if api.Status().Mode != model.ModeDirect {
		t.Fatal("direct mode not set")
	}
	s.runShortcut(model.ShortcutModeRule)
	if api.Status().Mode != model.ModeRule {
		t.Fatal("rule mode not set")
	}
	s.runShortcut(model.ShortcutSystemProxy)
	if api.Status().SystemProxy {
		t.Fatal("system proxy not toggled off")
	}
	s.runShortcut(model.ShortcutTestDelay)
	tt.Frame()
	if !tt.HasText("「节点选择」测速完成") {
		t.Fatalf("delay test toast missing: %q", tt.Texts())
	}
	s.runShortcut(model.ShortcutWindow) // no window: a no-op

	// The registrar remembers the keys and follows settings changes.
	if err := hotkey.Register(model.ShortcutTUN, "CmdOrCtrl+Alt+T"); err != nil {
		t.Fatal(err)
	}
	if got := s.shortcutReg().bound[model.ShortcutTUN]; got != "CmdOrCtrl+Alt+T" {
		t.Fatalf("bound = %q", got)
	}
	set := api.Settings().WithShortcut(model.ShortcutWindow, "CmdOrCtrl+Alt+L")
	if err := api.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if got := s.shortcutReg().bound[model.ShortcutWindow]; got != "CmdOrCtrl+Alt+L" {
		t.Fatalf("not followed: %q", got)
	}
	if err := api.SaveSettings(api.Settings().WithShortcut(model.ShortcutWindow, "")); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.shortcutReg().bound[model.ShortcutWindow]; ok {
		t.Fatal("not unbound")
	}
}
