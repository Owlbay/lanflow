package i18n

// English of the 订阅 page and its dialogs (internal/view/subs).
func init() {
	for k, v := range map[string]string{
		// internal/view/subs/dialogs.go
		"每 6 小时":    "Every 6 hours",
		"订阅配置":      "Subscription config",
		"查看配置 · %s": "View Config · %s",
		"订阅下载到本地的原始 YAML（只读，未应用覆写）。%s 查找。": "The raw YAML as downloaded (read-only, overrides not applied). %s to find.",
		"正在读取…":                "Loading…",
		"从链接导入订阅":              "Import Subscription from Link",
		"订阅链接":                 "Subscription link",
		"名称（可选）":               "Name (optional)",
		"留空则使用订阅提供的名称":         "Leave empty to use the name the subscription provides",
		"订阅名称":                 "Subscription name",
		"导入后立即使用":              "Use right after import",
		"高级：User-Agent、通过代理下载": "Advanced: User-Agent, download via proxy",
		"留空使用默认 User-Agent":    "Leave empty for the default User-Agent",
		"通过代理下载":               "Download via proxy",
		"开始导入":                 "Start import",
		"正在校验…":                "Validating…",
		"请输入以 http:// 或 https:// 开头的订阅链接。": "Enter a subscription link starting with http:// or https://.",
		"已导入 %s，但内核启动失败：%s":                "Imported %s, but the core failed to start: %s",
		"已导入并启动":     "Imported and started",
		"已导入并切换到 %s": "Imported and switched to %s",
		"订阅已导入":      "Subscription imported",
		"编辑订阅":       "Edit Subscription",
		"通过代理更新":     "Update via proxy",
		"请填写名称。":     "Enter a name.",
		"下载超时（秒）":    "Download timeout (s)",
		"留空为 %d 秒":   "Empty = %d s",
		"下载超时须为 1–%d 之间的秒数，或留空。": "The download timeout must be 1–%d seconds, or empty.",

		// internal/view/subs/page.go
		"从文件导入订阅":                     "Import Subscription from File",
		"全部更新":                        "Update All",
		"更新所有链接订阅（⌘R）":                "Update every link subscription (⌘R)",
		"也可以把 .yaml / .yml 文件拖到这个窗口。": "You can also drop a .yaml / .yml file onto this window.",
		"从链接导入":                       "Import from Link",
		"双击切换订阅 · 可将 YAML 文件拖到此处导入":   "Double-click to switch · drop a YAML file here to import",
		"已用 / 总量":                     "Used / Total",
		"到期":                          "Expires",
		"更新于":                         "Updated",
		"切换中":                         "Switching",
		"切换中…":                        "Switching…",
		"更新失败":                        "Update failed",
		"使用此订阅":                       "Use This Subscription",
		"更新（重新读取文件）":                  "Update (Re-read File)",
		"编辑覆写…":                       "Edit Override…",
		"查看配置":                        "View Config",
		"请先切换到其他订阅":                   "Switch to another subscription first",
		"删除「%s」？":                     "Delete “%s”?",
		"此操作无法撤销。":                    "This cannot be undone.",
		"已重新读取「%s」":                   "Re-read “%s”",
		"订阅已更新":                       "Subscription updated",
		"打开官网":                        "Open Website",
		"更新所有链接订阅（Ctrl+R）":            "Update every link subscription (Ctrl+R)",
	} {
		en[k] = v
	}
}
