package app

import (
	"net/netip"
	"reflect"
	"slices"
	"strings"

	"lanflow/internal/controller"
	"lanflow/internal/model"
	"lanflow/internal/platform/sysproxy"
)

// 设置 › 高级 (model.AdvancedSettings): normalisation, what the core takes
// hot and what needs a reload, and the system-proxy ownership check that
// knows about PAC mode. The PAC server itself is in pac.go.

// normalizeAdvanced drops values the core would reject or that collide
// with Lanflow's own: an invalid field falls back to its zero value, which
// the composer does not emit, so a bad settings file never produces a
// config the core refuses.
func normalizeAdvanced(a model.AdvancedSettings, mixedPort int) model.AdvancedSettings {
	a = a.Clone()
	port := func(p int) int {
		if p <= 0 || p > 65535 || p == mixedPort {
			return 0
		}
		return p
	}
	a.HTTPPort = port(a.HTTPPort)
	a.SocksPort = port(a.SocksPort)
	if a.SocksPort == a.HTTPPort {
		a.SocksPort = 0
	}
	if a.PACPort <= 0 || a.PACPort > 65535 || a.PACPort == mixedPort {
		a.PACPort = 0
	}
	a.LANAllowedIPs = cidrs(a.LANAllowedIPs)
	a.LANDisallowedIPs = cidrs(a.LANDisallowedIPs)
	a.SkipAuthPrefixes = cidrs(a.SkipAuthPrefixes)
	a.Authentication = cleanList(a.Authentication, func(s string) bool { u, _, ok := strings.Cut(s, ":"); return ok && u != "" })
	a.TUNRouteAddress = cidrs(a.TUNRouteAddress)
	a.TUNRouteExcludeAddress = cidrs(a.TUNRouteExcludeAddress)
	a.TUNDNSHijack = cleanList(a.TUNDNSHijack, nil)
	a.TUNIncludeInterface = cleanList(a.TUNIncludeInterface, nil)
	a.TUNExcludeInterface = cleanList(a.TUNExcludeInterface, nil)
	a.TUNDevice = strings.TrimSpace(a.TUNDevice)
	if a.TUNMTU != 0 && (a.TUNMTU < 576 || a.TUNMTU > 65535) {
		a.TUNMTU = 0
	}
	if !slices.Contains(model.FindProcessModes, a.FindProcessMode) {
		a.FindProcessMode = ""
	}
	if !slices.Contains(model.GeodataLoaders, a.GeodataLoader) {
		a.GeodataLoader = ""
	}
	if !slices.Contains(model.DNSCacheAlgorithms, a.DNSCacheAlgorithm) {
		a.DNSCacheAlgorithm = ""
	}
	if a.KeepAliveInterval < 0 {
		a.KeepAliveInterval = 0
	}
	if a.KeepAliveIdle < 0 {
		a.KeepAliveIdle = 0
	}
	a.InterfaceName = strings.TrimSpace(a.InterfaceName)
	a.GlobalUA = strings.TrimSpace(a.GlobalUA)
	a.DNSListen = strings.TrimSpace(a.DNSListen)
	if a.DNSListen != "" {
		if _, err := netip.ParseAddrPort(a.DNSListen); err != nil {
			a.DNSListen = ""
		}
	}
	a.DNSDefaultNameserver = cleanList(a.DNSDefaultNameserver, nil)
	a.DNSFallback = cleanList(a.DNSFallback, nil)
	a.DNSFallbackFilter.GeoIPCode = strings.ToUpper(strings.TrimSpace(a.DNSFallbackFilter.GeoIPCode))
	a.DNSFallbackFilter.IPCIDR = cidrs(a.DNSFallbackFilter.IPCIDR)
	a.DNSFallbackFilter.Domain = cleanList(a.DNSFallbackFilter.Domain, nil)
	return a
}

// cleanList trims the entries, drops empty ones and those keep rejects;
// nil when nothing is left.
func cleanList(in []string, keep func(string) bool) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && (keep == nil || keep(s)) {
			out = append(out, s)
		}
	}
	return out
}

// cidrs keeps the entries that parse as a CIDR or a single address (which
// becomes a /32 or /128).
func cidrs(in []string) []string {
	return cleanList(in, func(s string) bool {
		if _, err := netip.ParsePrefix(s); err == nil {
			return true
		}
		_, err := netip.ParseAddr(s)
		return err == nil
	})
}

// advancedPatch returns the PATCH /configs body for the 高级 fields the
// core applies hot, and whether any changed.
func advancedPatch(old, in model.AdvancedSettings) (controller.ConfigPatch, bool) {
	var p controller.ConfigPatch
	changed := false
	if in.HTTPPort != old.HTTPPort {
		v := in.HTTPPort
		p.Port, changed = &v, true
	}
	if in.SocksPort != old.SocksPort {
		v := in.SocksPort
		p.SocksPort, changed = &v, true
	}
	if in.TCPConcurrent != old.TCPConcurrent {
		v := in.TCPConcurrent
		p.TCPConcurrent, changed = &v, true
	}
	if in.InterfaceName != old.InterfaceName {
		v := in.InterfaceName
		p.InterfaceName, changed = &v, true
	}
	// IPv6 is not patched hot: the core's own `ipv6` always stays on (the
	// TUN would lose its inet6 address otherwise, profile.TUNInet6Address)
	// and the setting is applied by the composer (dns.ipv6 and the IPv6
	// reject rule), which takes a reload.
	return p, changed
}

// advancedNeedsReload reports whether a 高级 change touches the composed
// config in a way PATCH /configs cannot apply. The PAC fields never reach
// the core and the hot fields go through advancedPatch.
func advancedNeedsReload(old, in model.AdvancedSettings) bool {
	strip := func(a model.AdvancedSettings) model.AdvancedSettings {
		a.PACMode, a.PACDirectCN, a.PACPort = false, false, 0
		a.HTTPPort, a.SocksPort, a.TCPConcurrent, a.InterfaceName = 0, 0, false, ""
		return a
	}
	return !reflect.DeepEqual(strip(old), strip(in))
}

// pacChanged reports whether the system proxy has to be set up again
// because the way it is set up changed (global proxy ↔ PAC, PAC port).
func pacChanged(old, in model.AdvancedSettings) bool {
	return old.PACMode != in.PACMode || (in.PACMode && old.PACPort != in.PACPort)
}

// proxyIsOurs reports whether the system proxy still is what Lanflow set:
// the PAC URL recorded in the marker, or a manual proxy at 127.0.0.1:port.
func proxyIsOurs(cur sysproxy.State, m proxyMarker, port int) bool {
	if m.PAC != "" {
		return cur.PACURL == m.PAC
	}
	return cur.Enabled && isLoopback(cur.Host) && cur.Port == port
}
