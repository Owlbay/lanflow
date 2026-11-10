package i18n

// English of the 日志 page (internal/view/logs).
func init() {
	for k, v := range map[string]string{
		// internal/view/logs/page.go
		"级别：%s":       "Level: %s",
		"暂无日志":        "No Logs Yet",
		"没有匹配“%s”的日志": "No log lines match “%s”",
		"回到底部":        "Back to Bottom",
		"（有新日志）":      " (new lines)",
		"搜索日志":        "Search logs",
		"级别":          "Level",
		"清空":          "Clear",
		"在资源管理器中显示日志文件": "Show Log File in Explorer",
		"在访达中显示日志文件":    "Show Log File in Finder",
		"复制全部":          "Copy All",
		"ZIP 压缩包":       "ZIP archive",
		"内核：Mihomo 的运行日志；澜流：澜流自己的操作记录、提示和错误": "Core: Mihomo's run log. Lanflow: Lanflow's own actions, notices and errors",
		"包含日志、运行时配置和网络状态；节点地址、密码和订阅令牌已去除。":   "Includes logs, the runtime config and the network state; node addresses, passwords and subscription tokens are removed.",
		"导出诊断信息": "Export Diagnostics",
		"日志来源":   "Log source",
		"澜流":     "Lanflow",
		"已暂停。按「继续」恢复接收新日志。":                      "Paused. Press Resume to receive new entries.",
		"澜流自身的操作和错误会记录在这里。":                      "Lanflow's own actions and errors are recorded here.",
		"内核运行时，连接、DNS 和错误记录会实时显示在这里；可用「级别」缩小范围。": "While the core runs, its connection, DNS and error entries appear here live; narrow them with Level.",
	} {
		en[k] = v
	}
}
