package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/locate"
	"lanflow/internal/model"
	"lanflow/internal/netx"
)

// 泄漏检查 (docs/design.md §5.7, docs/advanced.md §5): does anything bypass
// the proxy? Five checks, run on demand, using only the data sources the
// design allows (the core's resolver, the system resolver, one public
// resolver, public STUN servers, the bundled ASN database and the
// Cloudflare trace used for the exit map; no third-party IP sites):
//
//   - DNS: resolve a name the way applications do (system resolver) and
//     see whether the answer came from the core (a Fake-IP, or the system
//     DNS pointing at the core's listener), compared with the core's own
//     answer.
//   - IPv6: request the trace over IPv6 only, without a proxy, as an
//     application that ignores the system proxy would, and compare the
//     reported address with the proxied exit.
//   - WebRTC: send a STUN Binding Request over plain UDP, as a browser's
//     WebRTC stack does (it ignores the system proxy), and compare the
//     address the STUN server saw with the proxied exit and the real IP.
//   - IP 类型: the ASN / organisation of the proxied exit and of the real
//     IP, and whether each looks like a data centre or a consumer ISP.
//   - DNS 对比: the answers of the core, the system resolver and a public
//     resolver side by side, flagging a system answer that matches neither
//     (hijack / pollution).

const (
	leakTimeout  = 25 * time.Second
	leakTestHost = "www.cloudflare.com" // the trace host: resolvable everywhere, no tracking
	// leakPublicDNS is the public resolver of the DNS comparison, asked
	// directly over UDP (reachable from mainland China, no proxy needed).
	leakPublicDNS = "223.5.5.5:53"
)

var errLeakBusy = i18n.Error("泄漏检查正在进行中，请稍候。")

// LeakCheck returns the state of the last / running leak check.
func (s *Service) LeakCheck() model.LeakResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.state.leak
	out.Items = append([]model.LeakItem(nil), s.state.leak.Items...)
	return out
}

// RunLeakCheck runs every check and publishes the result through
// LeakCheck() (TopicNet). It blocks until done (the UI runs it off thread).
func (s *Service) RunLeakCheck() error { return s.RunLeakCheckWith("") }

// RunLeakCheckWith is RunLeakCheck with the user's domain (may be "")
// added to the DNS comparison.
func (s *Service) RunLeakCheckWith(domain string) error {
	if !s.running() {
		return errCoreNotRunning
	}
	s.mu.Lock()
	if s.state.leak.Checking {
		s.mu.Unlock()
		return errLeakBusy
	}
	s.state.leak.Checking = true
	s.state.leak.Error = ""
	st := s.state.st
	port := s.state.settings.MixedPort
	netInfo := s.state.net
	rd := s.state.runtimeDNS
	var you model.EndpointInfo
	if s.state.you != nil {
		you = *s.state.you
	}
	var mainExit string
	for _, r := range s.state.routes {
		if r.Main {
			mainExit = r.Exit.Exit.IP
		}
	}
	s.mu.Unlock()
	s.em.emit(TopicNet)

	ctx, cancel := context.WithTimeout(s.rootCtx, leakTimeout)
	defer cancel()
	var items []model.LeakItem
	if st.Mode == model.ModeDirect {
		skip := i18n.T("直连模式下所有流量都不经过代理，无需检查。切换到规则或全局模式后再检查。")
		summary := i18n.T("当前为直连模式")
		items = []model.LeakItem{
			{Name: "DNS", Status: model.LeakSkipped, Summary: summary, Detail: skip},
			{Name: "IPv6", Status: model.LeakSkipped, Summary: summary, Detail: skip},
			{Name: model.LeakItemWebRTC, Status: model.LeakSkipped, Summary: summary, Detail: skip},
			{Name: model.LeakItemIPType, Title: i18n.T("IP 类型"), Status: model.LeakSkipped, Summary: summary, Detail: skip},
			{Name: model.LeakItemDNSCmp, Title: i18n.T("DNS 对比"), Status: model.LeakSkipped, Summary: summary, Detail: skip},
		}
	} else {
		items = s.runLeakChecks(ctx, st, netInfo, rd, port, you, mainExit, leakDomain(domain))
	}

	s.mu.Lock()
	s.state.leak = model.LeakResult{Items: items, CheckedAt: s.d.now()}
	s.mu.Unlock()
	s.em.emit(TopicNet)
	return nil
}

