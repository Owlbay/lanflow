package i18n

// English of 设置 › 高级 (the full Mihomo option set: inbound
// ports and LAN access, PAC system proxy, TUN, core behaviour, DNS and
// sniffer extras) and of the PAC mode's service messages.
func init() {
	for k, v := range map[string]string{
		// tab and shared
		"高级":      "Advanced",
		"高级设置已应用": "Advanced settings applied",
		"这些选项对应 Mihomo 配置里的同名字段，留空即使用内核默认值；应用后会重新生成配置，大多数需要内核重新加载。": "These options map to the Mihomo config fields of the same name; empty means the core's default. Applying regenerates the config, and most of them reload the core.",
		"默认": "Default",
		// 关闭 as a setting's value; the bare word is the Close button.
		"关闭|开关": "Off",
		"严格":    "Strict",
		"总是":    "Always",

		// validation
		"%s须为 1–65535 之间的数字，或留空":              "%s must be a number from 1 to 65535, or empty",
		"%s不能与代理端口 %d 相同":                     "%s cannot equal the proxy port %d",
		"%s须为 %d–%d 之间的数字，或留空":                "%s must be a number from %d to %d, or empty",
		"%s：「%s」不是有效的 IP 或网段":                 "%s: \"%s\" is not a valid IP or range",
		"HTTP 端口和 SOCKS 端口不能相同":               "The HTTP and SOCKS ports cannot be the same",
		"代理认证：「%s」应写成 用户名:密码":                 "Authentication: \"%s\" should be user:password",
		"DNS 监听地址应写成 IP:端口，例如 127.0.0.1:1053": "The DNS listen address should be IP:port, e.g. 127.0.0.1:1053",

		// ports and LAN
		"端口与局域网":  "Ports and LAN",
		"HTTP 端口": "HTTP port",
		"额外的纯 HTTP 代理端口（port）；留空则只用代理端口": "An extra plain HTTP proxy port (port); empty = the proxy port only",
		"SOCKS 端口": "SOCKS port",
		"额外的纯 SOCKS5 代理端口（socks-port）": "An extra plain SOCKS5 proxy port (socks-port)",
		"允许的局域网地址":                     "Allowed LAN addresses",
		"允许局域网连接时，只有这些网段可以使用代理，每行一个（lan-allowed-ips）": "With LAN access on, only these ranges may use the proxy, one per line (lan-allowed-ips)",
		"禁止的局域网地址": "Blocked LAN addresses",
		"这些网段不能使用代理，每行一个（lan-disallowed-ips）": "These ranges may not use the proxy, one per line (lan-disallowed-ips)",
		"代理认证": "Authentication",
		"每行一个 用户名:密码；设置后 HTTP 和 SOCKS5 入口都要求登录（authentication）": "One user:password per line; once set, the HTTP and SOCKS5 inbounds require a login (authentication)",
		"免认证的地址": "No-auth addresses",
		"来自这些网段的连接不需要认证，例如 127.0.0.1/32（skip-auth-prefixes）": "Connections from these ranges need no login, e.g. 127.0.0.1/32 (skip-auth-prefixes)",
		"已允许局域网连接但没有设置代理认证：同一网络里的任何设备都能使用这个代理。":              "LAN access is on without authentication: any device on the network can use this proxy.",
		"没有设置代理认证：同一网络里的任何设备都能使用这个代理。":                       "No authentication is set: any device on the network can use this proxy.",
		"设置认证…": "Set up authentication…",

		// system proxy / PAC
		"PAC 模式": "PAC mode",
		"系统代理改为自动配置脚本：澜流在本机提供 PAC 文件，「不走代理的地址」由应用自行直连，其余经代理": "Sets the system proxy to an auto-config script: Lanflow serves a PAC file locally, applications connect to the bypass list directly and send the rest through the proxy",
		"国内域名直连": "Mainland domains direct",
		"PAC 中把 .cn 和常见国内网站设为直连，不经过内核": "The PAC sends .cn and well-known mainland sites direct, without the core",
		"PAC 端口": "PAC port",
		"PAC 文件的本机端口，留空为 %d":                              "Local port of the PAC file; empty = %d",
		"应用后系统代理会立即切换到新的方式。":                              "The system proxy switches to the new form as soon as you apply.",
		"开启 PAC 代理失败：%v。内核仍在运行，可以手动把系统代理指向 127.0.0.1:%d。": "Turning the PAC proxy on failed: %v. The core is still running; you can point the system proxy at 127.0.0.1:%d by hand.",
		"无法启动 PAC 服务：%v":                                  "Cannot start the PAC server: %v",
		"系统代理仍使用澜流的 PAC 文件。可以恢复为关闭，或保持开启并启动内核。":           "The system proxy still uses Lanflow's PAC file. Turn it off, or keep it and start the core.",
		"端口 %d 已被其他程序占用，请换一个端口。":                          "Port %d is in use by another program; pick another port.",

		// TUN
		"仅在 TUN 模式下生效": "Only in TUN mode",
		"严格路由":         "Strict route",
		"阻止绕过 TUN 的流量，防止泄漏；个别局域网设备可能无法访问（strict-route）": "Blocks traffic that bypasses the TUN to prevent leaks; a few LAN devices may become unreachable (strict-route)",
		"TUN 网卡的最大传输单元，576–65535；留空用内核默认值":              "Maximum transmission unit of the TUN interface, 576-65535; empty = the core's default",
		"端点无关 NAT": "Endpoint-independent NAT",
		"UDP 使用对端无关的映射，改善游戏和 P2P 的 NAT 类型（endpoint-independent-nat）": "UDP uses peer-independent mappings, improving the NAT type for games and P2P (endpoint-independent-nat)",
		"只接管这些网段": "Route only these ranges",
		"设置后只有这些网段进入 TUN，每行一个（route-address）": "When set, only these ranges enter the TUN, one per line (route-address)",
		"不接管的网段": "Exclude these ranges",
		"这些网段绕过 TUN，每行一个；与 aTrust 兼容加入的网段合并（route-exclude-address）": "These ranges bypass the TUN, one per line; merged with the ranges the aTrust mode adds (route-exclude-address)",
		"DNS 劫持": "DNS hijack",
		"截获到这些地址的 DNS 查询，每行一个；留空为 any:53 和 tcp://any:53（dns-hijack）": "Intercepts DNS queries to these addresses, one per line; empty = any:53 and tcp://any:53 (dns-hijack)",
		"网卡名称": "Interface name",
		"TUN 网卡的名称；留空用澜流固定的名称（device）": "Name of the TUN interface; empty = Lanflow's fixed name (device)",
		"只在这些网卡上接管":                    "Only on these interfaces",
		"每行一个网卡名（include-interface）":   "One interface name per line (include-interface)",
		"不在这些网卡上接管":                    "Not on these interfaces",
		"每行一个网卡名（exclude-interface）":   "One interface name per line (exclude-interface)",

		// core behaviour
		"内核行为": "Core Behaviour",
		"解析 AAAA 记录并允许经 IPv6 连接（ipv6）": "Resolves AAAA records and allows IPv6 connections (ipv6)",
		"统一延迟": "Unified delay",
		"测速结果扣除握手时间，不同协议的节点更可比（unified-delay）": "Delay tests subtract the handshake, so nodes of different protocols compare better (unified-delay)",
		"TCP 并发": "TCP concurrent",
		"域名有多个 IP 时同时尝试，用最快的一个（tcp-concurrent）": "Tries every IP of a host at once and keeps the fastest (tcp-concurrent)",
		"进程匹配": "Process matching",
		"查找连接所属的进程，供 PROCESS 规则和连接页使用；关闭可省资源（find-process-mode）": "Finds the process behind each connection for PROCESS rules and the connections page; off saves resources (find-process-mode)",
		"TCP 保活间隔": "TCP keep-alive interval",
		"秒；留空用内核默认值（keep-alive-interval）": "Seconds; empty = the core's default (keep-alive-interval)",
		"TCP 保活空闲": "TCP keep-alive idle",
		"秒；空闲多久后开始发送保活（keep-alive-idle）": "Seconds idle before keep-alives start (keep-alive-idle)",
		"禁用 TCP 保活": "Disable TCP keep-alive",
		"部分网络下可减少无效流量（disable-keep-alive）": "Cuts idle traffic on some networks (disable-keep-alive)",
		"出站网卡": "Outbound interface",
		"内核的出站流量绑定到这个网卡，例如 en0；留空自动选择（interface-name）": "Binds the core's outbound traffic to this interface, e.g. en0; empty = automatic (interface-name)",
		"地理数据加载": "Geodata loader",
		"省内存模式启动更快、占用更少（geodata-loader）": "Memory-conservative mode starts faster and uses less memory (geodata-loader)",
		"省内存":     "Memory-conservative",
		"标准":      "Standard",
		"禁用 ETag": "Disable ETag",
		"下载规则集和地理数据库时不带 ETag 校验（etag-support）": "Downloads rule sets and geo databases without ETag checks (etag-support)",
		"保存 Fake-IP 映射": "Store Fake-IP mappings",
		"重启后 Fake-IP 与域名的对应关系不变（profile.store-fake-ip）": "Fake-IP to domain mappings survive a restart (profile.store-fake-ip)",
		"下载 User-Agent": "Download User-Agent",
		"内核下载规则集、地理数据库时使用的 UA（global-ua）": "UA the core uses to download rule sets and geo databases (global-ua)",

		// DNS extras
		"DNS 高级": "DNS Advanced",
		"作用于当前生效的 DNS 设置，无论跟随订阅还是由澜流接管": "Applies to the DNS settings in force, whether from the subscription or Lanflow",
		"本机 DNS 监听": "Local DNS listener",
		"让内核在本机提供 DNS 服务，例如 127.0.0.1:1053（listen）": "Makes the core serve DNS locally, e.g. 127.0.0.1:1053 (listen)",
		"优先 HTTP/3": "Prefer HTTP/3",
		"DoH 服务器支持时用 HTTP/3 查询（prefer-h3）": "Queries DoH servers over HTTP/3 when they support it (prefer-h3)",
		"缓存算法": "Cache algorithm",
		"ARC 命中率更高，占用稍多（cache-algorithm）": "ARC hits more often and uses a little more memory (cache-algorithm)",
		"引导服务器": "Bootstrap servers",
		"用来解析其他 DNS 服务器域名的纯 IP 服务器，每行一个（default-nameserver）": "Plain-IP servers that resolve the other DNS servers' names, one per line (default-nameserver)",
		"备用服务器": "Fallback servers",
		"与上游并发查询，命中下面的过滤条件时采用备用结果，每行一个（fallback）": "Queried alongside the upstreams; their answer is used when the filters below match, one per line (fallback)",
		"备用服务器 · 按 GeoIP 过滤":                             "Fallback · GeoIP filter",
		"上游返回的 IP 不属于下方国家时改用备用结果（fallback-filter.geoip）": "Uses the fallback answer when the upstream IP is outside the country below (fallback-filter.geoip)",
		"GeoIP 国家代码":   "GeoIP country code",
		"备用服务器 · 直连网段": "Fallback · IP ranges",
		"上游结果落在这些网段时改用备用结果，每行一个（fallback-filter.ipcidr）": "Uses the fallback answer when the upstream IP is in these ranges, one per line (fallback-filter.ipcidr)",
		"备用服务器 · 域名": "Fallback · domains",
		"这些域名总是采用备用结果，每行一个（fallback-filter.domain）": "These domains always take the fallback answer, one per line (fallback-filter.domain)",

		// sniffer extras
		"嗅探高级":       "Sniffer Advanced",
		"不强制 DNS 映射": "No forced DNS mapping",
		"默认对 Fake-IP 或 DNS 映射到的连接总是嗅探；关闭后只按端口列表嗅探（force-dns-mapping）": "By default connections mapped through Fake-IP or DNS are always sniffed; on, only the port lists are (force-dns-mapping)",
	} {
		en[k] = v
	}
}
