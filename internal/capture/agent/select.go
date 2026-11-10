package agent

import (
	"net/netip"
	"strings"
)

// matcher is a set of host patterns in the syntax of
// model.CaptureSettings.Domains / Bypass, applied by the agent itself:
// Mihomo's rules are only the first filter (docs/capture.md §2).
//
//	example.com     exactly that name                      (DOMAIN)
//	*.example.com   any name matching the wildcard         (DOMAIN-WILDCARD; * may span dots)
//	+.example.com   the name and every subdomain           (DOMAIN-SUFFIX)
//	ocsp.*          wildcard anywhere                      (DOMAIN-WILDCARD)
//	10.0.0.0/8      an IP destination in the range         (IP-CIDR)
type matcher struct {
	exact    map[string]string
	suffix   []string // without "+."
	wild     []string
	prefixes []netip.Prefix
	orig     map[string]string // normalised pattern -> as written
}

func newMatcher(patterns []string) *matcher {
	m := &matcher{exact: map[string]string{}, orig: map[string]string{}}
	for _, p := range patterns {
		raw := strings.TrimSpace(p)
		q := strings.ToLower(strings.TrimSuffix(raw, "."))
		if q == "" {
			continue
		}
		if pf, err := netip.ParsePrefix(q); err == nil {
			m.prefixes = append(m.prefixes, pf.Masked())
			continue
		}
		switch {
		case strings.HasPrefix(q, "+."):
			m.suffix = append(m.suffix, q[2:])
			m.orig["+"+q[2:]] = raw
		case strings.Contains(q, "*"):
			m.wild = append(m.wild, q)
			m.orig[q] = raw
		default:
			m.exact[q] = raw
		}
	}
	return m
}

func (m *matcher) empty() bool {
	return len(m.exact) == 0 && len(m.suffix) == 0 && len(m.wild) == 0 && len(m.prefixes) == 0
}

// match returns the pattern (as written) that host matches, "" if none.
func (m *matcher) match(host string) string {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" {
		return ""
	}
	if a, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
		a = a.Unmap()
		for _, p := range m.prefixes {
			if p.Contains(a) {
				return p.String()
			}
		}
		return ""
	}
	if r, ok := m.exact[h]; ok {
		return r
	}
	for _, s := range m.suffix {
		if h == s || strings.HasSuffix(h, "."+s) {
			return m.orig["+"+s]
		}
	}
	for _, w := range m.wild {
		if wildMatch(w, h) {
			return m.orig[w]
		}
	}
	return ""
}

// wildMatch matches s against pattern p where '*' is any run of
// characters (dots included) and '?' one character, as Mihomo's
// DOMAIN-WILDCARD does.
func wildMatch(p, s string) bool {
	px, sx := 0, 0
	star, mark := -1, 0
	for sx < len(s) {
		switch {
		case px < len(p) && (p[px] == '?' || p[px] == s[sx]):
			px++
			sx++
		case px < len(p) && p[px] == '*':
			star, mark = px, sx
			px++
		case star >= 0:
			px = star + 1
			mark++
			sx = mark
		default:
			return false
		}
	}
	for px < len(p) && p[px] == '*' {
		px++
	}
	return px == len(p)
}

// singleLabel reports a bare intranet name ("nas", "printer"): never
// decrypted (docs/capture.md §4.2).
func singleLabel(host string) bool {
	h := strings.TrimSuffix(host, ".")
	if _, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
		return false
	}
	return h != "" && !strings.Contains(h, ".")
}

func isIP(host string) bool {
	_, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil
}
