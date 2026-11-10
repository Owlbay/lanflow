package i18n

// English for the fixes from the VM end-to-end audit (build/vm-e2e-report.md):
// the IPv6 leak check that reads the routing table (internal/app/leak.go),
// the core update check that follows the app's update switch
// (internal/view/settings/cores.go) and the IPv6 setting's description
// (internal/view/settings/advanced.go). Kept apart from en.go, which another
// owner maintains.
func init() {
	for k, v := range map[string]string{
		// ---- internal/app/leak.go: IPv6 ----
		"澜流默认让 TUN 同时接管 IPv6；请检查订阅或覆写中是否设置了 ipv6: false、改动了 tun.inet6-address 或 tun.route-address，改好后重启内核再检查。临时的办法是在系统网络设置中关闭 IPv6。": "Lanflow's TUN takes IPv6 as well by default; check whether the subscription or an override sets ipv6: false or changes tun.inet6-address / tun.route-address, then restart the core and check again. As a stopgap, turn IPv6 off in the system's network settings.",
		"IPv6 路由绕过了 TUN（%s）": "IPv6 route bypasses the TUN (%s)",
		"本机经 %s 有 IPv6 默认路由，而 TUN 没有接管 IPv6（::/0 不经过 TUN）。支持 IPv6 的网站会被应用直接连接，不经过代理；本次经 IPv6 访问外网虽然失败（%v），数据包仍已直接发出。": "This machine has an IPv6 default route through %s, and the TUN does not cover IPv6 (::/0 does not go through it). Apps connect to IPv6-capable sites directly, bypassing the proxy; this IPv6 request failed (%v), but its packets still left directly.",
		"IPv6 已由 TUN 接管，当前无法经 IPv6 访问外网": "The TUN owns IPv6; no IPv6 connectivity through it right now",
		"TUN 持有 IPv6 默认路由，应用的 IPv6 连接都会进入内核；本次经 IPv6 访问外网失败（%v），通常是代理节点或上游不支持 IPv6，应用会回退到 IPv4。": "The TUN holds the IPv6 default route, so every IPv6 connection of every app enters the core; this IPv6 request failed (%v), which usually means the proxy node or its upstream has no IPv6, and apps fall back to IPv4.",
		"IPv6 流量经过澜流，出口 %s": "IPv6 traffic goes through Lanflow, exit %s",
		"TUN 持有 IPv6 默认路由，强制走 IPv6 的请求进入了内核；它从 %s 出去，与 IPv4 代理出口（%s）不同，是节点自己的 IPv6 出口。": "The TUN holds the IPv6 default route and the request forced over IPv6 entered the core; it left from %s, which differs from the IPv4 proxy exit (%s): the node's own IPv6 exit.",
		// ---- internal/app/leak.go: DNS (the upstream Fake-IP case from the same audit) ----
		"系统 DNS 的 Fake-IP 不是澜流分配的": "The system DNS's Fake-IP was not assigned by Lanflow",
		"系统解析器返回了 Fake-IP（%s），但 TUN 未开启、系统 DNS（%s）也不指向澜流，这些地址来自上游网络中的另一个代理（如路由器或宿主机上的代理客户端）。应用的域名查询没有经过澜流。": "The system resolver returned Fake-IPs (%s), but TUN is off and the system DNS (%s) does not point at Lanflow: these addresses come from another proxy upstream (a router, or the proxy client of a VM's host). Apps' DNS queries do not go through Lanflow.",

		// ---- internal/view/settings/cores.go ----
		"应用设置里的「自动检查更新」关闭时，内核也不会自动检查": "Not checked automatically while the app's own automatic update check is off",
	} {
		en[k] = v
	}
}
