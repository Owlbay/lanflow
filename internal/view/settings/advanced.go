package settings

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// The 高级 tab (docs/design.md §5.8 高级栏): Mihomo's remaining
// inbound, system proxy, TUN, DNS and sniffer options, kept out of the
// everyday tabs. Edits collect in advForm and apply with the sticky
// 应用 bar, like DNS and 嗅探: most of them regenerate the config and
// reload the core (the service patches the few it can apply hot). Empty
// fields mean "the core's default", so nothing changes for users who
// never open the tab.

// advForm is the editable copy of model.AdvancedSettings, lists as
// multi-line texts and numbers as text, so the struct stays comparable.
type advForm struct {
	httpPort, socksPort                       string
	lanAllowed, lanDisallowed, auth, skipAuth string
	pacMode, pacDirectCN                      bool
	pacPort                                   string
	tunStrict, tunEIN                         bool
	tunMTU, tunRoute, tunExclude, tunHijack   string
	tunDevice, tunInclude, tunExclIf          string
	ipv6, unifiedDelay, tcpConcurrent         bool
	findProcess                               string
	keepAliveInterval, keepAliveIdle          string
	disableKeepAlive                          bool
	ifaceName, geodataLoader                  string
	disableETag, storeFakeIP                  bool
	globalUA                                  string
	dnsListen                                 string
	dnsPreferH3                               bool
	dnsCache, dnsDefaultNS, dnsFallback       string
	ffGeoIP                                   bool
	ffCode, ffCIDR, ffDomain                  string
	snfNoForceMapping                         bool
}

