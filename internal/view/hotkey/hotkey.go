// Package hotkey parses, formats and records the global shortcut that
// toggles the system proxy (docs/design.md §9: default ⌘⇧S / Ctrl+Shift+S,
// 可修改). Shortcuts are stored as MyGo accelerators such as
// "CmdOrCtrl+Shift+S", the form mygo.GlobalShortcut and menu items take.
package hotkey

import (
	"errors"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
)

// Default is the shortcut of a fresh installation.
const Default = "CmdOrCtrl+Shift+S"

// Modifier flags of a parsed shortcut.
const (
	Cmd   = 1 << iota // "CmdOrCtrl": ⌘ on macOS, Ctrl elsewhere
	Ctrl              // "Ctrl"/"Control" written explicitly
	Alt               // "Alt"/"Option"
	Shift             // "Shift"
	Super             // "Super"/"Meta"/"Win" written explicitly
)

// Combo is a parsed shortcut: modifier flags and the key name in the
// accelerator's spelling ("S", "F5", "Space", ",").
type Combo struct {
	Mods int
	Key  string
}

// ErrNoModifier is returned when a shortcut has no modifier other than
// Shift: a bare key would swallow ordinary typing in every app.
var ErrNoModifier = i18n.Error("快捷键至少要包含 ⌘ / Ctrl / Alt 之一")

// namedKeys are the keys an accelerator names by word, by lower-case spelling.
var namedKeys = map[string]string{
	"space": "Space", "tab": "Tab", "enter": "Enter", "return": "Enter",
	"backspace": "Backspace", "delete": "Delete", "del": "Delete", "insert": "Insert", "ins": "Insert",
	"escape": "Escape", "esc": "Escape", "home": "Home", "end": "End",
	"pageup": "PageUp", "pagedown": "PageDown", "up": "Up", "down": "Down", "left": "Left", "right": "Right",
	"plus": "+",
}

// Parse reads an accelerator. Modifiers may come in any order and
// spelling MyGo accepts ("CmdOrCtrl", "Cmd", "Command", "Ctrl", "Control",
// "Alt", "Option", "Shift", "Super", "Meta", "Win"); the key is a letter,
// digit, punctuation mark, F1–F24 or a named key.
func Parse(acc string) (Combo, error) {
	var c Combo
	acc = strings.TrimSpace(acc)
	if acc == "" {
		return c, errors.New(i18n.T("快捷键不能为空"))
	}
	parts := strings.Split(acc, "+")
	// "CmdOrCtrl++" means the "+" key.
	if n := len(parts); n >= 2 && parts[n-1] == "" && parts[n-2] == "" {
		parts = append(parts[:n-2], "+")
	}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		last := i == len(parts)-1
		switch strings.ToLower(p) {
		case "cmdorctrl", "commandorcontrol", "cmd", "command":
			if !last {
				c.Mods |= Cmd
				continue
			}
		case "ctrl", "control":
			if !last {
				c.Mods |= Ctrl
				continue
			}
		case "alt", "option":
			if !last {
				c.Mods |= Alt
				continue
			}
		case "shift":
			if !last {
				c.Mods |= Shift
				continue
			}
		case "super", "meta", "win":
			if !last {
				c.Mods |= Super
				continue
			}
		}
		if !last {
			return c, errors.New(i18n.Tf("无法识别的修饰键「%s」", p))
		}
		key, ok := keyName(p)
		if !ok {
			return c, errors.New(i18n.Tf("无法识别的按键「%s」", p))
		}
		c.Key = key
	}
	if c.Key == "" {
		return c, errors.New(i18n.T("快捷键缺少按键"))
	}
	if c.Mods&^Shift == 0 {
		return c, ErrNoModifier
	}
	return c, nil
}

// keyName canonicalizes one key part.
func keyName(p string) (string, bool) {
	if n, ok := namedKeys[strings.ToLower(p)]; ok {
		return n, true
	}
	if r := []rune(p); len(r) == 1 {
		ch := r[0]
		switch {
		case ch >= 'a' && ch <= 'z':
			return strings.ToUpper(p), true
		case ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
			return p, true
		case strings.ContainsRune("-=,./;'[]\\`+", ch):
			return p, true
		}
		return "", false
	}
	if up := strings.ToUpper(p); strings.HasPrefix(up, "F") {
		if n, err := strconv.Atoi(up[1:]); err == nil && n >= 1 && n <= 24 {
			return "F" + strconv.Itoa(n), true
		}
	}
	return "", false
}

// String formats the combo as a canonical accelerator, the stored form.
func (c Combo) String() string {
	var parts []string
	if c.Mods&Cmd != 0 {
		parts = append(parts, "CmdOrCtrl")
	}
	if c.Mods&Ctrl != 0 {
		parts = append(parts, "Ctrl")
	}
	if c.Mods&Super != 0 {
		parts = append(parts, "Super")
	}
	if c.Mods&Alt != 0 {
		parts = append(parts, "Alt")
	}
	if c.Mods&Shift != 0 {
		parts = append(parts, "Shift")
	}
	return strings.Join(append(parts, c.Key), "+")
}