// runLeakChecks runs the probes concurrently (each has its own network
// round trips) and returns the items in display order.
func (s *Service) runLeakChecks(ctx context.Context, st model.Status, n model.NetInfo, rd runtimeDNS, port int, you model.EndpointInfo, mainExit, domain string) []model.LeakItem {
	var (
		wg                       sync.WaitGroup
		dnsItem, v6Item, cmpItem model.LeakItem
		dnsP                     dnsProbe
		exitIP, mapped           string
		exitErr, mappedErr       error
		traceURL                 = s.d.traceURL
	)
	if traceURL == "" {
		traceURL = locate.TraceURL
	}
	wg.Add(4)
	go func() { // the system resolver vs. the core, then the comparison
		defer wg.Done()
		dnsItem, dnsP = s.dnsLeak(ctx, st, n, rd)
		cmpItem = s.dnsCompare(ctx, dnsP, domain)
	}()
	go func() { // IPv6
		defer wg.Done()
		v6Item = s.ipv6Leak(ctx, st, n, port)
	}()
	go func() { // the proxied exit (IPv4), shared by WebRTC and IP 类型
		defer wg.Done()
		exitIP, exitErr = fetchTraceIP(ctx, s.d.httpVia(port, leakTimeout), traceURL)
		if exitErr != nil && mainExit != "" {
			exitIP, exitErr = mainExit, nil
		}
	}()
	go func() { // STUN over plain UDP
		defer wg.Done()
		stun := s.d.stun
		if stun == nil {
			stun = realSTUN
		}
		mapped, mappedErr = stun(ctx)
	}()
	wg.Wait()
	rtcItem := webrtcLeakItem(webrtcProbe{tun: st.TUN, sysProxy: st.SystemProxy, mapped: mapped, mappedErr: mappedErr, exit: exitIP, direct: you.IP, local6: n.IPv6})
	typeItem := ipTypeItem(s.ipTypeProbe(exitIP, exitErr, you))
	return []model.LeakItem{dnsItem, v6Item, rtcItem, typeItem, cmpItem}
}

// realSTUN asks netx.STUNServers directly (no proxy, like WebRTC).
func realSTUN(ctx context.Context) (string, error) {
	addr, err := netx.STUNPublicIP(ctx, nil, nil)
	if err != nil {
		return "", err
	}
	return addr.String(), nil
}

// leakDomain cleans the user's domain for the DNS comparison: a URL or a
// host with a path becomes its host; anything unusable becomes "".
func leakDomain(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			s = u.Hostname()
		}
	}
	s = strings.TrimSuffix(strings.SplitN(s, "/", 2)[0], ".")
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	if s == "" || s == leakTestHost || strings.ContainsAny(s, " \t\"'<>") || !strings.Contains(s, ".") || net.ParseIP(s) != nil {
		return ""
	}
	return s
}

// dnsProbe is what the DNS check observed; dnsLeakItem turns it into words.
type dnsProbe struct {
	tun, sysProxy bool
	fakeIP        bool       // the effective DNS runs in fake-ip mode
	fakeRange     *net.IPNet // nil = default
	listen        string     // the core's DNS listener ("host:port"), "" when none
	sysDNS        []string   // the system's DNS servers
	sysAnswers    []string   // what the system resolver returned for leakTestHost
	sysErr        error
	coreAnswers   []string // what the core's resolver returned
	coreErr       error
}

func (s *Service) dnsLeak(ctx context.Context, st model.Status, n model.NetInfo, rd runtimeDNS) (model.LeakItem, dnsProbe) {
	p := dnsProbe{tun: st.TUN, sysProxy: st.SystemProxy, fakeIP: rd.FakeIP, fakeRange: rd.FakeRange, listen: listenAddr(rd.Listen), sysDNS: n.DNS}
	p.sysAnswers, p.sysErr = s.appDNS(ctx, "", leakTestHost)
	p.coreAnswers, p.coreErr = s.coreDNS(ctx, leakTestHost)
	return dnsLeakItem(p), p
}

