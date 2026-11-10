package profile

import (
	"net/netip"
	"slices"

	"gopkg.in/yaml.v3"
)

// layerCoexist attributes the lines added for a VPN running beside Lanflow.
const layerCoexist = "aTrust 兼容"

// TUNDevice is the name Lanflow gives its TUN interface (macOS utunN), so
// it can always be told apart from other tunnels such as aTrust's.
const TUNDevice = "utun1700"

// Coexist describes a split-tunnel VPN (Sangfor aTrust) running beside
// Lanflow and how its traffic is kept out of Lanflow. The app works the
// lists out from detection and the user's configuration; the zero value
// adds nothing.
type Coexist struct {
	Processes     []string // the VPN's own processes: DIRECT
	ExcludeCIDRs  []string // never enter the TUN (route-exclude-address) and DIRECT
	DirectCIDRs   []string // DIRECT only; still pass through the TUN so DNS to them stays captured
	CorpDomains   []string // intranet suffixes: DIRECT, no fake IP, no sniffing; CorpDNS resolves them
	DirectDomains []string // public services to reach directly: DIRECT, no fake IP, no sniffing; PublicDNS resolves them
	CorpDNS       []string // company DNS servers ("" = leave intranet names to the normal resolvers)
	// PublicDNS resolves DirectDomains and, when UDPFallback is set, is
	// added to the nameservers: encrypted DNS (DoH/DoT) is often cut while
	// aTrust is connected and plain UDP is the one that still answers.
	PublicDNS   []string
	UDPFallback bool
}

func (co Coexist) empty() bool {
	return len(co.Processes) == 0 && len(co.ExcludeCIDRs) == 0 && len(co.DirectCIDRs) == 0 &&
		len(co.CorpDomains) == 0 && len(co.DirectDomains) == 0 && !co.UDPFallback
}

// Address ranges tried, in order, for fake-ip and the TUN interface when
// the default one overlaps a VPN route.
var (
	fakeIPCandidates  = []string{"198.18.0.1/16", "198.19.0.1/16", "28.0.0.1/8", "11.0.0.1/16"}
	tunAddrCandidates = []string{"198.18.0.1/30", "172.19.0.1/30", "28.255.0.1/30", "11.255.0.1/30"}
)

func prefixes(ss []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range ss {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
			out = append(out, netip.PrefixFrom(a, 32))
		}
	}
	return out
}

