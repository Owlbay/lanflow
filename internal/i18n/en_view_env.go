package i18n

// English of the shared formatting and page helpers (internal/view/env).
func init() {
	for k, v := range map[string]string{
		// internal/view/env/env.go
		"规则|页":            "Rules",
		"正在下载 Mihomo %s…": "Downloading Mihomo %s…",
		" %s / %s（%d%%）":  " %s / %s (%d%%)",
		"正在校验 Mihomo %s…": "Verifying Mihomo %s…",
		"正在安装 Mihomo %s…": "Installing Mihomo %s…",
		"正在启动新内核…":        "Starting the new core…",
		"正在处理 Mihomo %s…": "Processing Mihomo %s…",

		// internal/view/env/format.go
		"%d 小时 %d 分": "%d h %d min",
		"%d 分钟":      "%d min",
		"刚刚":         "just now",
		"%d 分钟前":     "%d min ago",
		"今天 %s":      "Today %s",
		"昨天":         "Yesterday",
		"域名后缀":       "Domain Suffix",
		"完整域名":       "Domain",
		"域名关键字":      "Domain Keyword",
		"进程名":        "Process Name",
		"拒绝":         "Reject",

		// internal/view/env/tun.go
		"TUN 模式已关闭": "TUN Mode off",
		"TUN 模式已开启": "TUN Mode on",
		"安装权限组件":    "Install Helper",
		"TUN 模式需要一个随系统运行的权限组件来创建虚拟网卡。安装时会请求管理员权限，只需一次。": "TUN Mode needs a helper that runs with the system to create the virtual interface. Installing asks for administrator rights, once.",
		"安装并开启": "Install and Turn On",
		"权限组件已安装，但暂时无法连接。请稍后再试一次": "The helper is installed but cannot be reached right now. Try again in a moment",
		"权限组件已安装，TUN 模式已开启":       "Helper installed; TUN Mode on",
	} {
		en[k] = v
	}
}
