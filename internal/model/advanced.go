package model

// AdvancedSettings are the core options behind 设置 › 高级: the rest of
// Mihomo's inbound, system proxy, TUN, DNS and sniffer options beyond
// Lanflow's own switches. Every field's zero value means "not set": the
// composer emits nothing for it, so the core keeps Mihomo's default (or
// Lanflow's usual value) and existing users see no change.
//
// Linux-only options (redir/tproxy ports, auto-redirect, routing-mark) are
// left out on purpose; global-client-fingerprint was removed by the core
// (v1.19.33 reports it as such) and is left out too.
type AdvancedSettings struct {
	// ---- inbound ports and LAN access ----
	HTTPPort  int `json:"httpPort,omitempty"`  // `port`: a plain HTTP(S) proxy besides the mixed port
	SocksPort int `json:"socksPort,omitempty"` // `socks-port`
	// LANAllowedIPs / LANDisallowedIPs restrict who may use the inbound
	// ports when allow-lan is on (`lan-allowed-ips`, `lan-disallowed-ips`), CIDRs.
	LANAllowedIPs    []string `json:"lanAllowedIps,omitempty"`
	LANDisallowedIPs []string `json:"lanDisallowedIps,omitempty"`
	// Authentication is the inbound user list, "user:pass" per entry
	// (`authentication`); SkipAuthPrefixes lists the CIDRs that need none
	// (`skip-auth-prefixes`).
	Authentication   []string `json:"authentication,omitempty"`
	SkipAuthPrefixes []string `json:"skipAuthPrefixes,omitempty"`

	// ---- system proxy ----
	// PACMode points the system proxy at a PAC file Lanflow serves on
	// 127.0.0.1 instead of setting a global HTTP/HTTPS/SOCKS proxy; the
	// bypass list becomes DIRECT rules in it. PACDirectCN also sends .cn
	// and well-known mainland domains DIRECT. PACPort is the PAC server's
	// port (0 = PACDefaultPort).
	PACMode     bool `json:"pacMode,omitempty"`
	PACDirectCN bool `json:"pacDirectCn,omitempty"`
	PACPort     int  `json:"pacPort,omitempty"`

	// ---- TUN ----
	TUNStrictRoute            bool     `json:"tunStrictRoute,omitempty"`            // `strict-route`
	TUNMTU                    int      `json:"tunMtu,omitempty"`                    // `mtu`
	TUNEndpointIndependentNAT bool     `json:"tunEndpointIndependentNat,omitempty"` // `endpoint-independent-nat`
	TUNRouteAddress           []string `json:"tunRouteAddress,omitempty"`           // `route-address`: only these ranges enter the TUN
	TUNRouteExcludeAddress    []string `json:"tunRouteExcludeAddress,omitempty"`    // `route-exclude-address`, merged with the aTrust layer
	TUNDNSHijack              []string `json:"tunDnsHijack,omitempty"`              // `dns-hijack`; empty = Lanflow's default (any:53, tcp://any:53)
	TUNDevice                 string   `json:"tunDevice,omitempty"`                 // `device`; empty = Lanflow's fixed name (utun1700 on macOS)
	TUNIncludeInterface       []string `json:"tunIncludeInterface,omitempty"`       // `include-interface`
	TUNExcludeInterface       []string `json:"tunExcludeInterface,omitempty"`       // `exclude-interface`

	// ---- core behaviour ----
	IPv6              bool   `json:"ipv6,omitempty"`              // `ipv6`: let the core resolve and connect over IPv6
	UnifiedDelay      bool   `json:"unifiedDelay,omitempty"`      // `unified-delay`
	TCPConcurrent     bool   `json:"tcpConcurrent,omitempty"`     // `tcp-concurrent`
	FindProcessMode   string `json:"findProcessMode,omitempty"`   // `find-process-mode`: "" (core default) / off / strict / always
	KeepAliveInterval int    `json:"keepAliveInterval,omitempty"` // `keep-alive-interval`, seconds
	KeepAliveIdle     int    `json:"keepAliveIdle,omitempty"`     // `keep-alive-idle`, seconds
	DisableKeepAlive  bool   `json:"disableKeepAlive,omitempty"`  // `disable-keep-alive`
	InterfaceName     string `json:"interfaceName,omitempty"`     // `interface-name`: bind outbound traffic to this interface
	GeodataLoader     string `json:"geodataLoader,omitempty"`     // `geodata-loader`: "" / memconservative / standard
	DisableETag       bool   `json:"disableEtag,omitempty"`       // emits `etag-support: false` (the core's default is true)
	StoreFakeIP       bool   `json:"storeFakeIp,omitempty"`       // `profile.store-fake-ip`
	GlobalUA          string `json:"globalUa,omitempty"`          // `global-ua`: User-Agent for the core's own downloads

	// ---- DNS extras (applied to the dns section in force) ----
	DNSListen            string            `json:"dnsListen,omitempty"`            // `dns.listen`, e.g. 127.0.0.1:1053
	DNSPreferH3          bool              `json:"dnsPreferH3,omitempty"`          // `dns.prefer-h3`
	DNSCacheAlgorithm    string            `json:"dnsCacheAlgorithm,omitempty"`    // `dns.cache-algorithm`: "" / lru / arc
	DNSDefaultNameserver []string          `json:"dnsDefaultNameserver,omitempty"` // `dns.default-nameserver`
	DNSFallback          []string          `json:"dnsFallback,omitempty"`          // `dns.fallback`
	DNSFallbackFilter    DNSFallbackFilter `json:"dnsFallbackFilter,omitempty"`

	// ---- sniffer extras ----
	// SnifferNoForceDNSMapping emits `sniffer.force-dns-mapping: false`
	// (the core's default is true). parse-pure-ip lives in SnifferSettings.
	SnifferNoForceDNSMapping bool `json:"snifferNoForceDnsMapping,omitempty"`
}

