package i18n

// English of the shortcut actions (internal/view/hotkey).
func init() {
	for k, v := range map[string]string{
		// internal/view/hotkey/actions.go
		"在任何应用中按下即可开关系统代理": "Press in any app to toggle the system proxy",
		"开关系统代理":            "Toggle system proxy",
		"开关 TUN 模式":         "Toggle TUN mode",
		"轮换模式":              "Cycle mode",
		"切换到全局模式":           "Switch to global mode",
		"切换到直连模式":           "Switch to direct mode",
		"显示 / 隐藏窗口":         "Show / hide window",
		"首次开启仍会询问安装权限组件":    "The first time still asks to install the helper",
		"规则 → 全局 → 直连 → 规则": "Rule → global → direct → rule",
		"窗口在前台时隐藏，否则显示":     "Hides the window when it is in front, shows it otherwise",
		"测速主代理组，和菜单栏的「测试当前组延迟」一样": "Tests the main group, like \"Test Current Group Delay\" in the menu bar",

		// internal/view/hotkey/hotkey.go
		"快捷键至少要包含 ⌘ / Ctrl / Alt 之一": "A shortcut needs at least one of ⌘ / Ctrl / Alt",
		"快捷键不能为空":                    "The shortcut cannot be empty",
		"无法识别的修饰键「%s」":               "Unknown modifier “%s”",
		"无法识别的按键「%s」":                "Unknown key “%s”",
		"快捷键缺少按键":                    "The shortcut has no key",
		"空格":                         "Space",
	} {
		en[k] = v
	}
}
