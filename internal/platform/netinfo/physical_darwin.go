//go:build darwin

package netinfo

import (
	"context"
	"net"
	"net/netip"
)

// physicalInterface walks the default routes (`route get default` first,
// then every default row of the routing table, which still lists the
// physical one when a TUN has captured the default route) and picks the
// first up, non-virtual interface.
func physicalInterface(parent context.Context) (Physical, error) {
	ctx, cancel := withBudget(parent)
	defer cancel()
	var routes []defaultRoute
	if out, err := runCommand(ctx, "route", "-n", "get", "default"); err == nil {
		routes = append(routes, parseRouteDefault(out))
	}
	if out, err := runCommand(ctx, "netstat", "-rn", "-f", "inet"); err == nil {
		routes = append(routes, parseNetstatDefaults(out)...)
	}
	var dns []netip.Addr
	if out, err := runCommand(ctx, "scutil", "--dns"); err == nil {
		dns = parseAddrs(parseScutilDNS(out))
	}
	var cs []candidate
	for i, r := range routes {
		ifi, err := net.InterfaceByName(r.Interface)
		if err != nil || ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		c := candidate{name: ifi.Name, index: ifi.Index, metric: uint32(i), dns: dns}
		if gw, err := netip.ParseAddr(r.Gateway); err == nil {
			c.gateway = gw
		}
		cs = append(cs, c)
	}
	c, err := pickPhysical(cs)
	if err != nil {
		return Physical{}, err
	}
	return c.physical(), nil
}
