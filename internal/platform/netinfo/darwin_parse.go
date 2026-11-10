package netinfo

// Parsers for the macOS command outputs used by netinfo_darwin.go. They
// carry no build tag so the unit tests run on every host.

import (
	"net"
	"regexp"
	"strconv"
	"strings"
)

// defaultRoute is what `route -n get default` tells us.
type defaultRoute struct {
	Interface string
	Gateway   string
}

// parseRouteDefault reads "interface:" and "gateway:" from `route -n get
// default`. A gateway that is not an IP address (e.g. "link#12" on a TUN)
// is reported as empty.
func parseRouteDefault(out string) defaultRoute {
	var r defaultRoute
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "interface":
			r.Interface = v
		case "gateway":
			if net.ParseIP(v) != nil {
				r.Gateway = v
			}
		}
	}
	return r
}

// parseNetstatDefaults lists the "default" rows of `netstat -rn -f inet`
// in table order (gateway, interface). It is used when the primary route
// goes through a tunnel so the underlying physical route can be shown.
func parseNetstatDefaults(out string) []defaultRoute {
	var routes []defaultRoute
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "default" {
			continue
		}
		r := defaultRoute{Interface: fields[len(fields)-1]}
		// Expire column may trail the interface on some versions.
		if _, err := strconv.Atoi(r.Interface); err == nil && len(fields) >= 5 {
			r.Interface = fields[len(fields)-2]
		}
		if net.ParseIP(fields[1]) != nil {
			r.Gateway = fields[1]
		}
		routes = append(routes, r)
	}
	return routes
}

// parseHardwarePorts maps BSD device names to hardware port names from
// `networksetup -listallhardwareports` ("en0" -> "Wi-Fi").
func parseHardwarePorts(out string) map[string]string {
	ports := map[string]string{}
	var current string
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "Hardware Port":
			current = v
		case "Device":
			if current != "" && v != "" {
				ports[v] = current
			}
			current = ""
		}
	}
	return ports
}

// parseScutilDNS returns the nameservers of resolver #1 from `scutil
// --dns` (falling back to the first resolver that has any), deduplicated.
// Scoped resolvers ("DNS configuration (for scoped queries)") are ignored.
func parseScutilDNS(out string) []string {
	var first, fallback []string
	resolverIdx, fallbackIdx := 0, 0
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "DNS configuration (for scoped queries)") {
			break
		}
		if strings.HasPrefix(line, "resolver #") {
			resolverIdx++
			continue
		}
		if resolverIdx == 0 || !strings.HasPrefix(line, "nameserver[") {
			continue
		}
		_, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		ip := strings.TrimSpace(v)
		// Strip a zone id ("fe80::1%en0") so the UI shows a clean address.
		if i := strings.IndexByte(ip, '%'); i > 0 {
			ip = ip[:i]
		}
		if net.ParseIP(ip) == nil {
			continue
		}
		switch {
		case resolverIdx == 1:
			first = append(first, ip)
		case fallbackIdx == 0 || fallbackIdx == resolverIdx:
			// Only one resolver feeds the fallback list; never merge several.
			fallbackIdx = resolverIdx
			fallback = append(fallback, ip)
		}
	}
	if len(first) > 0 {
		return dedup(first)
	}
	return dedup(fallback)
}

// ipconfigSummary is what we read from `ipconfig getsummary <if>`.
type ipconfigSummary struct {
	SSID string // "" when absent or redacted
	WiFi bool   // InterfaceType : WiFi
}

var ssidLine = regexp.MustCompile(`(?m)^\s*SSID\s*:\s*(.*?)\s*$`)

// parseIPConfigSummary extracts the SSID and interface type. macOS 14+
// prints "<redacted>" unless the app has Location Services permission;
// that is reported as an empty SSID.
func parseIPConfigSummary(out string) ipconfigSummary {
	var s ipconfigSummary
	if m := ssidLine.FindStringSubmatch(out); len(m) == 2 && !redacted(m[1]) {
		s.SSID = m[1]
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == "InterfaceType" && strings.EqualFold(strings.TrimSpace(v), "WiFi") {
			s.WiFi = true
		}
	}
	return s
}

// parseAirportNetwork extracts the SSID from `networksetup
// -getairportnetwork <if>` ("Current Wi-Fi Network: Foo"). "You are not
// associated with an AirPort network.", "<if> is not a Wi-Fi interface."
// and redacted values all yield "".
func parseAirportNetwork(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "not associated") || strings.Contains(lower, "not a wi-fi") || strings.Contains(lower, "error") {
			return ""
		}
		if _, v, ok := strings.Cut(line, "Network:"); ok {
			v = strings.TrimSpace(v)
			if redacted(v) {
				return ""
			}
			return v
		}
	}
	return ""
}

// isWiFiPort reports whether a hardware port name denotes Wi-Fi.
func isWiFiPort(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "wi-fi") || strings.Contains(n, "airport") || strings.Contains(n, "wlan")
}
