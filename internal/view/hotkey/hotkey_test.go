package hotkey

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestParseAndString(t *testing.T) {
	cases := map[string]string{
		"CmdOrCtrl+Shift+S":  "CmdOrCtrl+Shift+S",
		"shift+cmdorctrl+s":  "CmdOrCtrl+Shift+S",
		"Ctrl+Alt+F5":        "Ctrl+Alt+F5",
		"Option+Command+p":   "CmdOrCtrl+Alt+P",
		"CmdOrCtrl+Space":    "CmdOrCtrl+Space",
		"CmdOrCtrl++":        "CmdOrCtrl++",
		"Alt+,":              "Alt+,",
		"Super+Shift+Return": "Super+Shift+Enter",
	}
	for in, want := range cases {
		c, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if got := c.String(); got != want {
			t.Errorf("Parse(%q).String() = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "S", "Shift+S", "CmdOrCtrl", "CmdOrCtrl+Shift", "Hyper+S", "CmdOrCtrl+Foo", "CmdOrCtrl+F99"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
	if _, err := Parse("Shift+S"); err != ErrNoModifier {
		t.Errorf("Shift+S: %v, want ErrNoModifier", err)
	}
}

func TestDisplay(t *testing.T) {
	if got := Display(Default, true); got != "⇧⌘S" {
		t.Errorf("mac display = %q", got)
	}
	if got := Display(Default, false); got != "Ctrl+Shift+S" {
		t.Errorf("windows display = %q", got)
	}
	if got := Display("Ctrl+Alt+Space", true); got != "⌃⌥空格" {
		t.Errorf("mac ctrl display = %q", got)
	}
	if got := Display("", true); got != "未设置" {
		t.Errorf("empty display = %q", got)
	}
}

func TestFromKey(t *testing.T) {
	c, ok := FromKey(ui.Super|ui.Shift, ui.KeyS, true)
	if !ok || c.String() != Default {
		t.Fatalf("mac ⇧⌘S = %q %v", c.String(), ok)
	}
	c, ok = FromKey(ui.Ctrl|ui.Shift, ui.KeyS, false)
	if !ok || c.String() != Default {
		t.Fatalf("windows Ctrl+Shift+S = %q %v", c.String(), ok)
	}
	c, ok = FromKey(ui.Ctrl|ui.Alt, ui.KeyF5, true)
	if !ok || c.String() != "Ctrl+Alt+F5" {
		t.Fatalf("mac ⌃⌥F5 = %q %v", c.String(), ok)
	}
	if _, ok := FromKey(ui.Shift, ui.KeyS, true); ok {
		t.Error("Shift+S must be refused")
	}
	if _, ok := FromKey(ui.Super, ui.KeyUnknown, true); ok {
		t.Error("a lone modifier must be refused")
	}
	if _, ok := FromKey(0, ui.KeyF5, true); ok {
		t.Error("a bare key must be refused")
	}
}
