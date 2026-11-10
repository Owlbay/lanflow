package i18n

// English of the 统计 page (internal/view/stats) and the statistics
// service (internal/app/stats.go).
func init() {
	for k, v := range map[string]string{
		// ---- internal/app/stats.go ----
		"重置统计失败：%v": "Failed to reset the statistics: %v",

		// ---- internal/view/env/env.go ----
		"统计": "Statistics",

		// ---- internal/view/stats/page.go ----
		"今天":     "Today",
		"本周":     "This Week",
		"本月":     "This Month",
		"设备":     "Device",
		"代理|统计":  "Proxy",
		"地区":     "Region",
		"入站":     "Inbound",
		"请求数":    "Requests",
		"上传":     "Upload",
		"总计":     "Total",
		"占比":     "Share",
		"最近活动":   "Last Seen",
		"自 %s 起": "Since %s",
		"时间范围":   "Time range",
		"重置统计":   "Reset Statistics",
		"重置统计？":  "Reset the statistics?",
		"将删除所有已记录的流量统计，且无法恢复。": "Every recorded traffic statistic will be deleted. This cannot be undone.",
		"重置":     "Reset",
		"统计已重置":  "Statistics reset",
		"设备数":    "Devices",
		"暂无统计数据": "No statistics yet",
		"内核运行时，经过澜流的流量会按设备、域名、进程等在这里汇总。": "While the core runs, the traffic through Lanflow is summed up here by device, domain, process and more.",
		"最近 30 天":                 "Last 30 Days",
		"今天（按小时）":                 "Today (by Hour)",
		"峰值 ":                     "Peak ",
		"%s · ↓ %s ↑ %s · %d 次请求": "%s · ↓ %s ↑ %s · %d requests",
		"统计维度":                    "Dimension",
		"完整主机名":                   "Full host names",
		"搜索统计":                    "Search statistics",
		"没有匹配“%s”的项目":             "Nothing matches \"%s\"",
		"此范围内暂无数据":                "No data in this range",
		"复制名称":                    "Copy Name",
		"其他":                      "Other",
		"本机":                      "This Mac",
		"未知进程":                    "Unknown process",
	} {
		en[k] = v
	}
}
