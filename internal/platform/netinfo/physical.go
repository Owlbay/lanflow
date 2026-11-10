package netinfo

import (
	"context"
	"errors"
	"net/netip"
	"sort"
	"strings"
)

// Physical is the physical network interface that carries the default
// route, as opposed to a TUN device (Lanflow's own, Clash Party's, a VPN's)
// that may have captured it. Connections bound to it bypass the TUN.
type Physical struct {
	Name    string       // BSD name ("en0") or Windows friendly name ("Wi-Fi")
	Index   int          // interface index for IP_BOUND_IF / IP_UNICAST_IF
	Gateway netip.Addr   // IPv4 default gateway, invalid when unknown
	DNS     []netip.Addr // resolvers configured for it (may include TUN ones; callers filter)
}

// ErrNoPhysical is returned by PhysicalInterface when no non-tunnel
// interface carries a default route.
var ErrNoPhysical = errors.New("no physical interface with a default route")

// PhysicalInterface returns the physical interface carrying the default
// route, skipping utun/tun/wintun and other virtual adapters, in the way
// Mihomo's auto-detect-interface picks the outbound interface.
func PhysicalInterface(ctx context.Context) (Physical, error) { return physicalInterface(ctx) }

// candidate is one interface with a default route, as the platform code
// collected it; pickPhysical chooses among them.
type candidate struct {
	name    string
	index   int
	tunnel  bool   // the platform knows it is virtual (IfType, TUN address)
	metric  uint32 // lower wins; darwin uses the routing table order
	gateway netip.Addr
	dns     []netip.Addr
}

// tunnelHints are interface names (lower case prefixes) of virtual
// adapters besides those isTunnelName knows: Windows TUN adapters are
// named by the program that creates them.
var tunnelHints = []string{"wintun", "meta", "mihomo", "clash", "sing-box", "singbox", "wireguard", "tailscale", "zerotier", "openvpn", "vethernet", "vmware", "virtualbox"}

// isVirtualName reports whether name looks like a TUN or other virtual
// interface that must not be used as the physical outbound interface.
func isVirtualName(name string) bool {
	if isTunnelName(name) {
		return true
	}
	n := strings.ToLower(strings.TrimSpace(name))
	for _, p := range tunnelHints {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// pickPhysical returns the non-virtual candidate with the lowest metric
// (stable for ties), or ErrNoPhysical.
func pickPhysical(cs []candidate) (candidate, error) {
	var ok []candidate
	for _, c := range cs {
		if c.tunnel || c.index <= 0 || c.name == "" || isVirtualName(c.name) {
			continue
		}
		ok = append(ok, c)
	}
	if len(ok) == 0 {
		return candidate{}, ErrNoPhysical
	}
	sort.SliceStable(ok, func(i, j int) bool { return ok[i].metric < ok[j].metric })
	return ok[0], nil
}

func (c candidate) physical() Physical {
	return Physical{Name: c.name, Index: c.index, Gateway: c.gateway, DNS: c.dns}
}

// parseAddrs parses IP strings, dropping invalid ones.
func parseAddrs(in []string) []netip.Addr {
	var out []netip.Addr
	for _, s := range in {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
			out = append(out, a.Unmap())
		}
	}
	return out
}
