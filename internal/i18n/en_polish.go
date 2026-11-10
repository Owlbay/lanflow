package i18n

// English for the interface polish pass (empty and start states, toolbar
// options, platform-aware shortcut hints). Kept apart from en.go, which
// another owner maintains.
func init() {
	for k, v := range map[string]string{
		// ---- internal/view/proxies/page.go ----
		"%d 个代理集合":           "%d proxy sets",
		"依次测试所有组的延迟（Ctrl+R）": "Test the latency of every group in turn (Ctrl+R)",

		// ---- internal/view/subs/page.go ----
		"更新所有链接订阅（Ctrl+R）": "Update every link subscription (Ctrl+R)",

		// ---- internal/view/conns/page.go ----
		"拓扑选项": "Topology options",

		// ---- internal/view/logs/page.go ----
		"已暂停。按「继续」恢复接收新日志。":                      "Paused. Press Resume to receive new entries.",
		"澜流自身的操作和错误会记录在这里。":                      "Lanflow's own actions and errors are recorded here.",
		"内核运行时，连接、DNS 和错误记录会实时显示在这里；可用「级别」缩小范围。": "While the core runs, its connection, DNS and error entries appear here live; narrow them with Level.",

		// ---- internal/view/netpage/page.go ----
		"尚未检查": "Not checked yet",
		"正在比较系统解析器、内核和代理出口看到的地址。":             "Comparing the addresses seen by the system resolver, the core and the proxy exit.",
		"启动内核后可以检查 DNS、IPv6 和 WebRTC 是否绕过代理。": "Start the core to check whether DNS, IPv6 and WebRTC bypass the proxy.",

		// ---- internal/view/probe/page.go ----
		"按需检测，不会自动运行": "Checks run on demand only",
		"IP 纯净度读取出口的地址、位置、运营商和风险评分；解锁检测访问常用服务各一次；地区探查对比同一地区各节点的延迟和真实出口。只在你按下按钮时发出请求。": "IP purity reads the exit's address, location, carrier and risk score; the unlock check visits each popular service once; the region probe compares the latency and real exit of every node in a region. Requests are sent only when you press a button.",
		"尚未检测":     "Not checked yet",
		"尚未探查":     "Not probed yet",
		"没有可探查的地区": "No region to probe",
		"地区按节点名称判断（如「香港 01」「JP Tokyo」）。订阅里的节点名都不含地区时，这里没有可选项。": "Regions are read from node names (such as \"HK 01\" or \"JP Tokyo\"). When no node name in the subscription carries a region, there is nothing to choose here.",
	} {
		en[k] = v
	}
}