// appDNS resolves name's A records through server ("" = system resolver)
// within appDNSTimeout.
func (s *Service) appDNS(ctx context.Context, server, name string) ([]string, error) {
	actx, cancel := context.WithTimeout(ctx, appDNSTimeout)
	defer cancel()
	return s.d.appDNS(actx, server, name, "A")
}

// coreDNS resolves name's A records through the core's resolver.
func (s *Service) coreDNS(ctx context.Context, name string) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, ctrlTimeout)
	defer cancel()
	r, err := s.d.ctrl.DNSQuery(cctx, name, "A")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range r.Answer {
		out = append(out, a.Data)
	}
	return out, nil
}

// dnsLeakItem judges the DNS path from what was observed.
func dnsLeakItem(p dnsProbe) model.LeakItem {
	item := model.LeakItem{Name: "DNS"}
	sysDNS := strings.Join(p.sysDNS, ", ")
	if sysDNS == "" {
		sysDNS = i18n.T("未知")
	}
	switch {
	case p.sysErr != nil && len(p.sysAnswers) == 0:
		item.Status = model.LeakWarn
		item.Summary = i18n.Tf("系统解析器无法解析 %s", leakTestHost)
		item.Detail = i18n.Tf("无法判断 DNS 请求的去向（%v）。请检查网络连接后重试。", p.sysErr)
	case allFakeIP(p.sysAnswers, p.fakeRange) && (p.tun || (p.listen != "" && pointsAt(p.sysDNS, p.listen))):
		item.Status = model.LeakOK
		item.Summary = i18n.T("系统 DNS 请求经过澜流")
		item.Detail = i18n.T("系统解析器返回的是澜流分配的 Fake-IP，说明应用的域名查询已由内核处理，不会直接发给运营商的 DNS。")
	case p.listen != "" && pointsAt(p.sysDNS, p.listen):
		item.Status = model.LeakOK
		item.Summary = i18n.Tf("系统 DNS 指向澜流的 DNS 服务（%s）", p.listen)
		item.Detail = i18n.T("应用的域名查询先到内核的 DNS 服务，再由内核按设置向上游查询。")
	case allFakeIP(p.sysAnswers, p.fakeRange):
		// Fake-IPs, but nothing routes the system resolver to the core
		// (no TUN hijack, system DNS elsewhere): another fake-ip proxy
		// upstream (a router, the host of a VM) is answering.
		item.Status = model.LeakWarn
		item.Summary = i18n.T("系统 DNS 的 Fake-IP 不是澜流分配的")
		item.Detail = i18n.Tf("系统解析器返回了 Fake-IP（%s），但 TUN 未开启、系统 DNS（%s）也不指向澜流，这些地址来自上游网络中的另一个代理（如路由器或宿主机上的代理客户端）。应用的域名查询没有经过澜流。", strings.Join(p.sysAnswers, ", "), sysDNS)
	case p.tun && p.fakeIP:
		item.Status = model.LeakFail
		item.Summary = i18n.T("系统 DNS 请求未经过澜流")
		item.Detail = i18n.Tf("TUN 已开启，但系统解析器返回了真实地址（%s），说明域名查询绕过了内核，直接发给了系统 DNS（%s）。请在设置 › 代理中开启「自动设置系统 DNS」，或把系统 DNS 改为任意公网地址，让 TUN 接管 53 端口的查询。", strings.Join(p.sysAnswers, ", "), sysDNS)
	case p.tun:
		if overlaps(p.sysAnswers, p.coreAnswers) {
			item.Status = model.LeakOK
			item.Summary = i18n.T("系统 DNS 解析结果与内核一致")
			item.Detail = i18n.T("当前为真实 IP 模式，TUN 默认劫持所有发往 53 端口的查询；系统解析器与内核返回了相同的地址。使用 DoH/DoT 的应用不经过内核解析，这属于正常现象。")
		} else {
			item.Status = model.LeakWarn
			item.Summary = i18n.T("无法确认 DNS 请求是否经过澜流")
			item.Detail = i18n.Tf("当前为真实 IP 模式，经内核和直接查询系统 DNS 返回的都是真实地址，而两者结果不同（系统：%s；内核：%s）。TUN 默认劫持所有发往 53 端口的查询；如果系统 DNS（%s）使用 DoH/DoT，查询会绕过内核。", strings.Join(p.sysAnswers, ", "), strings.Join(p.coreAnswers, ", "), sysDNS)
		}
	case p.sysProxy:
		item.Status = model.LeakWarn
		item.Summary = i18n.T("仅遵守系统代理的应用由澜流解析域名")
		item.Detail = i18n.Tf("系统代理模式下，浏览器等遵守系统代理的应用把域名交给澜流处理，不会泄漏；不遵守系统代理的程序直接使用系统 DNS（%s）。要让所有应用的 DNS 请求经过澜流，请开启 TUN 模式。", sysDNS)
	default:
		item.Status = model.LeakWarn
		item.Summary = i18n.T("系统代理和 TUN 都未开启")
		item.Detail = i18n.Tf("应用的域名查询直接发给系统 DNS（%s）。开启系统代理或 TUN 模式后再检查。", sysDNS)
	}
	return item
}

