package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lanflow/internal/capture/agent"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// CheckCaptureTarget explains, stage by stage, whether connections to
// target would be captured and why not (docs/capture.md §16). target is a
// host, host:port or a URL. The check reads the selection and the running
// state, resolves the name and, while decryption runs, connects to the
// target through the return listener (the route the decrypted connection
// would take, without being captured).
func (s *Service) CheckCaptureTarget(target string) model.CaptureCheck {
	s.capLoad()
	host, port, scheme, err := parseCaptureTarget(target)
	chk := model.CaptureCheck{Host: host, Port: port, Scheme: scheme}
	step := func(stage string, ok bool, detail string) {
		chk.Steps = append(chk.Steps, model.CaptureCheckStep{Stage: stage, OK: ok, Detail: detail})
	}
	block := func(reason, summary, advice string) {
		if chk.Reason == "" {
			chk.Reason, chk.Summary, chk.Advice = reason, summary, advice
		}
	}
	if err != nil {
		step(i18n.T("目标"), false, err.Error())
		block(model.CaptureReasonNotMatched, err.Error(), i18n.T("输入域名、域名:端口或完整网址。"))
		return chk
	}

	c := &s.capst
	c.mu.Lock()
	set := cloneCaptureSettings(c.set)
	enabled, until, r, trusted := c.enabled, c.until, c.run, c.trusted
	flows := make([]model.CaptureFlow, 0, 8)
	for i := len(c.flows) - 1; i >= 0 && len(flows) < 50; i-- {
		if strings.EqualFold(c.flows[i].Host, host) && (c.flows[i].Port == 0 || c.flows[i].Port == port) {
			flows = append(flows, c.flows[i])
		}
	}
	c.mu.Unlock()
	s.mu.Lock()
	mode, core, tun := s.state.settings.Mode, s.state.st.Core, s.state.runningTUN
	co := s.coexistLocked()
	s.mu.Unlock()

	// 运行状态
	switch {
	case !enabled:
		step(i18n.T("运行状态"), false, i18n.T("解密未开启"))
		block(model.CaptureReasonDisabled, i18n.T("解密未开启"), i18n.T("在「抓包」页开启解密后再检查。"))
	case core != model.CoreRunning:
		step(i18n.T("运行状态"), false, i18n.T("内核未运行"))
		block(model.CaptureReasonPaused, i18n.T("解密已暂停：内核未运行"), i18n.T("启动代理后再检查。"))
	case mode != "" && mode != model.ModeRule:
		step(i18n.T("运行状态"), false, i18n.T("当前不是规则模式"))
		block(model.CaptureReasonPaused, i18n.T("解密已暂停：仅规则模式生效"), i18n.T("切回规则模式。"))
	default:
		d := i18n.T("解密已开启")
		if !until.IsZero() {
			d += i18n.Tf("，%s 自动关闭", until.Format("15:04"))
		}
		step(i18n.T("运行状态"), true, d)
	}
	if !trusted {
		block(model.CaptureReasonCA, i18n.T("系统尚未信任澜流根证书"), i18n.T("在「抓包」页安装根证书。"))
	}

	// 匹配
	sel := agent.MatchPattern(set.Domains, host)
	if sel == "" {
		d := i18n.T("不在要解密的域名中")
		if parent := parentSuggest(host, set.Domains); parent != "" {
			d += i18n.Tf("（「%s」只匹配它自己，不含子域名；要包含子域名请写成 +.%s）", parent, strings.TrimPrefix(parent, "*."))
		}
		step(i18n.T("匹配"), false, d)
		block(model.CaptureReasonNotMatched, i18n.T("目标不在选择范围内"), i18n.T("在「选择…」中添加这个域名。"))
	} else {
		step(i18n.T("匹配"), true, i18n.Tf("命中「%s」", sel))
	}
	ports := set.Ports
	if strings.TrimSpace(ports) == "" {
		ports = model.CaptureDefaultPorts
	}
	if !portIn(ports, port) {
		step(i18n.T("端口"), false, i18n.Tf("端口 %d 不在 %s 中", port, ports))
		block(model.CaptureReasonPortNotIn, i18n.Tf("端口 %d 未选择", port), i18n.T("在「选择… › 选项 › 端口」中加入这个端口。"))
	} else {
		step(i18n.T("端口"), true, ports)
	}

	// 跳过列表
	allowLAN := sel != "" && agent.MatchPattern(captureAllowPrivate(set), host) != ""
	always, private := CaptureBypassSplit(set)
	if b := agent.MatchPattern(set.Bypass, host); b != "" {
		step(i18n.T("跳过列表"), false, i18n.Tf("命中自定义跳过「%s」", b))
		block(model.CaptureReasonUserBypass, i18n.T("目标在自定义跳过列表中"), i18n.T("从「跳过」中移除该条目。"))
	} else if b := agent.MatchPattern(always, host); b != "" {
		step(i18n.T("跳过列表"), false, i18n.Tf("命中内置跳过「%s」", b))
		block(model.CaptureReasonBuiltinBypass, i18n.T("目标在内置跳过列表中"), i18n.T("可在「内置跳过列表」中停用该条目。"))
	} else {
		step(i18n.T("跳过列表"), true, i18n.T("未命中"))
	}

	// DNS
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a.Unmap()}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		cancel()
		if err != nil {
			step("DNS", false, err.Error())
			block(model.CaptureReasonDNS, i18n.T("域名无法解析"), i18n.T("检查企业 DNS 或网络连接。"))
		}
		for _, ip := range ips {
			addrs = append(addrs, ip.Unmap())
		}
	}
	var privHit string
	for _, a := range addrs {
		chk.Addrs = append(chk.Addrs, a.String())
		if p := agent.MatchPattern(private, a.String()); p != "" && privHit == "" {
			privHit = a.String() + " ∈ " + p
		}
	}
	if privHit == "" {
		privHit = agent.MatchPattern(private, host)
	}
	if privHit == "" && agent.SingleLabel(host) {
		privHit = i18n.T("单标签主机名")
	}
	if len(addrs) > 0 {
		step("DNS", true, strings.Join(chk.Addrs, ", "))
	}
	switch {
	case privHit != "" && allowLAN:
		step(i18n.T("内网地址"), true, i18n.Tf("%s，已允许内网抓包", privHit))
	case privHit != "":
		step(i18n.T("内网地址"), false, i18n.Tf("%s，内网抓包未允许", privHit))
		block(model.CaptureReasonPrivate, i18n.T("内网地址被排除"), i18n.T("在「选择… › 允许内网地址」中加入这个目标。"))
	}

	// 流量入口：ranges the aTrust layer keeps out of the TUN never reach Mihomo
	if tun {
		for _, a := range addrs {
			for _, p := range prefixesOf(co.ExcludeCIDRs) {
				if p.Contains(a) {
					step(i18n.T("流量入口"), false, i18n.Tf("%s 在 TUN 排除范围 %s 内，流量不经过澜流", a, p))
					block(model.CaptureReasonNotEntering, i18n.T("流量没有进入澜流"),
						i18n.Tf("该地址由企业 VPN 直接路由（route-exclude-address）。可让应用改用系统代理 127.0.0.1:%d，或从 aTrust 兼容的排除网段中移除它。", s.mixedPort()))
				}
			}
		}
	}
	for _, d := range co.CorpDomains {
		if agent.MatchPattern([]string{"+." + d}, host) != "" {
			step(i18n.T("企业规则"), true, i18n.Tf("命中企业域名 %s：抓包后回流仍按企业直连出站", d))
			break
		}
	}

	// 上游连接 through the return listener
	if enabled && r != nil && core == model.CoreRunning {
		if err := probeUpstream(r, host, port, scheme == "https"); err != nil {
			step(err.stage, false, err.msg)
			block(err.reason, err.summary, err.advice)
		} else {
			step(i18n.T("上游连接"), true, i18n.T("经原路由可以连通"))
		}
	} else {
		chk.Steps = append(chk.Steps, model.CaptureCheckStep{Stage: i18n.T("上游连接"), Skip: true, Detail: i18n.T("解密未运行，未检查")})
	}

	// 记录
	switch {
	case len(flows) == 0:
		chk.Steps = append(chk.Steps, model.CaptureCheckStep{Stage: i18n.T("请求记录"), Skip: true, Detail: i18n.T("尚未观察到这个目标的连接（已建立的长连接要断开重连后才会经过抓包）")})
		if chk.Reason == "" {
			block(model.CaptureReasonNoTraffic, i18n.T("配置无误，尚未观察到新连接"), i18n.T("在应用中重新发起请求；必要时重启应用以断开旧的长连接。"))
		}
	default:
		f := flows[0]
		switch {
		case f.Has(model.FlowDecrypted) || (f.Method != "" && !f.Has(model.FlowPassthrough)):
			step(i18n.T("请求记录"), true, i18n.Tf("已捕获 %d 条，最近：%s %s", len(flows), f.Method, f.Path))
		case strings.HasPrefix(f.Error, agent.ClientRejected):
			step(i18n.T("客户端 TLS"), false, f.Error)
			block(model.CaptureReasonClientTLS, i18n.T("客户端拒绝了澜流签发的证书"),
				i18n.T("该程序可能使用自带信任库或证书固定；系统根证书已安装不代表它会信任。"))
		default:
			step(i18n.T("请求记录"), false, strings.TrimSpace(strings.Join(f.Flags, ",")+" "+f.Error))
		}
	}
	if chk.Reason == "" {
		chk.Reason, chk.Summary = model.CaptureOK, i18n.T("可以抓包")
	}
	return chk
}

