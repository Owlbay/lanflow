package netinfo

import "strings"

// splitDefaults are the destinations TUN clients use to take over the
// default route without replacing it (netstat -rn notation on macOS).
var splitDefaults = map[string]bool{
	"0/1": true, "128.0/1": true, "1": true, "2/7": true, "4/6": true,
	"8/5": true, "16/4": true, "32/3": true, "64/2": true,
}

// parseDarwinTUNRoute finds a non-scoped split-default (or default) route
// through a utun interface in `netstat -rn -f inet` output.
func parseDarwinTUNRoute(out string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		dst, flags, netif := f[0], f[2], f[3]
		if strings.Contains(flags, "I") { // interface-scoped route (e.g. Tailscale's scoped default)
			continue
		}
		if !strings.HasPrefix(netif, "utun") {
			continue
		}
		if splitDefaults[dst] || dst == "default" {
			return netif, true
		}
	}
	return "", false
}

// parseWindowsTUNRoute finds the 0.0.0.0/1 + 128.0.0.0/1 pair in
// `route print -4` output and returns the interface address they use.
func parseWindowsTUNRoute(out string) (string, bool) {
	var low, high string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[1] != "128.0.0.0" {
			continue
		}
		switch f[0] {
		case "0.0.0.0":
			low = f[3]
		case "128.0.0.0":
			high = f[3]
		}
	}
	if low != "" && low == high {
		return low, true
	}
	return "", false
}