// pointsAt reports whether every system DNS server is the core's listener
// host (loopback listeners match any loopback address).
func pointsAt(servers []string, listen string) bool {
	if len(servers) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	for _, sv := range servers {
		sv = strings.TrimSpace(sv)
		if h, _, err := net.SplitHostPort(sv); err == nil {
			sv = h
		}
		if sv == host || (isLoopback(host) && isLoopback(sv)) {
			continue
		}
		return false
	}
	return true
}

// overlaps reports whether the two answer sets share an address.
func overlaps(a, b []string) bool {
	seen := map[string]bool{}
	for _, x := range a {
		seen[strings.TrimSpace(x)] = true
	}
	for _, y := range b {
		if seen[strings.TrimSpace(y)] {
			return true
		}
	}
	return false
}

// ---- DNS 对比 ----

// dnsAnswers is what the three resolvers returned for one name.
type dnsAnswers struct {
	name                    string
	core, sys, pub          []string
	coreErr, sysErr, pubErr error
}

// dnsCompareProbe is what the DNS comparison observed: the three answers
// for each name, plus what dnsLeak learned about the system path.
type dnsCompareProbe struct {
	fakeRange *net.IPNet
	listen    string
	sysDNS    []string
	names     []dnsAnswers
}

// dnsCompare resolves leakTestHost (reusing dnsLeak's system and core
// answers) and the user's domain through the core, the system resolver
// and the public resolver.
func (s *Service) dnsCompare(ctx context.Context, p dnsProbe, domain string) model.LeakItem {
	cp := dnsCompareProbe{fakeRange: p.fakeRange, listen: p.listen, sysDNS: p.sysDNS}
	host := dnsAnswers{name: leakTestHost, core: p.coreAnswers, coreErr: p.coreErr, sys: p.sysAnswers, sysErr: p.sysErr}
	host.pub, host.pubErr = s.appDNS(ctx, leakPublicDNS, leakTestHost)
	cp.names = append(cp.names, host)
	if domain != "" {
		d := dnsAnswers{name: domain}
		d.core, d.coreErr = s.coreDNS(ctx, domain)
		d.sys, d.sysErr = s.appDNS(ctx, "", domain)
		d.pub, d.pubErr = s.appDNS(ctx, leakPublicDNS, domain)
		cp.names = append(cp.names, d)
	}
	return dnsCompareItem(cp)
}