func (s *Service) mixedPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.state.settings.MixedPort; p > 0 {
		return p
	}
	return 7890
}

func prefixesOf(ss []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range ss {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out
}

// parseCaptureTarget accepts "host", "host:port" or a URL.
func parseCaptureTarget(t string) (host string, port int, scheme string, err error) {
	t = strings.TrimSpace(t)
	if t == "" {
		return "", 0, "", i18n.Error("请输入要检查的目标")
	}
	scheme = "https"
	if strings.Contains(t, "://") {
		u, perr := url.Parse(t)
		if perr != nil || u.Hostname() == "" {
			return "", 0, "", fmt.Errorf(i18n.T("无法解析网址「%s」"), t)
		}
		scheme = strings.ToLower(u.Scheme)
		host = u.Hostname()
		if p := u.Port(); p != "" {
			port, _ = strconv.Atoi(p)
		}
	} else {
		host = t
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		if h, p, e := net.SplitHostPort(host); e == nil {
			host = h
			port, _ = strconv.Atoi(p)
		}
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if port == 0 {
		port = 443
		if scheme == "http" {
			port = 80
		}
	} else if port == 80 && !strings.Contains(t, "://") {
		scheme = "http"
	}
	if port <= 0 || port > 65535 || host == "" {
		return "", 0, "", fmt.Errorf(i18n.T("无效的目标「%s」"), t)
	}
	return host, port, scheme, nil
}

func portIn(ports string, port int) bool {
	for _, p := range strings.Split(ports, "/") {
		p = strings.TrimSpace(p)
		if lo, hi, ok := strings.Cut(p, "-"); ok {
			a, e1 := strconv.Atoi(lo)
			b, e2 := strconv.Atoi(hi)
			if e1 == nil && e2 == nil && port >= a && port <= b {
				return true
			}
		} else if n, err := strconv.Atoi(p); err == nil && n == port {
			return true
		}
	}
	return false
}

// parentSuggest finds an exact selection that is a parent domain of host
// (the user wrote example.com and expects api.example.com to match).
func parentSuggest(host string, domains []string) string {
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if strings.HasPrefix(d, "+.") || strings.ContainsAny(d, "*?") {
			continue
		}
		if strings.HasSuffix(host, "."+d) {
			return d
		}
	}
	return ""
}

type probeErr struct{ stage, reason, msg, summary, advice string }

// probeUpstream connects to host:port through the return listener (which
// the capture rules skip), so the route is the one a decrypted connection
// takes; for HTTPS it also completes a TLS handshake against system roots.
func probeUpstream(r *captureRun, host string, port int, useTLS bool) *probeErr {
	upFail := func(msg string) *probeErr {
		return &probeErr{i18n.T("上游连接"), model.CaptureReasonUpConnect, msg, i18n.Tf("无法连接 %s:%d", host, port),
			i18n.T("服务不可达或端口未开放，与证书无关，无需重装根证书。")}
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.upPort), 3*time.Second)
	if err != nil {
		return upFail(i18n.Tf("回流入口不可用：%v", err))
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	target := net.JoinHostPort(host, strconv.Itoa(port))
	auth := base64.StdEncoding.EncodeToString([]byte("lanflow:" + r.upPass))
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return upFail(err.Error())
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return upFail(resp.Status)
	}
	// the core answers the CONNECT before dialing: a target that cannot be
	// reached shows as the connection closing right after
	closed := func(err error) bool {
		return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "reset by peer") || strings.Contains(err.Error(), "broken pipe")
	}
	if !useTLS {
		fmt.Fprintf(c, "HEAD / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
		resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodHead})
		if err != nil {
			return upFail(err.Error())
		}
		resp.Body.Close()
		return nil
	}
	tc := tls.Client(&bufConnApp{Conn: c, r: br}, &tls.Config{ServerName: host, NextProtos: []string{"h2", "http/1.1"}})
	if err := tc.Handshake(); err != nil {
		if closed(err) {
			return upFail(err.Error())
		}
		return &probeErr{i18n.T("上游 TLS"), model.CaptureReasonUpTLS, err.Error(), i18n.T("上游服务器证书未通过校验或握手失败"),
			i18n.T("这是服务器一侧的问题，与澜流根证书无关；内网自签名证书需要先被系统信任。")}
	}
	return nil
}

type bufConnApp struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConnApp) Read(p []byte) (int, error) { return b.r.Read(p) }
