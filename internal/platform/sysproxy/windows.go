package sysproxy

// Windows registry value formatting and parsing. Pure functions without a
// build tag so they are unit tested on every host; sysproxy_windows.go
// does the registry and WinINet calls.

import (
	"net"
	"strconv"
	"strings"
)

// formatProxyServer renders the WinINet ProxyServer value with explicit
// per-protocol entries so HTTP, HTTPS and SOCKS all point at host:port.
func formatProxyServer(host string, port int) string {
	hp := net.JoinHostPort(host, strconv.Itoa(port))
	return "http=" + hp + ";https=" + hp + ";socks=" + hp
}

// parseProxyServer extracts a host and port from a ProxyServer value,
// which is either a bare "host:port" or "proto=host:port;..." entries.
// Protocol preference: http, https, socks, then anything else.
func parseProxyServer(v string) (host string, port int) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", 0
	}
	byProto := map[string]string{}
	var bare string
	for _, part := range strings.Split(v, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if proto, hp, ok := strings.Cut(part, "="); ok {
			byProto[strings.ToLower(strings.TrimSpace(proto))] = strings.TrimSpace(hp)
		} else if bare == "" {
			bare = part
		}
	}
	candidates := []string{byProto["http"], byProto["https"], byProto["socks"], bare, byProto["ftp"]}
	for _, hp := range candidates {
		if hp == "" {
			continue
		}
		h, p, err := net.SplitHostPort(hp)
		if err != nil {
			// A value without a port ("proxy.corp") is still a host.
			return hp, 0
		}
		port, _ = strconv.Atoi(p)
		return h, port
	}
	return "", 0
}

// formatProxyOverride joins the bypass list with ";" and guarantees the
// WinINet "<local>" token (bypass for plain host names) is present.
func formatProxyOverride(bypass []string) string {
	seen := map[string]struct{}{}
	var parts []string
	hasLocal := false
	for _, b := range bypass {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		key := strings.ToLower(b)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if key == "<local>" {
			hasLocal = true
		}
		parts = append(parts, b)
	}
	if !hasLocal {
		parts = append(parts, "<local>")
	}
	return strings.Join(parts, ";")
}

// parseProxyOverride splits a ProxyOverride value into its entries.
func parseProxyOverride(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ";") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