// DNSFallbackFilter is `dns.fallback-filter`: when to trust a fallback
// answer over the nameserver's. Zero = not emitted.
type DNSFallbackFilter struct {
	GeoIP     bool     `json:"geoip,omitempty"`
	GeoIPCode string   `json:"geoipCode,omitempty"`
	IPCIDR    []string `json:"ipcidr,omitempty"`
	Domain    []string `json:"domain,omitempty"`
}

// IsZero reports whether the filter sets nothing.
func (f DNSFallbackFilter) IsZero() bool {
	return !f.GeoIP && f.GeoIPCode == "" && len(f.IPCIDR) == 0 && len(f.Domain) == 0
}

// PACDefaultPort is the PAC server's port when AdvancedSettings.PACPort is 0.
const PACDefaultPort = 11233

// Allowed values of the enumerated advanced fields ("" is always allowed
// and means the core's default).
var (
	FindProcessModes   = []string{"off", "strict", "always"}
	GeodataLoaders     = []string{"memconservative", "standard"}
	DNSCacheAlgorithms = []string{"lru", "arc"}
)

// Clone returns a deep copy, so settings snapshots never share slices.
func (a AdvancedSettings) Clone() AdvancedSettings {
	cp := func(s []string) []string {
		if s == nil {
			return nil
		}
		return append([]string(nil), s...)
	}
	a.LANAllowedIPs = cp(a.LANAllowedIPs)
	a.LANDisallowedIPs = cp(a.LANDisallowedIPs)
	a.Authentication = cp(a.Authentication)
	a.SkipAuthPrefixes = cp(a.SkipAuthPrefixes)
	a.TUNRouteAddress = cp(a.TUNRouteAddress)
	a.TUNRouteExcludeAddress = cp(a.TUNRouteExcludeAddress)
	a.TUNDNSHijack = cp(a.TUNDNSHijack)
	a.TUNIncludeInterface = cp(a.TUNIncludeInterface)
	a.TUNExcludeInterface = cp(a.TUNExcludeInterface)
	a.DNSDefaultNameserver = cp(a.DNSDefaultNameserver)
	a.DNSFallback = cp(a.DNSFallback)
	a.DNSFallbackFilter.IPCIDR = cp(a.DNSFallbackFilter.IPCIDR)
	a.DNSFallbackFilter.Domain = cp(a.DNSFallbackFilter.Domain)
	return a
}
