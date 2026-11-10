package i18n

// English of the exit path notes (internal/locate).
func init() {
	for k, v := range map[string]string{
		// internal/locate/analyze.go
		"%d 跳链式代理":                      "%d-hop chained proxy",
		"IP 归属%s，实际经%s机房":               "IP registered in %s, actually served from the %s data centre",
		"名称为%s，实测出口在%s":                 "Named %s, measured exit in %s",
		"延迟 %d ms 低于 %s→%s 的物理下限 %d ms": "Latency %d ms is below the physical floor for %s→%s, %d ms",
		// internal/locate/direct.go
		"找不到物理网卡，无法实测本机出口：%v":    "No physical network interface found; your own exit was not measured: %v",
		"无法绑定物理网卡 %s，未实测本机出口：%v": "Could not bind to the physical interface %s; your own exit was not measured: %v",
		"经物理网卡 %s 获取本机出口信息失败：%v": "Fetching your own exit over the physical interface %s failed: %v",
	} {
		en[k] = v
	}
}
