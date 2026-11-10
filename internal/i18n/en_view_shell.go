package i18n

// English of the window shell (internal/view): the sidebar, dialogs, toasts and the tray menu.
func init() {
	for k, v := range map[string]string{
		// internal/view/importlink.go
		"松开以导入":                  "Drop to Import",
		"支持 .yaml / .yml 文件":     ".yaml / .yml files",
		"只能导入 .yaml / .yml 配置文件": "Only .yaml / .yml config files can be imported",

		// internal/view/kit.go
		"测速中": "Testing latency",

		// internal/view/shell.go
		"澜流仍在%s运行":           "Lanflow keeps running in %s",
		"点击%s图标可以打开窗口或退出澜流。": "Click the icon in %s to open the window or quit Lanflow.",
		"全局快捷键保持关闭。":         "The global shortcut stays off.",
		"仍使用原来的快捷键 %s。":      "The previous shortcut %s stays in use.",
		"无法注册快捷键 %s：可能已被其他应用占用。%s可以换一个组合键再试。": "Could not register the shortcut %s: another app may hold it. %s Try another combination.",
		"标题栏": "Title bar",

		// internal/view/shell_dialogs.go
		"退出后将关闭系统代理。":              "Quitting turns off the system proxy.",
		"由澜流开启的系统代理会恢复为关闭，内核也会停止。": "The system proxy Lanflow turned on is turned off again, and the core stops.",
		"退出":      "Quit",
		"查看详情":    "Details",
		"错误详情":    "Error Details",
		"好":       "OK",
		"复制错误":    "Copy Error",
		"系统代理未恢复": "System Proxy Not Restored",
		"上次澜流未正常退出，系统代理仍指向 127.0.0.1:%d。如果不处理，部分应用可能无法联网。": "Lanflow did not quit properly last time and the system proxy still points at 127.0.0.1:%d. Left as is, some apps may not reach the network.",
		"保持开启并启动内核": "Keep On and Start Core",
		"恢复为关闭":     "Turn Off",
		"「%s」更新后的配置未通过校验，已继续使用上次的配置。": "The updated config of “%s” failed validation; the previous config stays in use.",
		"「%s」的配置未通过校验，已继续使用「%s」。":     "The config of “%s” failed validation; “%s” stays in use.",
		"「%s」的配置未通过校验，当前配置保持不变。":      "The config of “%s” failed validation; the current config is unchanged.",
		"配置未通过校验，当前配置保持不变。":           "The config failed validation; the current config is unchanged.",

		// internal/view/shell_sidebar.go
		"页面":       "Pages",
		"状态：回到概览":  "Status: back to Overview",
		"运行中 · %s": "Running · %s",

		// internal/view/shell_update.go
		"澜流有新版本 %s": "Lanflow %s is available",
		"在「设置 › 常规 › 关于」中下载并重启。": "Download and restart in Settings › General › About.",
		"前往设置": "Open Settings",

		// internal/view/shortcuts.go
		"「%s」测速完成":     "\"%s\" delay test done",
		"该动作的快捷键保持关闭。": "The shortcut for this action stays off.",
		"没有可测速的代理组":    "No proxy group to test",

		// internal/view/tray.go
		"● 运行中 · ↓ %s": "● Running · ↓ %s",
		"◌ 启动中…":       "◌ Starting…",
		"● 内核异常":       "● Core error",
		"● 未找到内核":      "● Core not found",
		"○ 未启动":        "○ Not running",
		"无":            "None",
		"订阅：%s":        "Subscription: %s",
		"打开澜流":         "Open Lanflow",
	} {
		en[k] = v
	}
}
