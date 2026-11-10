package settings

import (
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/hotkey"
)

// 全局快捷键 (docs/design.md §9: 默认 ⌘⇧S / Ctrl+Shift+S，可修改), one row
// per action (model.ShortcutActions): a recorder that takes the next key
// combination pressed while it has the focus, 「恢复默认」 for the system
// proxy and 「清除」. A combination another action already uses is refused
// here; the shell registers the keys first (hotkey.Register, the
// system-proxy action through Env.SetGlobalShortcut) and when the system
// refuses them the error shows under the row and the old keys stay.

// shortcutRows draws the rows inside the 常规 section: the actions that
// have keys (and those shown earlier in this session, so a cleared row
// stays), then a row whose select adds another action to the list.
func (p *Page) shortcutRows(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	if p.f2.hotkeyShown == nil {
		p.f2.hotkeyShown = map[string]bool{}
	}
	var hidden []string
	for _, action := range model.ShortcutActions {
		action := action
		acc := s.ShortcutOf(action)
		if acc != "" {
			p.f2.hotkeyShown[action] = true
		}
		if !p.f2.hotkeyShown[action] {
			hidden = append(hidden, action)
			continue
		}
		desc := hotkey.ActionDesc(action)
		if acc == "" && action == model.ShortcutSystemProxy {
			desc = i18n.T("已关闭；点击右侧并按下组合键来设置")
		}
		e.Kit.Row(c, hotkey.ActionName(action), desc, func() {
			ui.Column(c).Gap(4).AlignItems(ui.End).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if action == model.ShortcutSystemProxy && acc != hotkey.Default {
						ui.Button(c, i18n.T("恢复默认")).Disabled(p.saving).OnClick(func() { p.setShortcut(s, action, hotkey.Default) })
					}
					if acc != "" {
						ui.Button(c, i18n.T("清除")).Disabled(p.saving).OnClick(func() { p.setShortcut(s, action, "") })
					}
					p.hotkeyRecorder(c, t, s, action)
				})
				if msg := p.f2.hotkeyErrs[action]; msg != "" {
					ui.Text(c, msg).FontSize(11).TextColor(t.Danger).MaxWidth(360).TextAlign(ui.End)
				}
			})
		})
	}
	if len(hidden) == 0 {
		return
	}
	e.Kit.Row(c, i18n.T("添加快捷键"), i18n.T("为其他动作设置组合键"), func() {
		opts := []string{i18n.T("选择动作…")}
		for _, a := range hidden {
			opts = append(opts, hotkey.ActionName(a))
		}
		v := opts[0]
		if ui.Select(c.Key("hotkey-add"), &v, opts).Label(i18n.T("添加快捷键")).Changed() {
			for i, a := range hidden {
				if opts[i+1] == v {
					p.f2.hotkeyShown[a] = true
				}
			}
		}
	})
}

// hotkeyRecorder draws the field that shows the shortcut and records a
// new one while focused: Backspace or Delete clears it, Tab moves on.
func (p *Page) hotkeyRecorder(c *ui.Context, t *ui.Theme, s model.Settings, action string) {
	e := p.e
	acc := s.ShortcutOf(action)
	box := ui.Row(c.Key("hotkey-"+action)).Focusable().Role(ui.RoleButton).Label(hotkey.ActionName(action)).
		MinWidth(132).Height(28).PaddingX(10).Radius(6).AlignItems(ui.Center).Justify(ui.Center).
		Background(t.Surface).Border(1, t.Border).Cursor(ui.CursorPointer)
	focused := box.Focused()
	if focused {
		box.Border(1, t.Accent)
	}
	box.Children(func() {
		switch {
		case focused:
			ui.Text(c, i18n.T("按下组合键…")).FontSize(12).TextColor(t.TextMuted).SingleLine()
		case acc == "":
			ui.Text(c, i18n.T("未设置")).FontSize(12).TextColor(t.TextMuted).SingleLine()
		default:
			ui.Text(c, hotkey.Display(acc, e.Mac)).FontSize(13).FontFeatures("tnum").SingleLine()
		}
	})
	box.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputKeyDown || ev.Repeat {
			return false
		}
		switch ev.Key {
		case ui.KeyTab:
			return false // moves the focus on
		case ui.KeyEscape:
			p.setHotkeyErr(action, "")
			return true
		case ui.KeyBackspace, ui.KeyDelete:
			if ev.Mods == 0 {
				p.setShortcut(s, action, "")
				return true
			}
		case ui.KeyUnknown:
			return true // a modifier on its own: wait for the key
		}
		combo, ok := hotkey.FromKey(ev.Mods, ev.Key, e.Mac)
		if !ok {
			p.setHotkeyErr(action, hotkey.ErrNoModifier.Error())
			return true
		}
		p.setShortcut(s, action, combo.String())
		return true
	})
}

func (p *Page) setHotkeyErr(action, msg string) {
	if p.f2.hotkeyErrs == nil {
		p.f2.hotkeyErrs = map[string]string{}
	}
	if msg == "" {
		delete(p.f2.hotkeyErrs, action)
		return
	}
	p.f2.hotkeyErrs[action] = msg
}

// setShortcut checks acc ("" = off) against the other actions, registers
// it through the shell, then saves it.
func (p *Page) setShortcut(s model.Settings, action, acc string) {
	if acc == s.ShortcutOf(action) {
		p.setHotkeyErr(action, "")
		return
	}
	if acc != "" {
		if _, err := hotkey.Parse(acc); err != nil {
			p.setHotkeyErr(action, err.Error())
			return
		}
		if other := hotkey.Conflict(s, action, acc); other != "" {
			p.setHotkeyErr(action, i18n.Tf("%s 已用于「%s」，请换一个组合键。", hotkey.Display(acc, p.e.Mac), hotkey.ActionName(other)))
			return
		}
	}
	var err error
	if action == model.ShortcutSystemProxy && p.e.SetGlobalShortcut != nil {
		err = p.e.SetGlobalShortcut(acc)
	} else if action != model.ShortcutSystemProxy {
		err = hotkey.Register(action, acc)
	}
	if err != nil {
		p.setHotkeyErr(action, err.Error())
		return
	}
	p.setHotkeyErr(action, "")
	s = s.WithShortcut(action, acc)
	msg := i18n.Tf("「%s」的快捷键已关闭", hotkey.ActionName(action))
	if acc != "" {
		msg = i18n.Tf("「%s」的快捷键已改为 %s", hotkey.ActionName(action), hotkey.Display(acc, p.e.Mac))
	}
	if action == model.ShortcutSystemProxy { // the wording the feature started with
		msg = i18n.T("全局快捷键已关闭")
		if acc != "" {
			msg = i18n.T("全局快捷键已改为 ") + hotkey.Display(acc, p.e.Mac)
		}
	}
	p.save(s, msg, nil)
}
