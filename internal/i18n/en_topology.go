package i18n

// English of the 拓扑 view of the 连接 page (internal/view/conns/topology.go,
// topology_graph.go), kept apart from en.go.
func init() {
	for k, v := range map[string]string{
		"拓扑":          "Topology",
		"连接视图":        "Connections view",
		"速度":          "Speed",
		"流量":          "Traffic",
		"线宽依据":        "Band width by",
		"含已关闭":        "Include closed",
		"分端口":         "By port",
		"目标":          "Destination",
		"其他":          "Other",
		"其他 %d 项":     "%d others",
		"未知进程":        "Unknown process",
		"网络拓扑":        "Network topology",
		" · 点击在列表中查看": " · Click to show in the list",
		"%d 个连接 · %s": "%d connections · %s",
		"%d 个连接 · ↓ %s ↑ %s · 共 %s":      "%d connections · ↓ %s ↑ %s · %s total",
		"%d 个连接的流向，从来源经进程、规则、代理组、节点到目标。": "Where %d connections flow, from source through process, rule, proxy group and node to destination.",
		"节点：": "Nodes: ",
		"有连接经过澜流时，这里会画出它们从设备到目标的路径。": "When connections go through Lanflow, their paths from the device to the destination are drawn here.",
	} {
		en[k] = v
	}
}