// dnsCompareItem lists the answers side by side and flags a system answer
// that came neither from the core nor agrees with the public resolver.
func dnsCompareItem(p dnsCompareProbe) model.LeakItem {
	item := model.LeakItem{Name: model.LeakItemDNSCmp, Title: i18n.T("DNS 对比"), Status: model.LeakOK}
	var suspicious, unresolved []string
	viaCore := p.listen != "" && pointsAt(p.sysDNS, p.listen)
	for _, a := range p.names {
		prefix := ""
		if len(p.names) > 1 {
			prefix = i18n.Tf("%s：", a.name)
		}
		item.Rows = append(item.Rows,
			model.LeakRow{Label: i18n.T("内核"), Value: prefix + answersText(a.core, a.coreErr)},
			model.LeakRow{Label: i18n.T("系统解析器"), Value: prefix + answersText(a.sys, a.sysErr)},
			model.LeakRow{Label: i18n.Tf("公共 DNS %s", strings.TrimSuffix(leakPublicDNS, ":53")), Value: prefix + answersText(a.pub, a.pubErr)},
		)
		switch {
		case len(a.sys) == 0:
			unresolved = append(unresolved, a.name)
		case allFakeIP(a.sys, p.fakeRange), viaCore:
			// The system answer is the core's: nothing to compare.
		case overlaps(a.sys, a.core), overlaps(a.sys, a.pub):
		case len(a.core) == 0 && len(a.pub) == 0:
			unresolved = append(unresolved, a.name)
		default:
			suspicious = append(suspicious, a.name)
		}
	}
	switch {
	case len(suspicious) > 0:
		item.Status = model.LeakWarn
		item.Summary = i18n.Tf("系统解析器对 %s 的结果与内核和公共 DNS 都不同", strings.Join(suspicious, "、"))
		item.Detail = i18n.T("系统 DNS 返回的地址既不是内核解析到的，也不是公共 DNS 解析到的，可能被运营商劫持或污染。CDN 域名在不同地区本来就会返回不同地址，若该域名使用 CDN，这可能是正常现象；若是普通域名，建议开启 TUN 或「自动设置系统 DNS」让查询经过澜流。")
	case len(unresolved) > 0:
		item.Status = model.LeakWarn
		item.Summary = i18n.Tf("无法完成 %s 的解析对比", strings.Join(unresolved, "、"))
		item.Detail = i18n.T("至少一路解析没有返回结果（超时或被拦截）。直接发往公共 DNS 的 UDP 查询若总是超时，通常是网络拦截了 53 端口的外部查询。")
	default:
		item.Summary = i18n.T("系统解析器的结果与内核或公共 DNS 一致")
		item.Detail = i18n.T("三路解析相互印证，未发现劫持或污染。内核与公共 DNS 的结果不同属正常现象（内核可能经代理在境外解析）。")
	}
	return item
}

// answersText joins answers, or names the failure.
func answersText(answers []string, err error) string {
	if len(answers) > 0 {
		return strings.Join(answers, ", ")
	}
	if err != nil {
		return i18n.Tf("失败（%v）", shortErr(err))
	}
	return i18n.T("无结果")
}

// shortErr trims a Go net error to its last, most telling part.
func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && i+2 < len(s) {
		s = s[i+2:]
	}
	return s
}

// ---- WebRTC / STUN ----

// webrtcProbe is what the WebRTC check observed.
type webrtcProbe struct {
	tun, sysProxy bool
	mapped        string // the address the STUN server saw, "" on failure
	mappedErr     error
	exit          string // the proxied exit (trace through the proxy), "" when unknown
	direct        string // the real public IP (You()), "" when unknown
	local6        string // the machine's public IPv6 (NetInfo), "" when none
}

// fromReal reports whether the STUN server saw one of the machine's own
// public addresses (the socket may have gone over IPv6).
func (p webrtcProbe) fromReal() bool {
	return (p.direct != "" && p.mapped == p.direct) || (p.local6 != "" && p.mapped == p.local6)
}

