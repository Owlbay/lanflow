package i18n

// English of the 概览 page and its exit globe (internal/view/overview).
func init() {
	for k, v := range map[string]string{
		// internal/view/overview/exitmap.go
		"出口地图": "Exit Map",
		"实测出口": "Measure Exits",
		"经各出口访问 Cloudflare trace，测出真实出口位置": "Reach Cloudflare trace through each exit to find its real location",
		"实测中…":       "Measuring…",
		"查看经 %s 的连接": "Show connections via %s",
		"已更新出口位置":    "Exit locations updated",
		"你 · %s":     "You · %s",
		"你 · %s（推测）": "You · %s (guessed)",
		"。拖动可旋转地球，滚轮缩放，双击复位": ". Drag to turn the globe, scroll to zoom, double-click to reset.",
		"放大":   "Zoom in",
		"缩小":   "Zoom out",
		"复位视角": "Reset view",
		"地球全景": "Whole globe",

		// internal/view/overview/exitmap_list.go
		"直连模式": "Direct Mode",
		"流量不经过节点，无出口可显示": "Traffic bypasses the nodes; no exits to show",
		"暂无路由":         "No Routes",
		"当前出口":         "Current Exit",
		"其他路由":         "Other Routes",
		"%s：%s，在代理页查看": "%s: %s, see Proxies",
		"位置未知":         "Unknown location",
		"%s · %d 个连接":  "%s · %d connections",
		"经中转 · %s":     "Via relay · %s",

		// internal/view/overview/exitmap_routes.go
		"：从你所在的%s": ": from %s, where you are,",
		"到%s":      " to %s",
		"；目标国家：%s": "; destination countries: %s",

		// internal/view/overview/page.go
		"导入机场提供的订阅链接或 YAML 配置文件后即可开始使用。": "Import the subscription link or YAML config from your provider to get started.",
		"运行中":       "Running",
		" · 已运行 %s": " · up %s",
		"内核开关":      "Core switch",
		"Mihomo 内核意外退出，自动重启也失败了。系统代理已自动关闭，避免断网。": "The Mihomo core quit unexpectedly and the automatic restart failed. The system proxy was turned off to keep you online.",
		"Mihomo 内核意外退出：%s。系统代理已自动关闭，避免断网。":       "The Mihomo core quit unexpectedly: %s. The system proxy was turned off to keep you online.",
		"打开以让系统流量走代理，点击关闭提示":                     "Turn on to route system traffic through the proxy; click to dismiss",
		"打开以让系统流量走代理":                            "Turn on to route system traffic through the proxy",
		"系统代理开关":                                 "System proxy switch",
		"正在下载内核":                                 "Downloading Core",
		"未找到 Mihomo 内核：内核文件不存在或无法执行。可下载官方内核，或选择本机已有的内核文件。": "Mihomo core not found: the core file does not exist or cannot run. Download the official core, or choose one already on this machine.",
		"未找到 Mihomo 内核：%s":             "Mihomo core not found: %s",
		"选择内核文件":                       "Choose Core File",
		"已选择内核文件":                      "Core file chosen",
		"当前订阅，打开订阅页":                   "Current subscription, opens Subscriptions",
		"尚未选择订阅":                       "No subscription selected",
		"无流量信息":                        "No traffic information",
		"已用 %s / %s GB":                "%s / %s GB used",
		"本地文件 · 无流量信息":                 "Local file · no traffic information",
		"到期 %s":                        "Expires %s",
		"实时速度":                         "Live Speed",
		"本次 ↑ %s ↓ %s · 连接 %d · 内存 %s": "Session ↑ %s ↓ %s · %d connections · %s memory",
		"60 秒前":                        "60 s ago",
		"现在":                           "now",
		"终端代理":                         "Terminal Proxy",
		"复制到剪贴板":                       "Copy to clipboard",
		"TUN 模式开关":                     "TUN mode switch",
		"接管所有应用的流量":                    "Captures the traffic of every app",
		"首次开启需安装权限组件":                  "The first time needs the helper installed",
	} {
		en[k] = v
	}
}