// Display formats the combo for the user: "⌘⇧S" on macOS, "Ctrl+Shift+S"
// elsewhere. Order follows the platform's menus.
func (c Combo) Display(mac bool) string {
	key := c.Key
	if mac {
		var b strings.Builder
		if c.Mods&Ctrl != 0 {
			b.WriteString("⌃")
		}
		if c.Mods&Alt != 0 {
			b.WriteString("⌥")
		}
		if c.Mods&Shift != 0 {
			b.WriteString("⇧")
		}
		if c.Mods&(Cmd|Super) != 0 {
			b.WriteString("⌘")
		}
		switch key {
		case "Space":
			key = i18n.T("空格")
		case "Enter":
			key = "↩"
		case "Backspace":
			key = "⌫"
		case "Delete":
			key = "⌦"
		case "Escape":
			key = "⎋"
		case "Tab":
			key = "⇥"
		case "Up":
			key = "↑"
		case "Down":
			key = "↓"
		case "Left":
			key = "←"
		case "Right":
			key = "→"
		}
		b.WriteString(key)
		return b.String()
	}
	var parts []string
	if c.Mods&(Cmd|Ctrl) != 0 {
		parts = append(parts, "Ctrl")
	}
	if c.Mods&Super != 0 {
		parts = append(parts, "Win")
	}
	if c.Mods&Alt != 0 {
		parts = append(parts, "Alt")
	}
	if c.Mods&Shift != 0 {
		parts = append(parts, "Shift")
	}
	return strings.Join(append(parts, key), "+")
}

// Display formats a stored accelerator for the user; an empty or unreadable
// one shows as "未设置".
func Display(acc string, mac bool) string {
	c, err := Parse(acc)
	if err != nil {
		return i18n.T("未设置")
	}
	return c.Display(mac)
}

// FromKey turns a key press into a combo, for a shortcut recorder.
// ok is false for a modifier on its own or a key with no modifier but
// Shift; the platform's primary modifier (⌘ or Ctrl) becomes "CmdOrCtrl"
// so the stored shortcut reads the same on both systems.
func FromKey(mods ui.Modifiers, key ui.Key, mac bool) (Combo, bool) {
	name := uiKeyName(key)
	if name == "" {
		return Combo{}, false
	}
	var c Combo
	c.Key = name
	primary, other := ui.Ctrl, ui.Super
	if mac {
		primary, other = ui.Super, ui.Ctrl
	}
	if mods&primary != 0 {
		c.Mods |= Cmd
	}
	if mods&other != 0 {
		if mac {
			c.Mods |= Ctrl
		} else {
			c.Mods |= Super
		}
	}
	if mods&ui.Alt != 0 {
		c.Mods |= Alt
	}
	if mods&ui.Shift != 0 {
		c.Mods |= Shift
	}
	if c.Mods&^Shift == 0 {
		return Combo{}, false
	}
	return c, true
}

// uiKeyName names a ui.Key in accelerator spelling; "" for keys an
// accelerator cannot name (modifiers, unknown keys).
func uiKeyName(k ui.Key) string {
	switch {
	case k >= ui.KeyA && k <= ui.KeyZ:
		return string(rune('A' + k - ui.KeyA))
	case k >= ui.Key0 && k <= ui.Key9:
		return string(rune('0' + k - ui.Key0))
	case k >= ui.KeyF1 && k <= ui.KeyF12:
		return "F" + strconv.Itoa(int(k-ui.KeyF1)+1)
	}
	switch k {
	case ui.KeyEnter:
		return "Enter"
	case ui.KeySpace:
		return "Space"
	case ui.KeyBackspace:
		return "Backspace"
	case ui.KeyDelete:
		return "Delete"
	case ui.KeyInsert:
		return "Insert"
	case ui.KeyHome:
		return "Home"
	case ui.KeyEnd:
		return "End"
	case ui.KeyPageUp:
		return "PageUp"
	case ui.KeyPageDown:
		return "PageDown"
	case ui.KeyUp:
		return "Up"
	case ui.KeyDown:
		return "Down"
	case ui.KeyLeft:
		return "Left"
	case ui.KeyRight:
		return "Right"
	case ui.KeyMinus:
		return "-"
	case ui.KeyEqual:
		return "="
	case ui.KeyComma:
		return ","
	case ui.KeyPeriod:
		return "."
	case ui.KeySlash:
		return "/"
	case ui.KeySemicolon:
		return ";"
	case ui.KeyQuote:
		return "'"
	case ui.KeyBracketLeft:
		return "["
	case ui.KeyBracketRight:
		return "]"
	case ui.KeyBackslash:
		return "\\"
	case ui.KeyBackquote:
		return "`"
	}
	return ""
}
