//go:build windows

package netinfo

import "context"

// physicalInterface picks, among the up adapters with an IPv4 default
// gateway, the non-virtual one with the lowest IPv4 metric (Wintun and
// other IF_TYPE_PROP_VIRTUAL adapters, and those holding a 198.18.0.0/15
// TUN address, never qualify).
func physicalInterface(context.Context) (Physical, error) {
	adapters, err := listAdapters()
	if err != nil {
		return Physical{}, err
	}
	var cs []candidate
	for _, a := range adapters {
		if a.gateway == "" {
			continue
		}
		c := candidate{name: a.name, index: int(a.index), tunnel: a.tunnel, metric: a.metric, dns: parseAddrs(a.dns)}
		if gw := parseAddrs([]string{a.gateway}); len(gw) == 1 {
			c.gateway = gw[0]
		}
		cs = append(cs, c)
	}
	c, err := pickPhysical(cs)
	if err != nil {
		return Physical{}, err
	}
	return c.physical(), nil
}
