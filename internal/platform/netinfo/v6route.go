package netinfo

import (
	"context"
	"strings"
)

// V6Route is where the machine's IPv6 default route points. Interface is
// the physical (non-tunnel) interface that carries an IPv6 default route,
// "" when there is none; ViaTUN reports that a TUN owns ::/0, either as a
// plain default route or through the split-default routes TUN clients
// such as Mihomo install (::/1 + 8000::/1, or 100::/8 … 8000::/1), so every
// off-link IPv6 connection enters it. The leak check uses the pair: an
// Interface with ViaTUN false while Lanflow's TUN is on means IPv6 bypasses
// the TUN.
type V6Route struct {
	Interface string
	ViaTUN    bool
}

// IPv6Route reads the IPv6 routing table (netstat on macOS, route print on
// Windows). The zero value when it cannot be read or the platform has no
// reader.
func IPv6Route(ctx context.Context) V6Route { return ipv6Route(ctx) }

// v6SplitDefaults are destinations that, through a TUN, cover public IPv6
// without replacing the default route (netstat notation).
var v6SplitDefaults = map[string]bool{"::/1": true, "2000::/3": true, "8000::/1": true}

// parseDarwinV6Routes reads `netstat -rn -f inet6`: Destination, Gateway,
// Flags, Netif. Interface-scoped routes (flag I, e.g. every utun's own
// "default fe80::%utunN") do not count.
func parseDarwinV6Routes(out string) V6Route {
	var r V6Route
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		dst, flags, netif := f[0], f[2], f[3]
		if strings.Contains(flags, "I") {
			continue
		}
		tun := strings.HasPrefix(netif, "utun")
		switch {
		case dst == "default" && !tun && !isTunnelName(netif):
			if r.Interface == "" {
				r.Interface = netif
			}
		case tun && (dst == "default" || v6SplitDefaults[dst]):
			r.ViaTUN = true
		}
	}
	return r
}

// parseWindowsV6Routes reads the "IPv6 Route Table" of `route print -6`:
// If, Metric, Network Destination, Gateway. A TUN's default is on-link on
// its adapter; a physical adapter's default has the router's link-local
// address as its gateway. Interface is reported as the adapter index.
func parseWindowsV6Routes(out string) V6Route {
	var r V6Route
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		dst, gw := f[2], f[3]
		if dst != "::/0" && !v6SplitDefaults[dst] {
			continue
		}
		if strings.EqualFold(gw, "On-link") {
			r.ViaTUN = true
		} else if dst == "::/0" && r.Interface == "" {
			r.Interface = "if#" + f[0]
		}
	}
	return r
}