func optInt(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func advFormFrom(a model.AdvancedSettings) advForm {
	return advForm{
		httpPort: optInt(a.HTTPPort), socksPort: optInt(a.SocksPort),
		lanAllowed: joinLines(a.LANAllowedIPs), lanDisallowed: joinLines(a.LANDisallowedIPs),
		auth: joinLines(a.Authentication), skipAuth: joinLines(a.SkipAuthPrefixes),
		pacMode: a.PACMode, pacDirectCN: a.PACDirectCN, pacPort: optInt(a.PACPort),
		tunStrict: a.TUNStrictRoute, tunEIN: a.TUNEndpointIndependentNAT, tunMTU: optInt(a.TUNMTU),
		tunRoute: joinLines(a.TUNRouteAddress), tunExclude: joinLines(a.TUNRouteExcludeAddress), tunHijack: joinLines(a.TUNDNSHijack),
		tunDevice: a.TUNDevice, tunInclude: joinLines(a.TUNIncludeInterface), tunExclIf: joinLines(a.TUNExcludeInterface),
		ipv6: a.IPv6, unifiedDelay: a.UnifiedDelay, tcpConcurrent: a.TCPConcurrent, findProcess: a.FindProcessMode,
		keepAliveInterval: optInt(a.KeepAliveInterval), keepAliveIdle: optInt(a.KeepAliveIdle), disableKeepAlive: a.DisableKeepAlive,
		ifaceName: a.InterfaceName, geodataLoader: a.GeodataLoader, disableETag: a.DisableETag, storeFakeIP: a.StoreFakeIP, globalUA: a.GlobalUA,
		dnsListen: a.DNSListen, dnsPreferH3: a.DNSPreferH3, dnsCache: a.DNSCacheAlgorithm,
		dnsDefaultNS: joinLines(a.DNSDefaultNameserver), dnsFallback: joinLines(a.DNSFallback),
		ffGeoIP: a.DNSFallbackFilter.GeoIP, ffCode: a.DNSFallbackFilter.GeoIPCode,
		ffCIDR: joinLines(a.DNSFallbackFilter.IPCIDR), ffDomain: joinLines(a.DNSFallbackFilter.Domain),
		snfNoForceMapping: a.SnifferNoForceDNSMapping,
	}
}

// toSettings validates the form against mixedPort and returns the
// settings; the error names the first field that is wrong.
func (f advForm) toSettings(mixedPort int) (model.AdvancedSettings, error) {
	var a model.AdvancedSettings
	var err error
	port := func(label, text string) int {
		if err != nil {
			return 0
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return 0
		}
		n, e := strconv.Atoi(text)
		if e != nil || n < 1 || n > 65535 {
			err = fmt.Errorf(i18n.T("%s须为 1–65535 之间的数字，或留空"), label)
			return 0
		}
		if n == mixedPort {
			err = fmt.Errorf(i18n.T("%s不能与代理端口 %d 相同"), label, mixedPort)
			return 0
		}
		return n
	}
	num := func(label, text string, min, max int) int {
		if err != nil {
			return 0
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return 0
		}
		n, e := strconv.Atoi(text)
		if e != nil || n < min || n > max {
			err = fmt.Errorf(i18n.T("%s须为 %d–%d 之间的数字，或留空"), label, min, max)
			return 0
		}
		return n
	}
	cidrList := func(label, text string) []string {
		if err != nil {
			return nil
		}
		ls := lines(text)
		for _, l := range ls {
			if _, e := netip.ParsePrefix(l); e == nil {
				continue
			}
			if _, e := netip.ParseAddr(l); e == nil {
				continue
			}
			err = fmt.Errorf(i18n.T("%s：「%s」不是有效的 IP 或网段"), label, l)
			return nil
		}
		return ls
	}
	a.HTTPPort = port(i18n.T("HTTP 端口"), f.httpPort)
	a.SocksPort = port(i18n.T("SOCKS 端口"), f.socksPort)
	if err == nil && a.HTTPPort != 0 && a.HTTPPort == a.SocksPort {
		err = errors.New(i18n.T("HTTP 端口和 SOCKS 端口不能相同"))
	}
	a.LANAllowedIPs = cidrList(i18n.T("允许的局域网地址"), f.lanAllowed)
	a.LANDisallowedIPs = cidrList(i18n.T("禁止的局域网地址"), f.lanDisallowed)
	a.Authentication = lines(f.auth)
	for _, l := range a.Authentication {
		if u, _, ok := strings.Cut(l, ":"); err == nil && (!ok || strings.TrimSpace(u) == "") {
			err = fmt.Errorf(i18n.T("代理认证：「%s」应写成 用户名:密码"), l)
		}
	}
	a.SkipAuthPrefixes = cidrList(i18n.T("免认证的地址"), f.skipAuth)
	a.PACMode, a.PACDirectCN = f.pacMode, f.pacDirectCN
	a.PACPort = port(i18n.T("PAC 端口"), f.pacPort)
	a.TUNStrictRoute, a.TUNEndpointIndependentNAT = f.tunStrict, f.tunEIN
	a.TUNMTU = num("MTU", f.tunMTU, 576, 65535)
	a.TUNRouteAddress = cidrList(i18n.T("只接管这些网段"), f.tunRoute)
	a.TUNRouteExcludeAddress = cidrList(i18n.T("不接管的网段"), f.tunExclude)
	a.TUNDNSHijack = lines(f.tunHijack)
	a.TUNDevice = strings.TrimSpace(f.tunDevice)
	a.TUNIncludeInterface, a.TUNExcludeInterface = lines(f.tunInclude), lines(f.tunExclIf)
	a.IPv6, a.UnifiedDelay, a.TCPConcurrent = f.ipv6, f.unifiedDelay, f.tcpConcurrent
	a.FindProcessMode = f.findProcess
	a.KeepAliveInterval = num(i18n.T("TCP 保活间隔"), f.keepAliveInterval, 1, 86400)
	a.KeepAliveIdle = num(i18n.T("TCP 保活空闲"), f.keepAliveIdle, 1, 86400)
	a.DisableKeepAlive = f.disableKeepAlive
	a.InterfaceName = strings.TrimSpace(f.ifaceName)
	a.GeodataLoader = f.geodataLoader
	a.DisableETag, a.StoreFakeIP = f.disableETag, f.storeFakeIP
	a.GlobalUA = strings.TrimSpace(f.globalUA)
	a.DNSListen = strings.TrimSpace(f.dnsListen)
	if err == nil && a.DNSListen != "" {
		if _, e := netip.ParseAddrPort(a.DNSListen); e != nil {
			err = errors.New(i18n.T("DNS 监听地址应写成 IP:端口，例如 127.0.0.1:1053"))
		}
	}
	a.DNSPreferH3, a.DNSCacheAlgorithm = f.dnsPreferH3, f.dnsCache
	a.DNSDefaultNameserver, a.DNSFallback = lines(f.dnsDefaultNS), lines(f.dnsFallback)
	a.DNSFallbackFilter = model.DNSFallbackFilter{GeoIP: f.ffGeoIP, GeoIPCode: strings.ToUpper(strings.TrimSpace(f.ffCode)),
		IPCIDR: cidrList(i18n.T("备用服务器 · 直连网段"), f.ffCIDR), Domain: lines(f.ffDomain)}
	a.SnifferNoForceDNSMapping = f.snfNoForceMapping
	if err != nil {
		return model.AdvancedSettings{}, err
	}
	return a, nil
}

func (p *Page) advDirty() bool { return p.advInit && p.adv != advFormFrom(p.advLoaded) }

func (p *Page) discardAdvanced() { p.adv = advFormFrom(p.advLoaded); p.advSession++ }

// applyAdvanced validates the form and saves it; the service reloads or
// patches the core as each field needs.
func (p *Page) applyAdvanced() {
	e := p.e
	if p.applying {
		return
	}
	s := p.current()
	a, err := p.adv.toSettings(s.MixedPort)
	if err != nil {
		e.Toast(err.Error(), true)
		return
	}
	s.Advanced = a
	p.applying = true
	e.Run(func() error {
		err := e.API.SaveSettings(s)
		p.mu.Lock()
		p.applying = false
		if err == nil {
			p.advLoaded = a
			p.toast = i18n.T("高级设置已应用")
		}
		p.mu.Unlock()
		return err
	}, "")
}

// advancedTab draws the tab: two columns at ≥1100 pt, inbound / system
// proxy / TUN on the left, core behaviour / DNS / sniffer on the right.
func (p *Page) advancedTab(c *ui.Context, t *ui.Theme, s model.Settings, st model.Status) {
	e := p.e
	f := &p.adv
	ui.Column(c.Key(p.advSession)).Gap(20).Children(func() {
		e.Kit.Banner(c, "info", i18n.T("这些选项对应 Mihomo 配置里的同名字段，留空即使用内核默认值；应用后会重新生成配置，大多数需要内核重新加载。"), nil)
		env.Columns(c, 20, func() {
			e.Kit.Section(c, i18n.T("端口与局域网"), "", func() {
				p.advInput(c, i18n.T("HTTP 端口"), i18n.T("额外的纯 HTTP 代理端口（port）；留空则只用代理端口"), "adv-http", &f.httpPort, 84, "")
				p.advInput(c, i18n.T("SOCKS 端口"), i18n.T("额外的纯 SOCKS5 代理端口（socks-port）"), "adv-socks", &f.socksPort, 84, "")
				p.textRow(c, t, i18n.T("允许的局域网地址"), i18n.T("允许局域网连接时，只有这些网段可以使用代理，每行一个（lan-allowed-ips）"), "adv-lan-allow", &f.lanAllowed, 1, false)
				p.textRow(c, t, i18n.T("禁止的局域网地址"), i18n.T("这些网段不能使用代理，每行一个（lan-disallowed-ips）"), "adv-lan-deny", &f.lanDisallowed, 1, false)
				p.textRow(c, t, i18n.T("代理认证"), i18n.T("每行一个 用户名:密码；设置后 HTTP 和 SOCKS5 入口都要求登录（authentication）"), "adv-auth", &f.auth, 1, false)
				p.textRow(c, t, i18n.T("免认证的地址"), i18n.T("来自这些网段的连接不需要认证，例如 127.0.0.1/32（skip-auth-prefixes）"), "adv-skip-auth", &f.skipAuth, 1, false)
				if s.AllowLAN && len(lines(f.auth)) == 0 {
					e.Kit.Banner(c, "warn", i18n.T("已允许局域网连接但没有设置代理认证：同一网络里的任何设备都能使用这个代理。"), nil)
				}
			})
			e.Kit.Section(c, i18n.T("系统代理"), "", func() {
				e.Kit.Row(c, i18n.T("PAC 模式"), i18n.T("系统代理改为自动配置脚本：澜流在本机提供 PAC 文件，「不走代理的地址」由应用自行直连，其余经代理"), func() {
					ui.Switch(c.Key("adv-pac"), &f.pacMode).Label(i18n.T("PAC 模式"))
				})
				e.Kit.Row(c, i18n.T("国内域名直连"), i18n.T("PAC 中把 .cn 和常见国内网站设为直连，不经过内核"), func() {
					ui.Switch(c.Key("adv-pac-cn"), &f.pacDirectCN).Label(i18n.T("国内域名直连")).Disabled(!f.pacMode)
				})
				p.advInput(c, i18n.T("PAC 端口"), i18n.Tf("PAC 文件的本机端口，留空为 %d", model.PACDefaultPort), "adv-pac-port", &f.pacPort, 84, "")
				if st.SystemProxy && (f.pacMode != s.Advanced.PACMode) {
					e.Kit.Banner(c, "info", i18n.T("应用后系统代理会立即切换到新的方式。"), nil)
				}
			})
			e.Kit.Section(c, "TUN", i18n.T("仅在 TUN 模式下生效"), func() {
				e.Kit.Row(c, i18n.T("严格路由"), i18n.T("阻止绕过 TUN 的流量，防止泄漏；个别局域网设备可能无法访问（strict-route）"), func() {
					ui.Switch(c.Key("adv-tun-strict"), &f.tunStrict).Label(i18n.T("严格路由"))
				})
				p.advInput(c, "MTU", i18n.T("TUN 网卡的最大传输单元，576–65535；留空用内核默认值"), "adv-tun-mtu", &f.tunMTU, 84, "")
				e.Kit.Row(c, i18n.T("端点无关 NAT"), i18n.T("UDP 使用对端无关的映射，改善游戏和 P2P 的 NAT 类型（endpoint-independent-nat）"), func() {
					ui.Switch(c.Key("adv-tun-ein"), &f.tunEIN).Label(i18n.T("端点无关 NAT"))
				})
				p.textRow(c, t, i18n.T("只接管这些网段"), i18n.T("设置后只有这些网段进入 TUN，每行一个（route-address）"), "adv-tun-route", &f.tunRoute, 1, false)
				p.textRow(c, t, i18n.T("不接管的网段"), i18n.T("这些网段绕过 TUN，每行一个；与 aTrust 兼容加入的网段合并（route-exclude-address）"), "adv-tun-exclude", &f.tunExclude, 1, false)
				p.textRow(c, t, i18n.T("DNS 劫持"), i18n.T("截获到这些地址的 DNS 查询，每行一个；留空为 any:53 和 tcp://any:53（dns-hijack）"), "adv-tun-hijack", &f.tunHijack, 1, false)
				p.advInput(c, i18n.T("网卡名称"), i18n.T("TUN 网卡的名称；留空用澜流固定的名称（device）"), "adv-tun-device", &f.tunDevice, 160, "utun1700")
				p.textRow(c, t, i18n.T("只在这些网卡上接管"), i18n.T("每行一个网卡名（include-interface）"), "adv-tun-include", &f.tunInclude, 1, false)
				p.textRow(c, t, i18n.T("不在这些网卡上接管"), i18n.T("每行一个网卡名（exclude-interface）"), "adv-tun-exclif", &f.tunExclIf, 1, false)
			})
		}, func() {
			e.Kit.Section(c, i18n.T("内核行为"), "", func() {
				e.Kit.Row(c, "IPv6", i18n.T("解析 AAAA 记录并允许经 IPv6 连接（ipv6）"), func() {
					ui.Switch(c.Key("adv-ipv6"), &f.ipv6).Label("IPv6")
				})
				e.Kit.Row(c, i18n.T("统一延迟"), i18n.T("测速结果扣除握手时间，不同协议的节点更可比（unified-delay）"), func() {
					ui.Switch(c.Key("adv-unified"), &f.unifiedDelay).Label(i18n.T("统一延迟"))
				})
				e.Kit.Row(c, i18n.T("TCP 并发"), i18n.T("域名有多个 IP 时同时尝试，用最快的一个（tcp-concurrent）"), func() {
					ui.Switch(c.Key("adv-tcpc"), &f.tcpConcurrent).Label(i18n.T("TCP 并发"))
				})
				fpOpts := []string{i18n.T("默认"), i18n.T("关闭"), i18n.T("严格"), i18n.T("总是")}
				fpVals := []string{"", "off", "strict", "always"}
				p.selectRow(c, i18n.T("进程匹配"), i18n.T("查找连接所属的进程，供 PROCESS 规则和连接页使用；关闭可省资源（find-process-mode）"), mapName(f.findProcess, fpVals, fpOpts), fpOpts, func(v string) {
					f.findProcess = mapValue(v, fpOpts, fpVals)
				})
				p.advInput(c, i18n.T("TCP 保活间隔"), i18n.T("秒；留空用内核默认值（keep-alive-interval）"), "adv-ka-int", &f.keepAliveInterval, 84, "")
				p.advInput(c, i18n.T("TCP 保活空闲"), i18n.T("秒；空闲多久后开始发送保活（keep-alive-idle）"), "adv-ka-idle", &f.keepAliveIdle, 84, "")
				e.Kit.Row(c, i18n.T("禁用 TCP 保活"), i18n.T("部分网络下可减少无效流量（disable-keep-alive）"), func() {
					ui.Switch(c.Key("adv-ka-off"), &f.disableKeepAlive).Label(i18n.T("禁用 TCP 保活"))
				})
				p.advInput(c, i18n.T("出站网卡"), i18n.T("内核的出站流量绑定到这个网卡，例如 en0；留空自动选择（interface-name）"), "adv-iface", &f.ifaceName, 160, "")
				glOpts := []string{i18n.T("默认"), i18n.T("省内存"), i18n.T("标准")}
				glVals := []string{"", "memconservative", "standard"}
				p.selectRow(c, i18n.T("地理数据加载"), i18n.T("省内存模式启动更快、占用更少（geodata-loader）"), mapName(f.geodataLoader, glVals, glOpts), glOpts, func(v string) {
					f.geodataLoader = mapValue(v, glOpts, glVals)
				})
				e.Kit.Row(c, i18n.T("禁用 ETag"), i18n.T("下载规则集和地理数据库时不带 ETag 校验（etag-support）"), func() {
					ui.Switch(c.Key("adv-etag"), &f.disableETag).Label(i18n.T("禁用 ETag"))
				})
				e.Kit.Row(c, i18n.T("保存 Fake-IP 映射"), i18n.T("重启后 Fake-IP 与域名的对应关系不变（profile.store-fake-ip）"), func() {
					ui.Switch(c.Key("adv-store-fakeip"), &f.storeFakeIP).Label(i18n.T("保存 Fake-IP 映射"))
				})
				p.advInput(c, i18n.T("下载 User-Agent"), i18n.T("内核下载规则集、地理数据库时使用的 UA（global-ua）"), "adv-ua", &f.globalUA, 200, "clash.meta")
			})
			e.Kit.Section(c, i18n.T("DNS 高级"), i18n.T("作用于当前生效的 DNS 设置，无论跟随订阅还是由澜流接管"), func() {
				p.advInput(c, i18n.T("本机 DNS 监听"), i18n.T("让内核在本机提供 DNS 服务，例如 127.0.0.1:1053（listen）"), "adv-dns-listen", &f.dnsListen, 160, "")
				e.Kit.Row(c, i18n.T("优先 HTTP/3"), i18n.T("DoH 服务器支持时用 HTTP/3 查询（prefer-h3）"), func() {
					ui.Switch(c.Key("adv-dns-h3"), &f.dnsPreferH3).Label(i18n.T("优先 HTTP/3"))
				})
				caOpts := []string{i18n.T("默认"), "LRU", "ARC"}
				caVals := []string{"", "lru", "arc"}
				p.selectRow(c, i18n.T("缓存算法"), i18n.T("ARC 命中率更高，占用稍多（cache-algorithm）"), mapName(f.dnsCache, caVals, caOpts), caOpts, func(v string) {
					f.dnsCache = mapValue(v, caOpts, caVals)
				})
				p.textRow(c, t, i18n.T("引导服务器"), i18n.T("用来解析其他 DNS 服务器域名的纯 IP 服务器，每行一个（default-nameserver）"), "adv-dns-default", &f.dnsDefaultNS, 1, false)
				p.textRow(c, t, i18n.T("备用服务器"), i18n.T("与上游并发查询，命中下面的过滤条件时采用备用结果，每行一个（fallback）"), "adv-dns-fallback", &f.dnsFallback, 1, false)
				e.Kit.Row(c, i18n.T("备用服务器 · 按 GeoIP 过滤"), i18n.T("上游返回的 IP 不属于下方国家时改用备用结果（fallback-filter.geoip）"), func() {
					ui.Row(c).Gap(10).Children(func() {
						ui.TextInput(c.Key("adv-ff-code"), &f.ffCode).Width(60).Font("monospace").FontSize(12).Placeholder("CN").Label(i18n.T("GeoIP 国家代码")).Disabled(!f.ffGeoIP)
						ui.Switch(c.Key("adv-ff-geoip"), &f.ffGeoIP).Label(i18n.T("备用服务器 · 按 GeoIP 过滤"))
					})
				})
				p.textRow(c, t, i18n.T("备用服务器 · 直连网段"), i18n.T("上游结果落在这些网段时改用备用结果，每行一个（fallback-filter.ipcidr）"), "adv-ff-cidr", &f.ffCIDR, 1, false)
				p.textRow(c, t, i18n.T("备用服务器 · 域名"), i18n.T("这些域名总是采用备用结果，每行一个（fallback-filter.domain）"), "adv-ff-domain", &f.ffDomain, 1, false)
			})
			e.Kit.Section(c, i18n.T("嗅探高级"), "", func() {
				e.Kit.Row(c, i18n.T("不强制 DNS 映射"), i18n.T("默认对 Fake-IP 或 DNS 映射到的连接总是嗅探；关闭后只按端口列表嗅探（force-dns-mapping）"), func() {
					ui.Switch(c.Key("adv-snf-fdm"), &f.snfNoForceMapping).Label(i18n.T("不强制 DNS 映射"))
				})
			})
		})
	})
}

// advInput is a settings row with a single-line text field bound to the form.
func (p *Page) advInput(c *ui.Context, label, desc, key string, text *string, width float32, placeholder string) {
	p.e.Kit.Row(c, label, desc, func() {
		in := ui.TextInput(c.Key(key), text).Width(width).Font("monospace").FontSize(12).Label(label)
		if width <= 100 {
			in.TextAlign(ui.End).FontFeatures("tnum")
		}
		if placeholder != "" {
			in.Placeholder(placeholder)
		}
	})
}
