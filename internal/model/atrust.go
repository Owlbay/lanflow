package model

// ATrustConfig is the user's aTrust 兼容 configuration, applied on top of
// what is detected automatically (Settings.ATrust). Nothing here is
// built in: it starts empty and is filled by hand, from the reference
// file (API.ATrustReference) or by importing a configuration such as
// a JavaScript override script (atrust-coexist.js) (API.ImportATrustConfig).
type ATrustConfig struct {
	// Always applies these lists even while aTrust is not running (on the
	// office network intranet names still need the company DNS).
	Always bool `json:",omitempty"`
	// CorpDomains are intranet domain suffixes: DIRECT, never fake-ip, not
	// sniffed, resolved by CorpDNS when it is set.
	CorpDomains []string `json:",omitempty"`
	// DirectDomains are public services that must connect directly (e.g.
	// the company's SaaS): DIRECT, never fake-ip, not sniffed, resolved by
	// public DNS (never the company DNS, which may be unreachable).
	DirectDomains []string `json:",omitempty"`
	// CorpDNS are the company DNS servers (IP addresses).
	CorpDNS []string `json:",omitempty"`
	// ExcludeCIDRs never enter Lanflow's TUN (left to aTrust / the system
	// routes) and go DIRECT: aTrust's mapped range, its gateways, intranet
	// ranges reachable only through aTrust.
	ExcludeCIDRs []string `json:",omitempty"`
	// DirectCIDRs go DIRECT but still pass through the TUN, so DNS sent to
	// them is still captured (a range holding the company DNS, e.g.
	// 10.0.0.0/8, must be here rather than in ExcludeCIDRs).
	DirectCIDRs []string `json:",omitempty"`
}

// Empty reports whether nothing is configured.
func (c ATrustConfig) Empty() bool {
	return len(c.CorpDomains) == 0 && len(c.DirectDomains) == 0 && len(c.CorpDNS) == 0 &&
		len(c.ExcludeCIDRs) == 0 && len(c.DirectCIDRs) == 0
}

// Clone returns a deep copy.
func (c ATrustConfig) Clone() ATrustConfig {
	cp := func(s []string) []string {
		if s == nil {
			return nil
		}
		return append([]string(nil), s...)
	}
	return ATrustConfig{Always: c.Always, CorpDomains: cp(c.CorpDomains), DirectDomains: cp(c.DirectDomains),
		CorpDNS: cp(c.CorpDNS), ExcludeCIDRs: cp(c.ExcludeCIDRs), DirectCIDRs: cp(c.DirectCIDRs)}
}