// applyCoexist adds the coexistence layer: rules first in the list, DNS
// policy / fake-ip-filter / sniffer skip for the configured domains, the
// excluded ranges kept out of the TUN, and fake-ip / TUN addresses moved
// off any range the VPN uses.
func applyCoexist(c *composer, co Coexist, tunOn bool) {
	if co.empty() {
		return
	}
	c.layer = layerCoexist
	excl, direct := prefixes(co.ExcludeCIDRs), prefixes(co.DirectCIDRs)
	vpn := append(append([]netip.Prefix(nil), excl...), direct...)
	domains := append(append([]string(nil), co.CorpDomains...), co.DirectDomains...)

	// rules: ahead of everything else
	var items []*yaml.Node
	seen := map[string]bool{}
	add := func(r string) {
		if !seen[r] {
			seen[r] = true
			items = append(items, c.mark(strNode(r)))
		}
	}
	for _, p := range co.Processes {
		add("PROCESS-NAME," + p + ",DIRECT")
	}
	for _, d := range domains {
		add("DOMAIN-SUFFIX," + d + ",DIRECT")
	}
	for _, p := range vpn {
		add("IP-CIDR," + p.String() + ",DIRECT,no-resolve")
	}
	rs := c.seq("rules")
	kept := rs.Content[:0:0]
	for _, n := range rs.Content {
		if !seen[n.Value] { // an override (e.g. an imported script) may hold the same rule
			kept = append(kept, n)
		}
	}
	rs.Content = append(items, kept...)

	// DNS
	if d := deref(mapGet(c.root, "dns")); d != nil && d.Kind == yaml.MappingNode {
		if len(domains) > 0 {
			appendUnique(c, d, "fake-ip-filter", prefixAll("+.", domains))
		}
		policy := deref(mapGet(d, "nameserver-policy"))
		if (len(co.CorpDNS) > 0 && len(co.CorpDomains) > 0) || (len(co.PublicDNS) > 0 && len(co.DirectDomains) > 0) {
			if policy == nil || policy.Kind != yaml.MappingNode {
				policy = mapNode()
				mapSet(d, "nameserver-policy", policy)
			}
			if len(co.CorpDNS) > 0 {
				for _, dom := range co.CorpDomains {
					mapSet(policy, "+."+dom, c.mark(strSeq(co.CorpDNS)))
				}
			}
			if len(co.PublicDNS) > 0 {
				for _, dom := range co.DirectDomains {
					mapSet(policy, "+."+dom, c.mark(strSeq(co.PublicDNS)))
				}
			}
		}
		if co.UDPFallback && len(co.PublicDNS) > 0 {
			appendUnique(c, d, "nameserver", co.PublicDNS)
			appendUnique(c, d, "proxy-server-nameserver", co.PublicDNS)
		}
		cur := "198.18.0.1/16"
		if v := deref(mapGet(d, "fake-ip-range")); v != nil && v.Kind == yaml.ScalarNode && v.Value != "" {
			cur = v.Value
		}
		if overlapsAny(cur, vpn) {
			if alt := firstFree(fakeIPCandidates, vpn); alt != "" {
				mapSet(d, "fake-ip-range", c.mark(strNode(alt)))
			}
		}
	}

	// sniffer: never rewrite the destination of intranet / direct names
	if len(domains) > 0 {
		if sn := deref(mapGet(c.root, "sniffer")); sn != nil && sn.Kind == yaml.MappingNode {
			appendUnique(c, sn, "skip-domain", prefixAll("+.", domains))
		}
	}

	if !tunOn {
		return
	}
	tun := deref(mapGet(c.root, "tun"))
	if tun == nil || tun.Kind != yaml.MappingNode {
		return
	}
	if len(excl) > 0 {
		var ss []string
		for _, p := range excl {
			ss = append(ss, p.String())
		}
		appendUnique(c, tun, "route-exclude-address", ss)
	}
	addrs := []string{"198.18.0.1/30"}
	if v := deref(mapGet(tun, "inet4-address")); v != nil && v.Kind == yaml.SequenceNode && len(v.Content) > 0 {
		addrs = addrs[:0]
		for _, n := range v.Content {
			addrs = append(addrs, n.Value)
		}
	}
	for _, a := range addrs {
		if overlapsAny(a, vpn) {
			if alt := firstFree(tunAddrCandidates, vpn); alt != "" {
				mapSet(tun, "inet4-address", c.mark(strSeq([]string{alt})))
			}
			break
		}
	}
}

// appendUnique appends items to the sequence m[key] (created when absent),
// skipping ones already there.
func appendUnique(c *composer, m *yaml.Node, key string, items []string) {
	seq := deref(mapGet(m, key))
	if seq == nil || seq.Kind != yaml.SequenceNode {
		seq = seqNode()
		mapSet(m, key, seq)
	}
	have := map[string]bool{}
	for _, n := range seq.Content {
		have[n.Value] = true
	}
	for _, s := range items {
		if !have[s] {
			have[s] = true
			seq.Content = append(seq.Content, c.mark(strNode(s)))
		}
	}
}

func prefixAll(pre string, ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = pre + s
	}
	return out
}

func overlapsAny(cidr string, ps []netip.Prefix) bool {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(ps, func(q netip.Prefix) bool { return q.Overlaps(p.Masked()) })
}

func firstFree(cands []string, ps []netip.Prefix) string {
	for _, c := range cands {
		if !overlapsAny(c, ps) {
			return c
		}
	}
	return ""
}
