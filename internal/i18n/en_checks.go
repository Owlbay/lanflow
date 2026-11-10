package i18n

// English for the extended 泄漏检查 (internal/app/leak.go, iptype.go and
// the leak section of internal/view/netpage/page.go): the WebRTC / STUN
// check, the IP-type item and the DNS comparison. Kept apart from en.go,
// which another owner maintains.
func init() {
	for k, v := range map[string]string{
		// ---- internal/app/leak.go: names and rows ----
		"IP 类型":       "IP Type",
		"DNS 对比":      "DNS Compare",
		"STUN 看到":     "STUN saw",
		"本机公网":        "Local public",
		"系统解析器":       "System resolver",
		"公共 DNS %s":   "Public DNS %s",
		"%s：":         "%s: ",
		"失败（%v）":      "failed (%v)",
		"无结果":         "no answer",
		"出口 %s":       "Exit %s",
		"机房 IP":       "data-centre IP",
		"家宽 / 运营商 IP": "residential / ISP IP",
		"类型未知":        "type unknown",

		// ---- WebRTC / STUN ----
		"STUN 请求没有得到响应": "No STUN server answered",
		"直接发出的 UDP 探测没有收到任何公共 STUN 服务器的回应：UDP 被网络拦截或 STUN 服务不可达。浏览器的 WebRTC 此时同样拿不到公网地址，不会借此暴露真实 IP。": "A UDP probe sent directly got no reply from any public STUN server: the network blocks UDP, or the STUN servers are unreachable. A browser's WebRTC cannot learn the public address this way either, so it does not reveal the real IP.",
		"WebRTC（UDP）流量经过澜流，出口 %s": "WebRTC (UDP) traffic goes through Lanflow, exit %s",
		"STUN 服务器看到的是代理出口地址，说明直接发出的 UDP 已被接管；浏览器的 WebRTC 不会暴露真实 IP。": "The STUN server saw the proxy's exit address, so directly sent UDP is captured; a browser's WebRTC does not reveal the real IP.",
		"WebRTC 暴露了真实 IP（%s）": "WebRTC reveals the real IP (%s)",
		"TUN 已开启，但直接发出的 UDP 仍从本机公网地址出去，没有经过代理。请检查节点是否支持 UDP、订阅或覆写中是否禁用了 UDP 转发（udp: false），以及 fake-ip-filter 是否放行了 STUN 域名。": "TUN is on, yet directly sent UDP still leaves from this machine's public address instead of the proxy. Check that the node supports UDP, that the subscription or an override does not disable UDP forwarding (udp: false), and whether fake-ip-filter exempts STUN domains.",
		"浏览器的 WebRTC 会暴露真实 IP（%s）": "A browser's WebRTC reveals the real IP (%s)",
		"系统代理模式只覆盖 TCP 的 HTTP/HTTPS 请求，而浏览器的 WebRTC 直接用 UDP 向 STUN 服务器询问公网地址，网站可借此拿到真实 IP。":                                                                                                  "System-proxy mode only covers HTTP/HTTPS over TCP, while a browser's WebRTC asks STUN servers for the public address directly over UDP, so a website can learn the real IP.",
		"要堵住这个口子，请开启 TUN 模式让 UDP 也经过澜流；或在浏览器中限制 WebRTC：Chrome/Edge 可安装 WebRTC 控制类扩展（或 uBlock Origin 的「阻止 WebRTC 泄漏本地 IP」），Firefox 可在 about:config 中把 media.peerconnection.enabled 设为 false。": "To close this gap, turn on TUN Mode so UDP also goes through Lanflow, or limit WebRTC in the browser: Chrome/Edge can use a WebRTC-control extension (or uBlock Origin's “Prevent WebRTC from leaking local IP addresses”), and Firefox can set media.peerconnection.enabled to false in about:config.",
		"STUN 看到的地址是 %s，但无法取得代理出口和本机公网地址作对比":                                                 "STUN saw %s, but neither the proxy exit nor the local public address is known to compare with",
		"请稍后重新检查；若本机公网地址一直无法获取，请点击网络页上方的「重新测试」。":                                             "Check again later; if the local public address never arrives, click “Retest” at the top of the Network page.",
		"STUN 看到的地址（%s）既不是代理出口也不是本机公网地址":                                                     "The address STUN saw (%s) is neither the proxy exit nor the local public address",
		"UDP 可能从另一条线路（如节点的另一出口、运营商 NAT 的不同地址或 IPv6）出去。如果它属于代理节点，则 WebRTC 没有泄漏；否则请开启 TUN 模式。": "UDP may leave by another path (another exit of the node, a different carrier NAT address, or IPv6). If it belongs to the proxy node, WebRTC does not leak; otherwise turn on TUN Mode.",

		// ---- IP 类型 ----
		"无法获取代理出口地址": "Could not determine the proxy exit address",
		"经代理请求 Cloudflare 失败（%v）。请确认当前节点可用后重新检查。":                     "Requesting Cloudflare through the proxy failed (%v). Make sure the current node works, then check again.",
		"出口属于云服务 / 机房网络，流媒体和风控系统容易把它识别为代理；需要更「纯净」的出口时可换用家宽或原生 IP 节点。": "The exit is in a cloud / hosting network, which streaming sites and risk engines readily flag as a proxy; for a “cleaner” exit, use a residential or native-IP node.",
		"出口属于运营商 / 家宽网络，通常不易被识别为代理。":                                  "The exit is in an ISP / residential network and is usually not flagged as a proxy.",
		"ASN 数据库中没有这个地址的归属信息，无法判断类型；更新地理数据库后可再试。":                     "The ASN database has no record for this address, so its type cannot be judged; try again after updating the geo databases.",
		"「在浏览器中检查 IP 纯净度」会打开第三方网站做更细的风险评分，澜流本身不访问这些网站。":               "“Check IP Purity in Browser” opens a third-party site for a finer risk score; Lanflow itself never contacts such sites.",

		// ---- DNS 对比 ----
		"系统解析器对 %s 的结果与内核和公共 DNS 都不同": "The system resolver's answer for %s matches neither the core nor public DNS",
		"系统 DNS 返回的地址既不是内核解析到的，也不是公共 DNS 解析到的，可能被运营商劫持或污染。CDN 域名在不同地区本来就会返回不同地址，若该域名使用 CDN，这可能是正常现象；若是普通域名，建议开启 TUN 或「自动设置系统 DNS」让查询经过澜流。": "The address from system DNS is neither what the core nor what public DNS resolved: it may be hijacked or polluted by the carrier. CDN domains legitimately answer differently per region, so for a CDN domain this can be normal; for an ordinary domain, turn on TUN or “Set System DNS Automatically” so queries go through Lanflow.",
		"无法完成 %s 的解析对比": "Could not complete the comparison for %s",
		"至少一路解析没有返回结果（超时或被拦截）。直接发往公共 DNS 的 UDP 查询若总是超时，通常是网络拦截了 53 端口的外部查询。": "At least one resolver returned nothing (timeout or blocked). If UDP queries sent directly to public DNS always time out, the network usually blocks outside queries on port 53.",
		"系统解析器的结果与内核或公共 DNS 一致": "The system resolver agrees with the core or public DNS",
		"三路解析相互印证，未发现劫持或污染。内核与公共 DNS 的结果不同属正常现象（内核可能经代理在境外解析）。": "The three answers corroborate each other; no hijack or pollution found. The core and public DNS differing is normal (the core may resolve abroad through the proxy).",

		// ---- internal/view/netpage/page.go ----
		"系统解析器、强制 IPv6 的直连请求和直接发出的 STUN（UDP）各测一次，与内核和代理出口比较；并列出出口 IP 的归属与三路 DNS 结果": "Probes the system resolver, an IPv6-only direct request and a directly sent STUN (UDP) request once each and compares them with the core and the proxy exit; also lists the exit IP's network and three DNS answers side by side",
		"检查 DNS、IPv6 和 WebRTC 是否绕过代理，以及出口 IP 的类型":                                   "Check whether DNS, IPv6 and WebRTC bypass the proxy, and the exit IP's type",
		"附加对比的域名（可选）": "Extra domain to compare (optional)",
		"更细的 IP 风险评分（纯净度）可在「检测」页按需在线检测，也可在浏览器中查看": "A finer IP risk score (“purity”) is available on demand on the Diagnostics page, or in the browser",
		"在浏览器中检查 IP 纯净度": "Check IP Purity in Browser",
	} {
		en[k] = v
	}
}
