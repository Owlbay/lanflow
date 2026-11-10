package app

import (
	"net"
	"net/netip"
	"reflect"
	"slices"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/platform/atrust"
	"lanflow/internal/platform/sysdns"
	"lanflow/internal/profile"
)

// aTrust 兼容 (Sangfor aTrust running beside Lanflow). aTrust is a
// split-tunnel VPN: it routes the intranet ranges into its own utun and
// answers the intranet domains with its own resolver. Lanflow's TUN would
// take those packets first, its fake-ip DNS would hand out fake addresses
// for intranet names, and the subscription's MATCH rule would send intranet
// requests to a remote node. While aTrust runs (and the setting is on), or
// always when the user's lists say so, the composed config therefore:
//   - keeps the intranet ranges out of the TUN (tun.route-exclude-address),
//     except ranges holding a DNS server, which stay in the TUN so the DNS
//     hijack keeps working, and go DIRECT only;
//   - excludes aTrust's own gateways, so a core restart never cuts its tunnel;
//   - sends aTrust's processes, the intranet ranges and domains DIRECT,
//   - never gives intranet / direct names a fake IP (dns.fake-ip-filter),
//     never sniffs them, and resolves them with the company / public DNS
//     (dns.nameserver-policy),
//   - falls back to plain UDP public DNS while aTrust runs (its tunnel
//     tends to cut DoH/DoT),
//   - moves fake-ip / the TUN address off a range aTrust uses,
//
// and the system proxy bypasses the intranet. The automatic system DNS
// stays: it changes the network services' DNS only. Detection is repeated
// by the system watcher, so connecting or disconnecting aTrust re-applies
// all of it; changing the lists in the settings does too.

const coexistInterval = 10 * time.Second

// publicDNS are the plain-UDP public resolvers that still answer while
// aTrust is connected (the ones the TUN mode already points the system
// at); they resolve the direct domains and back the nameservers up.
var publicDNS = sysdns.TUNServers

// ownTUNRanges are the addresses Lanflow's TUN may carry (profile's
// candidates), so its own utun is never taken for aTrust's.
var ownTUNRanges = []netip.Prefix{
	netip.MustParsePrefix("198.18.0.0/30"), netip.MustParsePrefix("172.19.0.0/30"),
	netip.MustParsePrefix("28.255.0.0/30"), netip.MustParsePrefix("11.255.0.0/30"),
}

// isOwnTUN reports whether iface is Lanflow's TUN: the fixed device name
// first, else (an older core that ignored the name) one of its addresses.
func isOwnTUN(iface string) bool {
	if iface == profile.TUNDevice {
		return true
	}
	i, err := net.InterfaceByName(iface)
	if err != nil {
		return false
	}
	addrs, _ := i.Addrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if slices.ContainsFunc(ownTUNRanges, func(p netip.Prefix) bool { return p.Contains(ip) }) {
			return true
		}
	}
	return false
}

// detectCoexist looks for aTrust (when the setting is on) and stores the
// result; it reports whether anything changed. Never call with mu held.
func (s *Service) detectCoexist() bool {
	s.mu.Lock()
	set := s.state.settings
	on := set.ATrustCompat
	detect := s.d.vpnDetect
	prev := s.state.coexist
	s.mu.Unlock()
	var info atrust.Info
	if on && detect != nil && s.rootCtx.Err() == nil {
		ctx, cancel := s.ctrlCtx(nil, 8*time.Second)
		info = detect(ctx, isOwnTUN)
		cancel()
	}
	if info.Key() == prev.Key() {
		return false
	}
	if info.Running() {
		s.alog.Infof("aTrust: processes %v, interfaces %v, ranges %v, domains %v, DNS %v, gateways %v", info.Processes, info.Ifaces, info.CIDRs, info.Domains, info.DNS, info.Gateways)
	} else {
		s.alog.Infof("aTrust: not running (compatibility setting %s)", onOff(on))
	}
	s.mu.Lock()
	s.state.coexist = info
	co := coexistInput(info, s.state.settings, s.state.net.DNS)
	s.state.st.ATrust = coexistStatus(info, co)
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	switch {
	case info.Running() && !prev.Running():
		ranges := len(co.ExcludeCIDRs) + len(co.DirectCIDRs)
		domains := len(co.CorpDomains) + len(co.DirectDomains)
		detail := i18n.Tf("内网网段 %d 个、内网域名 %d 个将直接连接，不经过澜流的节点和 TUN。", ranges, domains)
		if ranges == 0 && domains == 0 {
			detail = i18n.T("aTrust 自身的流量将直接连接。aTrust 连上后，内网网段和域名会自动加入。")
		}
		s.notify(Notice{Kind: "toast", Title: i18n.T("检测到 aTrust，已启用兼容模式"), Detail: detail})
	case !info.Running() && prev.Running() && on:
		s.notify(Notice{Kind: "toast", Title: i18n.T("aTrust 已退出，兼容模式已关闭")})
	}
	return true
}

