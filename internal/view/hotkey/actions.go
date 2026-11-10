package hotkey

import (
	"sync"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Shortcut actions (model.ShortcutActions): the system proxy toggle the
// package started with and the actions added with it. The shell runs
// them (view/shortcuts.go) and registers their keys; the settings page
// records them (view/settings/hotkey.go) and asks the registrar below
// before it saves, so a refused combination never reaches the settings.

// ActionName returns the user-facing name of an action.
func ActionName(action string) string {
	switch action {
	case model.ShortcutSystemProxy:
		return i18n.T("开关系统代理")
	case model.ShortcutTUN:
		return i18n.T("开关 TUN 模式")
	case model.ShortcutCycleMode:
		return i18n.T("轮换模式")
	case model.ShortcutModeRule:
		return i18n.T("切换到规则模式")
	case model.ShortcutModeGlobal:
		return i18n.T("切换到全局模式")
	case model.ShortcutModeDirect:
		return i18n.T("切换到直连模式")
	case model.ShortcutWindow:
		return i18n.T("显示 / 隐藏窗口")
	case model.ShortcutTestDelay:
		return i18n.T("测试当前组延迟")
	}
	return action
}

// ActionDesc explains an action under its name.
func ActionDesc(action string) string {
	switch action {
	case model.ShortcutSystemProxy:
		return i18n.T("在任何应用中按下即可开关系统代理")
	case model.ShortcutTUN:
		return i18n.T("首次开启仍会询问安装权限组件")
	case model.ShortcutCycleMode:
		return i18n.T("规则 → 全局 → 直连 → 规则")
	case model.ShortcutWindow:
		return i18n.T("窗口在前台时隐藏，否则显示")
	case model.ShortcutTestDelay:
		return i18n.T("测速主代理组，和菜单栏的「测试当前组延迟」一样")
	}
	return ""
}

// Conflict returns the other action already bound to acc in set, or ""
// when acc is free. Accelerators are compared in canonical form.
func Conflict(set model.Settings, action, acc string) string {
	c, err := Parse(acc)
	if err != nil {
		return ""
	}
	want := c.String()
	for _, a := range model.ShortcutActions {
		if a == action {
			continue
		}
		other, err := Parse(set.ShortcutOf(a))
		if err == nil && other.String() == want {
			return a
		}
	}
	return ""
}

// Registrar binds acc ("" = none) to action system-wide, replacing the
// keys registered for the action; it returns a user-facing error, and
// keeps the previous keys, when the system refuses them.
type Registrar func(action, acc string) error

var (
	regMu     sync.Mutex
	registrar Registrar
)

// SetRegistrar installs the shell's registrar (nil = none: the settings
// page then accepts any shortcut, as in tests).
func SetRegistrar(r Registrar) {
	regMu.Lock()
	registrar = r
	regMu.Unlock()
}

// Register asks the registrar to bind acc to action.
func Register(action, acc string) error {
	regMu.Lock()
	r := registrar
	regMu.Unlock()
	if r == nil {
		return nil
	}
	return r(action, acc)
}
