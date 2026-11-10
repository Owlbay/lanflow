package profile

import (
	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

// applyAdvanced writes the 高级 options (model.AdvancedSettings) in the
// Lanflow layer: only fields that are set are emitted, so a zero value
// leaves the core on Mihomo's default or Lanflow's usual value. It runs
// after the managed fields (so it may override the TUN defaults) and
// before the aTrust layer (so route-exclude-address merges with it). The
// inbound ports are set by applyAdvancedPorts after the subscription's
// are stripped.
func applyAdvanced(c *composer, a model.AdvancedSettings) {
	c.layer = layerLanflow

	// ---- LAN access ----
	if len(a.LANAllowedIPs) > 0 {
		c.set("lan-allowed-ips", strSeq(a.LANAllowedIPs))
	}
	if len(a.LANDisallowedIPs) > 0 {
		c.set("lan-disallowed-ips", strSeq(a.LANDisallowedIPs))
	}
	if len(a.Authentication) > 0 {
		c.set("authentication", strSeq(a.Authentication))
	}
	if len(a.SkipAuthPrefixes) > 0 {
		c.set("skip-auth-prefixes", strSeq(a.SkipAuthPrefixes))
	}

	// ---- core behaviour ----
	if a.UnifiedDelay {
		c.set("unified-delay", boolNode(true))
	}
	if a.TCPConcurrent {
		c.set("tcp-concurrent", boolNode(true))
	}
	if a.FindProcessMode != "" {
		c.set("find-process-mode", strNode(a.FindProcessMode))
	}
	if a.KeepAliveInterval > 0 {
		c.set("keep-alive-interval", intNode(a.KeepAliveInterval))
	}
	if a.KeepAliveIdle > 0 {
		c.set("keep-alive-idle", intNode(a.KeepAliveIdle))
	}
	if a.DisableKeepAlive {
		c.set("disable-keep-alive", boolNode(true))
	}
	if a.InterfaceName != "" {
		c.set("interface-name", strNode(a.InterfaceName))
	}
	if a.GeodataLoader != "" {
		c.set("geodata-loader", strNode(a.GeodataLoader))
	}
	if a.DisableETag {
		c.set("etag-support", boolNode(false))
	}
	if a.GlobalUA != "" {
		c.set("global-ua", strNode(a.GlobalUA))
	}
	if a.StoreFakeIP {
		mapSet(c.sub("profile"), "store-fake-ip", boolNode(true))
	}

	// ---- TUN ----
	if tun := deref(mapGet(c.root, "tun")); tun != nil && tun.Kind == yaml.MappingNode {
		if a.TUNStrictRoute {
			mapSet(tun, "strict-route", boolNode(true))
		}
		if a.TUNMTU > 0 {
			mapSet(tun, "mtu", intNode(a.TUNMTU))
		}
		if a.TUNEndpointIndependentNAT {
			mapSet(tun, "endpoint-independent-nat", boolNode(true))
		}
		if len(a.TUNRouteAddress) > 0 {
			mapSet(tun, "route-address", strSeq(a.TUNRouteAddress))
		}
		if len(a.TUNRouteExcludeAddress) > 0 {
			appendUnique(c, tun, "route-exclude-address", a.TUNRouteExcludeAddress)
		}
		if len(a.TUNDNSHijack) > 0 {
			mapSet(tun, "dns-hijack", strSeq(a.TUNDNSHijack))
		}
		if a.TUNDevice != "" {
			mapSet(tun, "device", strNode(a.TUNDevice))
		}
		if len(a.TUNIncludeInterface) > 0 {
			mapSet(tun, "include-interface", strSeq(a.TUNIncludeInterface))
		}
		if len(a.TUNExcludeInterface) > 0 {
			mapSet(tun, "exclude-interface", strSeq(a.TUNExcludeInterface))
		}
	}

	// ---- DNS extras: into whichever dns section is in force ----
	if a.DNSListen != "" || a.DNSPreferH3 || a.DNSCacheAlgorithm != "" || len(a.DNSDefaultNameserver) > 0 || len(a.DNSFallback) > 0 || !a.DNSFallbackFilter.IsZero() {
		d := c.sub("dns")
		if a.DNSListen != "" {
			mapSet(d, "listen", strNode(a.DNSListen))
		}
		if a.DNSPreferH3 {
			mapSet(d, "prefer-h3", boolNode(true))
		}
		if a.DNSCacheAlgorithm != "" {
			mapSet(d, "cache-algorithm", strNode(a.DNSCacheAlgorithm))
		}
		if len(a.DNSDefaultNameserver) > 0 {
			mapSet(d, "default-nameserver", strSeq(a.DNSDefaultNameserver))
		}
		if len(a.DNSFallback) > 0 {
			mapSet(d, "fallback", strSeq(a.DNSFallback))
		}
		if f := a.DNSFallbackFilter; !f.IsZero() {
			ff := mapNode("geoip", boolNode(f.GeoIP))
			if f.GeoIPCode != "" {
				mapSet(ff, "geoip-code", strNode(f.GeoIPCode))
			}
			if len(f.IPCIDR) > 0 {
				mapSet(ff, "ipcidr", strSeq(f.IPCIDR))
			}
			if len(f.Domain) > 0 {
				mapSet(ff, "domain", strSeq(f.Domain))
			}
			mapSet(d, "fallback-filter", ff)
		}
	}

	// ---- sniffer extras ----
	if a.SnifferNoForceDNSMapping {
		mapSet(c.sub("sniffer"), "force-dns-mapping", boolNode(false))
	}
}

// applyAdvancedPorts adds the optional plain HTTP and SOCKS ports. It runs
// after strippedKeys removed the subscription's, and never emits a port
// equal to the mixed port or to the other one.
func applyAdvancedPorts(c *composer, a model.AdvancedSettings, mixed int) {
	c.layer = layerLanflow
	if p := a.HTTPPort; p > 0 && p <= 65535 && p != mixed {
		c.set("port", intNode(p))
	}
	if p := a.SocksPort; p > 0 && p <= 65535 && p != mixed && p != a.HTTPPort {
		c.set("socks-port", intNode(p))
	}
}
