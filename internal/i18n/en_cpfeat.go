package i18n

// English of the profile and proxy conveniences: the ordered override list,
// delay-test settings, group icons and 定位当前节点, 切换节点时断开该组的连接,
// subscription extras and the tray additions.
func init() {
	for k, v := range map[string]string{
		// override list (view/settings/override.go)
		"列表中的覆写按顺序应用：全局的作用于所有订阅，其余由各订阅自行选择。": "The overrides in the list apply in order: global ones to every subscription, the others to the subscriptions that pick them.",
		"覆写列表":       "Overrides",
		"按从上到下的顺序应用": "Applied from top to bottom",
		"还没有覆写":      "No overrides yet",
		"新建…":        "New…",
		"从文件导入覆写":    "Import Override from File",
		"从链接导入覆写":    "Import Override from Link",
		"下载链接指向的 YAML 作为一条覆写；之后可在它的菜单中「更新」。": "Downloads the YAML the link points at as an override; its menu can update it later.",
		"覆写链接":        "Override link",
		"名称（留空则用文件名）": "Name (file name when empty)",
		"覆写名称":        "Override name",
		"新建覆写":        "New Override",
		"重命名覆写":       "Rename Override",
		"创建":          "Create",
		"已重命名":        "Renamed",
		"重命名…":        "Rename…",
		"下移":          "Move Down",
		"启用 %s":       "Enable %s",
		" · 链接":       " · link",
		"删除覆写「%s」？":   "Delete the override \"%s\"?",
		"使用它的订阅会重新生成配置。此操作无法撤销。": "Subscriptions using it regenerate their config. This cannot be undone.",
		"已导入「%s」": "Imported \"%s\"",
		"请输入以 http:// 或 https:// 开头的链接。": "Enter a link starting with http:// or https://.",
		"订阅使用的覆写":                        "Overrides per Subscription",
		"每个订阅可以额外选择要应用的覆写；全局覆写始终应用":      "Each subscription can pick extra overrides to apply; global overrides always apply",
		"选择…":       "Choose…",
		"选择… %s":    "Choose… %s",
		"选择覆写 · %s": "Choose Overrides · %s",
		"勾选的覆写按列表顺序应用于这个订阅；全局覆写始终应用。": "The checked overrides apply to this subscription in list order; global overrides always apply.",
		"%s（全局）":    "%s (global)",
		"仅全局覆写":     "Global overrides only",
		"全局覆写 + %s": "Global overrides + %s",
		// override list (app)
		"覆写不存在，可能已被删除。": "The override does not exist; it may have been deleted.",
		" 覆写":      " override",
		"请填写覆写名称。": "Enter a name for the override.",
		"覆写链接无效。请输入以 http:// 或 https:// 开头的完整链接。": "Invalid override link. Enter a full link starting with http:// or https://.",
		"下载覆写失败：%v":          "Downloading the override failed: %v",
		"删除覆写失败：%v":          "Deleting the override failed: %v",
		"调整覆写顺序失败：%v":        "Reordering the override failed: %v",
		"这条覆写不是从链接导入的，无法更新。": "This override was not imported from a link, so it cannot be updated.",
		"覆写未更新：%v":           "Override not updated: %v",
		"%d 条覆写":             "%d overrides",
		"覆写「%s」（%s）":         "override \"%s\" (%s)",

		// delay test (view/settings/delay.go)
		"延迟测试": "Delay Test",
		"代理页、代理组和菜单栏的测速都用这里的地址和超时": "The proxies page, the groups and the menu bar test delays with this URL and timeout",
		"测速地址": "Test URL",
		"节点经代理访问该地址，默认 www.gstatic.com/generate_204": "Nodes fetch this URL through the proxy; default www.gstatic.com/generate_204",
		"测速超时": "Test timeout",
		"毫秒，%d–%d；超过即显示为超时":               "Milliseconds, %d–%d; slower nodes show as timed out",
		"请输入以 http:// 或 https:// 开头的完整地址": "Enter a full URL starting with http:// or https://",
		"已更新测速地址":                         "Test URL updated",
		"超时须为 %d–%d 之间的毫秒数":               "The timeout must be %d–%d milliseconds",
		"测速超时已改为 %d 毫秒":                   "Test timeout set to %d ms",
		"切换节点时断开该组的连接":                    "Disconnect the group's connections on switch",
		"切换后立即断开经过该代理组的连接，让它们改走新节点":       "Closes the connections going through the group right after a switch, so they reconnect through the new node",

		// proxies page
		"定位当前节点":    "Locate Current Node",
		"定位当前节点 %s": "Locate current node of %s",
		// subscriptions
		"打开官网":     "Open Website",
		"下载超时（秒）":  "Download timeout (s)",
		"留空为 %d 秒": "Empty = %d s",
		"下载超时须为 1–%d 之间的秒数，或留空。": "The download timeout must be 1–%d seconds, or empty.",
		"下载超时须为 1–%d 秒之间的数字。":    "The download timeout must be a number of seconds from 1 to %d.",

		// tray
		"测试当前组延迟":  "Test Current Group Delay",
		"「%s」测速完成": "\"%s\" delay test done",
	} {
		en[k] = v
	}
}
