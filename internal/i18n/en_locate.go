package i18n

// English of the exit path notes (internal/locate).
func init() {
	for k, v := range map[string]string{
		// internal/locate/analyze.go
		"%d 跳链式代理":                      "%d-hop chained proxy",
		"IP 归属%s，实际经%s机房":               "IP registered in %s, actually served from the %s data centre",
		"名称为%s，实测出口在%s":                 "Named %s, measured exit in %s",
		"延迟 %d ms 低于 %s→%s 的物理下限 %d ms": "Latency %d ms is below the physical floor for %s→%s, %d ms",
	} {
		en[k] = v
	}
}