// coexistStatus is the Status.ATrust for a detection result and the layer
// built from it.
func coexistStatus(i atrust.Info, co profile.Coexist) model.ATrustState {
	return model.ATrustState{
		Running: i.Running(), Processes: i.Processes, Interfaces: i.Ifaces,
		CIDRs: i.CIDRs, Domains: i.Domains, DNS: i.DNS, Gateways: i.Gateways,
		Applied: !coexistEmpty(co),
	}
}

func coexistEmpty(co profile.Coexist) bool {
	return reflect.DeepEqual(co, profile.Coexist{})
}

// coexistApplies reports whether the coexistence layer is wanted: the
// setting is on and aTrust runs, or the user's lists apply always.
func coexistApplies(i atrust.Info, set model.Settings) bool {
	return set.ATrustCompat && (i.Running() || (set.ATrust.Always && !set.ATrust.Empty()))
}

// coexistInput builds the composer's layer from the detection result, the
// user's lists (Settings.ATrust) and the DNS servers in use: sysDNS are the
// system's current servers. The zero value means no layer.
//
// A detected range that holds a DNS server (the system's, aTrust's own or
// a configured company server) is never kept out of the TUN: the TUN's
// DNS hijack would miss the queries sent to it and every name would be
// resolved behind the core's back. Such ranges only go DIRECT.
func coexistInput(i atrust.Info, set model.Settings, sysDNS []string) profile.Coexist {
	if !coexistApplies(i, set) {
		return profile.Coexist{}
	}
	cfg := set.ATrust
	var dnsAddrs []netip.Addr
	for _, lst := range [][]string{sysDNS, i.DNS, cfg.CorpDNS} {
		for _, s := range lst {
			if a, err := netip.ParseAddr(s); err == nil {
				dnsAddrs = append(dnsAddrs, a.Unmap())
			}
		}
	}
	holdsDNS := func(cidr string) bool {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			if a, err := netip.ParseAddr(cidr); err == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			} else {
				return false
			}
		}
		return slices.ContainsFunc(dnsAddrs, p.Contains)
	}
	var co profile.Coexist
	add := func(dst *[]string, vs ...string) {
		for _, v := range vs {
			if v != "" && !slices.Contains(*dst, v) {
				*dst = append(*dst, v)
			}
		}
	}
	if i.Running() {
		add(&co.Processes, i.Processes...)
		for _, c := range i.CIDRs {
			if holdsDNS(c) {
				add(&co.DirectCIDRs, c)
			} else {
				add(&co.ExcludeCIDRs, c)
			}
		}
		for _, g := range i.Gateways {
			if a, err := netip.ParseAddr(g); err == nil {
				add(&co.ExcludeCIDRs, netip.PrefixFrom(a, a.BitLen()).String())
			}
		}
		add(&co.CorpDomains, i.Domains...)
		co.UDPFallback = true
	}
	add(&co.ExcludeCIDRs, cfg.ExcludeCIDRs...)
	for _, c := range cfg.DirectCIDRs {
		if !slices.Contains(co.ExcludeCIDRs, c) {
			add(&co.DirectCIDRs, c)
		}
	}
	add(&co.CorpDomains, cfg.CorpDomains...)
	for _, d := range cfg.DirectDomains {
		if !slices.Contains(co.CorpDomains, d) {
			add(&co.DirectDomains, d)
		}
	}
	add(&co.CorpDNS, cfg.CorpDNS...)
	if len(co.CorpDNS) == 0 { // the detected servers answer aTrust's own domains
		add(&co.CorpDNS, i.DNS...)
	}
	co.PublicDNS = append([]string(nil), publicDNS...)
	return co
}

