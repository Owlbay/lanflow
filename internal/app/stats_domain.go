package app

import (
	"net"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// statsHost strips the port of a connection's Host ("www.a.com:443" →
// "www.a.com", "[::1]:53" → "::1").
func statsHost(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]"))
}

// registrableDomain returns the eTLD+1 of host ("rr3.sn.googlevideo.com"
// → "googlevideo.com"). An IP address, a single label and anything the
// public suffix list cannot place come back unchanged.
func registrableDomain(host string) string {
	host = strings.TrimSuffix(host, ".")
	if host == "" || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return host
	}
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	// Unknown suffix (a private TLD, "*.local"…): keep the last two labels.
	labels := strings.Split(host, ".")
	if len(labels) > 2 {
		return strings.Join(labels[len(labels)-2:], ".")
	}
	return host
}
