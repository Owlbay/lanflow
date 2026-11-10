package i18n

// English of the 连接 page and its topology (internal/view/conns).
func init() {
	for k, v := range map[string]string{
		// internal/view/conns/page.go
		"主机":       "Host",
		"链路":       "Chain",
		"↓ 速度":     "↓ Speed",
		"↑ 速度":     "↑ Speed",
		"↓ 总计":     "↓ Total",
		"时长":       "Duration",
		"显示全部列":    "Show All Columns",
		"%d 个活动连接": "%d active connections",
		"启动内核后这里会实时显示经过澜流的连接。": "Once the core runs, connections through Lanflow show here live.",
		"搜索连接":         "Search connections",
		"活动":           "Active",
		"已关闭":          "Closed",
		"连接列表":         "Connection list",
		"全部断开":         "Close All",
		"断开全部 %d 个连接？": "Close all %d connections?",
		"应用会自动重新建立需要的连接。": "Apps reconnect on their own as needed.",
		"断开":          "Close",
		"已断开全部连接":     "All connections closed",
		"没有匹配“%s”的连接": "No connections match “%s”",
		"暂无已关闭连接":     "No Closed Connections",
		"断开的连接会在这里保留最近 500 条，仅保存在内存中。": "The last 500 closed connections are kept here, in memory only.",
		"暂无活动连接":    "No Active Connections",
		"此域名始终走…":   "Always Route This Domain via…",
		"此程序始终走…":   "Always Route This App via…",
		"复制主机":      "Copy Host",
		"关闭详情":      "Close details",
		" · 已关闭 %s": " · closed %s",
		"源地址":       "Source",
		"目标地址":      "Destination",
		"目标 IP":     "Destination IP",
		"实际连接":      "Actual connection",
		"规则载荷":      "Rule payload",
		"%s · 共 %s": "%s · %s total",
		"断开此连接":     "Close This Connection",
		"拓扑选项":      "Topology options",
		"拓扑":        "Topology",
		"连接视图":      "Connections view",
		"速度":        "Speed",
		"流量":        "Traffic",
		"线宽依据":      "Band width by",
		"含已关闭":      "Include Closed",
		"分端口":       "By Port",

		// internal/view/conns/topology.go
		"网络拓扑":                      "Network topology",
		" · 点击在列表中查看":               " · Click to show in the list",
		"%d 个连接 · %s":               "%d connections · %s",
		"%d 个连接 · ↓ %s ↑ %s · 共 %s": "%d connections · ↓ %s ↑ %s · %s total",
		"%d 个连接的流向，从来源经进程、规则、代理组、节点到目标。": "Where %d connections flow, from source through process, rule, proxy group and node to destination.",
		"节点：%s": "Nodes: %s",
		"有连接经过澜流时，这里会画出它们从设备到目标的路径。": "When connections go through Lanflow, their paths from the device to the destination are drawn here.",

		// internal/view/conns/topology_graph.go
		"目标":      "Destination",
		"其他 %d 项": "%d others",
	} {
		en[k] = v
	}
}