// coexistBypass extends the system proxy bypass list with everything the
// layer sends DIRECT: the domains and the ranges.
func coexistBypass(base []string, co profile.Coexist) []string {
	out := append([]string(nil), base...)
	add := func(v string) {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	for _, d := range co.CorpDomains {
		add(d)
		add("*." + d)
	}
	for _, d := range co.DirectDomains {
		add(d)
		add("*." + d)
	}
	for _, c := range co.ExcludeCIDRs {
		add(c)
	}
	for _, c := range co.DirectCIDRs {
		add(c)
	}
	return out
}

// coexistLocked is the layer for the current state. Call with mu held.
func (s *Service) coexistLocked() profile.Coexist {
	return coexistInput(s.state.coexist, s.state.settings, s.state.net.DNS)
}

// bypassLocked is the system proxy bypass list in effect now. Call with mu held.
func (s *Service) bypassLocked() []string {
	return coexistBypass(s.state.settings.Bypass, s.coexistLocked())
}

// bypassList is the system proxy bypass list in effect now.
func (s *Service) bypassList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bypassLocked()
}

// coexistActive reports whether the aTrust adjustments are in effect.
func (s *Service) coexistActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return coexistApplies(s.state.coexist, s.state.settings)
}

// coexistSettingsChanged reports whether going from old to in (already
// stored) changes the layer, and refreshes Status.ATrust when it does.
// Never call with mu held.
func (s *Service) coexistSettingsChanged(old, in model.Settings) bool {
	if reflect.DeepEqual(old.ATrust, in.ATrust) && old.ATrustCompat == in.ATrustCompat {
		return false
	}
	s.mu.Lock()
	info, sysDNS := s.state.coexist, s.state.net.DNS
	before, after := coexistInput(info, old, sysDNS), coexistInput(info, in, sysDNS)
	if reflect.DeepEqual(before, after) {
		s.mu.Unlock()
		return false
	}
	s.state.st.ATrust = coexistStatus(info, after)
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	return true
}

// recheckCoexist detects aTrust again and, when that changed, re-applies the
// config, the system proxy bypass and the system DNS.
func (s *Service) recheckCoexist() {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.detectCoexist() {
		s.applyCoexist()
	}
}

// applyCoexist re-applies everything coexistence touches. Call with opMu held.
func (s *Service) applyCoexist() {
	if s.running() {
		if err := s.reloadLocked(); err != nil {
			s.notify(Notice{Kind: "error", Title: i18n.T("aTrust 兼容设置未能生效"), Detail: err.Error()})
		}
	}
	s.mu.Lock()
	ours := s.state.weSetProxy
	port := s.state.settings.MixedPort
	s.mu.Unlock()
	if ours {
		if err := s.d.sys.Set("127.0.0.1", port, s.bypassList()); err != nil {
			s.notify(Notice{Kind: "error", Title: i18n.T("更新系统代理例外列表失败"), Detail: err.Error()})
		}
	}
	s.syncDNS()
}

// watchCoexist polls for aTrust until shutdown.
func (s *Service) watchCoexist() {
	s.recheckCoexist()
	t := time.NewTicker(coexistInterval)
	defer t.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-t.C:
			s.recheckCoexist()
		}
	}
}