// webrtcLeakItem judges the UDP path from what was observed.
func webrtcLeakItem(p webrtcProbe) model.LeakItem {
	item := model.LeakItem{Name: model.LeakItemWebRTC}
	rows := func() []model.LeakRow {
		r := []model.LeakRow{{Label: i18n.T("STUN 看到"), Value: orDash(p.mapped)}}
		r = append(r, model.LeakRow{Label: i18n.T("代理出口"), Value: orDash(p.exit)}, model.LeakRow{Label: i18n.T("本机公网"), Value: orDash(p.direct)})
		return r
	}
	browserFix := i18n.T("要堵住这个口子，请开启 TUN 模式让 UDP 也经过澜流；或在浏览器中限制 WebRTC：Chrome/Edge 可安装 WebRTC 控制类扩展（或 uBlock Origin 的「阻止 WebRTC 泄漏本地 IP」），Firefox 可在 about:config 中把 media.peerconnection.enabled 设为 false。")
	switch {
	case p.mappedErr != nil:
		item.Status = model.LeakOK
		item.Summary = i18n.T("STUN 请求没有得到响应")
		item.Detail = i18n.T("直接发出的 UDP 探测没有收到任何公共 STUN 服务器的回应：UDP 被网络拦截或 STUN 服务不可达。浏览器的 WebRTC 此时同样拿不到公网地址，不会借此暴露真实 IP。")
		item.Rows = rows()
	case p.exit != "" && p.mapped == p.exit:
		item.Status = model.LeakOK
		item.Summary = i18n.Tf("WebRTC（UDP）流量经过澜流，出口 %s", p.exit)
		item.Detail = i18n.T("STUN 服务器看到的是代理出口地址，说明直接发出的 UDP 已被接管；浏览器的 WebRTC 不会暴露真实 IP。")
		item.Rows = rows()
	case p.fromReal() && p.tun:
		item.Status = model.LeakFail
		item.Summary = i18n.Tf("WebRTC 暴露了真实 IP（%s）", p.mapped)
		item.Detail = i18n.T("TUN 已开启，但直接发出的 UDP 仍从本机公网地址出去，没有经过代理。请检查节点是否支持 UDP、订阅或覆写中是否禁用了 UDP 转发（udp: false），以及 fake-ip-filter 是否放行了 STUN 域名。") + " " + browserFix
		item.Rows = rows()
	case p.fromReal():
		item.Status = model.LeakWarn
		item.Summary = i18n.Tf("浏览器的 WebRTC 会暴露真实 IP（%s）", p.mapped)
		item.Detail = i18n.T("系统代理模式只覆盖 TCP 的 HTTP/HTTPS 请求，而浏览器的 WebRTC 直接用 UDP 向 STUN 服务器询问公网地址，网站可借此拿到真实 IP。") + " " + browserFix
		item.Rows = rows()
	case p.exit == "" && p.direct == "":
		item.Status = model.LeakWarn
		item.Summary = i18n.Tf("STUN 看到的地址是 %s，但无法取得代理出口和本机公网地址作对比", p.mapped)
		item.Detail = i18n.T("请稍后重新检查；若本机公网地址一直无法获取，请点击网络页上方的「重新测试」。")
		item.Rows = rows()
	default:
		item.Status = model.LeakWarn
		item.Summary = i18n.Tf("STUN 看到的地址（%s）既不是代理出口也不是本机公网地址", p.mapped)
		item.Detail = i18n.T("UDP 可能从另一条线路（如节点的另一出口、运营商 NAT 的不同地址或 IPv6）出去。如果它属于代理节点，则 WebRTC 没有泄漏；否则请开启 TUN 模式。") + " " + browserFix
		item.Rows = rows()
	}
	return item
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ---- IP 类型 ----

// ipTypeProbe is what the IP-type check has: the proxied exit and the
// real endpoint with their ASN data.
type ipTypeProbe struct {
	exit    model.EndpointInfo
	exitErr error
	direct  model.EndpointInfo // IP "" when unknown
}

// ipTypeProbe looks both addresses up in the bundled ASN database.
func (s *Service) ipTypeProbe(exitIP string, exitErr error, you model.EndpointInfo) ipTypeProbe {
	p := ipTypeProbe{exitErr: exitErr}
	if exitIP != "" {
		p.exit = s.d.loc.Lookup(exitIP)
	}
	if you.IP != "" {
		p.direct = s.d.loc.Lookup(you.IP)
		if p.direct.ASN == "" {
			p.direct.ASN = you.ASN
		}
		if p.direct.ISP == "" {
			p.direct.ISP = you.ISP
		}
	}
	return p
}

// ipTypeItem describes both endpoints; it is informational (OK) unless
// the exit could not be determined.
func ipTypeItem(p ipTypeProbe) model.LeakItem {
	item := model.LeakItem{Name: model.LeakItemIPType, Title: i18n.T("IP 类型"), Status: model.LeakOK}
	if p.exit.IP == "" {
		item.Status = model.LeakWarn
		item.Summary = i18n.T("无法获取代理出口地址")
		item.Detail = i18n.Tf("经代理请求 Cloudflare 失败（%v）。请确认当前节点可用后重新检查。", shortErr(errOrUnknown(p.exitErr)))
	} else {
		item.Summary = i18n.Tf("出口 %s", endpointLine(p.exit))
		switch locate.Kind(p.exit.ASN, p.exit.ISP) {
		case model.IPKindDatacenter:
			item.Detail = i18n.T("出口属于云服务 / 机房网络，流媒体和风控系统容易把它识别为代理；需要更「纯净」的出口时可换用家宽或原生 IP 节点。")
		case model.IPKindResidential:
			item.Detail = i18n.T("出口属于运营商 / 家宽网络，通常不易被识别为代理。")
		default:
			item.Detail = i18n.T("ASN 数据库中没有这个地址的归属信息，无法判断类型；更新地理数据库后可再试。")
		}
	}
	item.Rows = []model.LeakRow{{Label: i18n.T("代理出口"), Value: orDash(endpointLineOrEmpty(p.exit))}, {Label: i18n.T("本机公网"), Value: orDash(endpointLineOrEmpty(p.direct))}}
	item.Detail += " " + i18n.T("「在浏览器中检查 IP 纯净度」会打开第三方网站做更细的风险评分，澜流本身不访问这些网站。")
	return item
}

func endpointLineOrEmpty(ep model.EndpointInfo) string {
	if ep.IP == "" {
		return ""
	}
	return endpointLine(ep)
}

func errOrUnknown(err error) error {
	if err == nil {
		return errors.New(i18n.T("未知"))
	}
	return err
}

// ---- IPv6 ----

// ipv6Probe is what the IPv6 check observed.
type ipv6Probe struct {
	tun, sysProxy bool
	localV6       string // the machine's public IPv6 from NetInfo, "" when none
	direct6       string // the address Cloudflare saw for an IPv6-only direct request
	direct6Err    error
	via           string // the address Cloudflare saw through the proxy port
	viaErr        error
	// The IPv6 routing table: the physical interface with an IPv6 default
	// route ("" when none) and whether a TUN owns ::/0, so IPv6 connections
	// enter it. Under TUN they tell a bypass from "no IPv6".
	routeIface  string
	routeViaTUN bool
}

func (s *Service) ipv6Leak(ctx context.Context, st model.Status, n model.NetInfo, port int) model.LeakItem {
	p := ipv6Probe{tun: st.TUN, sysProxy: st.SystemProxy, localV6: n.IPv6}
	if s.d.v6Route != nil {
		r := s.d.v6Route(ctx)
		p.routeIface, p.routeViaTUN = r.Interface, r.ViaTUN
	}
	direct6 := s.d.httpDirect6
	if direct6 == nil {
		direct6 = directIPv6Client
	}
	url := s.d.traceURL
	if url == "" {
		url = locate.TraceURL
	}
	p.direct6, p.direct6Err = fetchTraceIP(ctx, direct6(leakTimeout), url)
	if p.direct6Err == nil {
		p.via, p.viaErr = fetchTraceIP(ctx, s.d.httpVia(port, leakTimeout), url)
	}
	return ipv6LeakItem(p)
}

// ipv6LeakItem judges the IPv6 path from what was observed. Under TUN the
// routing table decides what a failed IPv6 request means: a default route
// on a physical interface with no TUN route covering IPv6 is a bypass (the
// packets left, whether or not anything answered, as a LAN with a ULA and
// NAT66 shows), while a TUN that owns ::/0 took the request and the proxy
// simply has no IPv6.
func ipv6LeakItem(p ipv6Probe) model.LeakItem {
	item := model.LeakItem{Name: "IPv6"}
	fix := i18n.T("澜流默认让 TUN 同时接管 IPv6；请检查订阅或覆写中是否设置了 ipv6: false、改动了 tun.inet6-address 或 tun.route-address，改好后重启内核再检查。临时的办法是在系统网络设置中关闭 IPv6。")
	switch {
	case p.tun && p.direct6Err != nil && p.routeIface != "" && !p.routeViaTUN:
		item.Status = model.LeakFail
		item.Summary = i18n.Tf("IPv6 路由绕过了 TUN（%s）", p.routeIface)
		item.Detail = i18n.Tf("本机经 %s 有 IPv6 默认路由，而 TUN 没有接管 IPv6（::/0 不经过 TUN）。支持 IPv6 的网站会被应用直接连接，不经过代理；本次经 IPv6 访问外网虽然失败（%v），数据包仍已直接发出。", p.routeIface, p.direct6Err) + " " + fix
	case p.tun && p.direct6Err != nil && p.routeViaTUN:
		item.Status = model.LeakOK
		item.Summary = i18n.T("IPv6 已由 TUN 接管，当前无法经 IPv6 访问外网")
		item.Detail = i18n.Tf("TUN 持有 IPv6 默认路由，应用的 IPv6 连接都会进入内核；本次经 IPv6 访问外网失败（%v），通常是代理节点或上游不支持 IPv6，应用会回退到 IPv4。", p.direct6Err)
	case p.direct6Err != nil:
		item.Status = model.LeakOK
		item.Summary = i18n.T("本机没有可用的公网 IPv6")
		item.Detail = i18n.T("没有 IPv6 连接，也就不存在 IPv6 流量绕过代理的问题。")
		if p.localV6 != "" {
			item.Status = model.LeakWarn
			item.Summary = i18n.Tf("本机有 IPv6 地址（%s），但经 IPv6 访问失败", p.localV6)
			item.Detail = i18n.Tf("无法通过 IPv6 直接访问外网（%v），通常意味着 IPv6 不可用，应用会回退到 IPv4。", p.direct6Err)
		}
	case p.tun && p.viaErr == nil && p.direct6 == p.via:
		item.Status = model.LeakOK
		item.Summary = i18n.T("IPv6 流量经过澜流")
		item.Detail = i18n.Tf("强制走 IPv6 的直连请求也从代理出口（%s）出去，说明 TUN 已接管 IPv6 流量。", p.via)
	case p.tun && p.routeViaTUN && p.direct6 != p.localV6:
		// The request entered the TUN (it owns ::/0) and came out of an
		// address that is not this machine's: the node's own IPv6 exit.
		item.Status = model.LeakOK
		item.Summary = i18n.Tf("IPv6 流量经过澜流，出口 %s", p.direct6)
		item.Detail = i18n.Tf("TUN 持有 IPv6 默认路由，强制走 IPv6 的请求进入了内核；它从 %s 出去，与 IPv4 代理出口（%s）不同，是节点自己的 IPv6 出口。", p.direct6, orDash(p.via))
	case p.tun:
		item.Status = model.LeakFail
		item.Summary = i18n.T("IPv6 流量绕过了代理")
		item.Detail = i18n.Tf("强制走 IPv6 的请求直接从本机地址 %s 出去，而不是代理节点。TUN 没有接管 IPv6：请检查订阅或覆写中的 ipv6 与 tun.inet6-address 设置；在此之前，支持 IPv6 的网站会被应用直接连接。", p.direct6)
	case p.sysProxy:
		item.Status = model.LeakWarn
		item.Summary = i18n.Tf("本机可直接访问 IPv6（%s）", p.direct6)
		item.Detail = i18n.T("系统代理模式下，浏览器等遵守系统代理的应用经代理访问，不受影响；不遵守系统代理的程序对支持 IPv6 的网站会直接连接。开启 TUN 模式可接管全部流量。")
	default:
		item.Status = model.LeakWarn
		item.Summary = i18n.Tf("本机可直接访问 IPv6（%s）", p.direct6)
		item.Detail = i18n.T("系统代理和 TUN 都未开启，应用直接连接。开启系统代理或 TUN 模式后再检查。")
	}
	return item
}

// directIPv6Client is netx.Direct restricted to IPv6: it fails fast when
// the machine has no IPv6 route instead of falling back to IPv4.
func directIPv6Client(timeout time.Duration) *http.Client {
	c := netx.Direct(timeout)
	if t, ok := c.Transport.(*http.Transport); ok {
		t = t.Clone()
		d := &net.Dialer{Timeout: 10 * time.Second}
		t.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp6", addr)
		}
		c.Transport = t
	}
	return c
}

// fetchTraceIP GETs the Cloudflare trace with client and returns its ip= line.
func fetchTraceIP(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Lanflow/0.1 leak-check")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(i18n.T("Cloudflare 返回 HTTP %d"), resp.StatusCode)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 64<<10))
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok && strings.TrimSpace(k) == "ip" {
			if ip := strings.TrimSpace(v); ip != "" {
				return ip, nil
			}
		}
	}
	return "", errors.New(i18n.T("响应中没有 ip 字段，可能被中间设备劫持"))
}
