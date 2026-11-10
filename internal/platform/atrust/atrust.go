// Package atrust detects a running Sangfor aTrust client (or, more
// generally, a split-tunnel VPN running beside it) so Lanflow can keep the
// VPN's traffic out of its own TUN and rules.
//
// What it looks at (all read-only):
//   - processes whose name contains "atrust" or "sangfor";
//   - on macOS, the non-default routes through utun interfaces that are not
//     Lanflow's own TUN (`netstat -rn -f inet`), and the scoped resolvers
//     on those interfaces (`scutil --dns`) for the intranet domains and the
//     VPN's DNS servers.
//
// Windows only reports the processes for now.
package atrust

import (
	"context"
	"encoding/csv"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Info is one detection result. The zero value means "not running".
type Info struct {
	Processes []string // process names (base names, deduplicated, sorted)
	Ifaces    []string // VPN tunnel interfaces carrying the routes
	CIDRs     []string // destinations routed into the VPN (and its DNS servers as /32)
	Domains   []string // intranet domain suffixes served by the VPN's resolvers
	DNS       []string // the VPN's DNS servers
	Gateways  []string // remote addresses aTrust's processes are connected to (its tunnel servers)
}

// Running reports whether an aTrust process was found.
func (i Info) Running() bool { return len(i.Processes) > 0 }

// Key is a stable fingerprint of i, to tell whether anything changed.
func (i Info) Key() string {
	return strings.Join([]string{
		strings.Join(i.Processes, ","), strings.Join(i.Ifaces, ","),
		strings.Join(i.CIDRs, ","), strings.Join(i.Domains, ","), strings.Join(i.DNS, ","),
		strings.Join(i.Gateways, ","),
	}, "|")
}

// Detect looks for aTrust. own reports whether an interface is Lanflow's
// own TUN (so its routes are not mistaken for the VPN's); nil means none.
func Detect(ctx context.Context, own func(iface string) bool) Info {
	if own == nil {
		own = func(string) bool { return false }
	}
	return detect(ctx, own)
}

// IsATrustProcess reports whether a process name looks like part of aTrust.
func IsATrustProcess(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "atrust") || strings.Contains(n, "sangfor")
}

// matchProcesses picks the aTrust processes out of a list of process
// names or paths (one per line), returning sorted unique base names.
func matchProcesses(lines []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if i := strings.LastIndexAny(l, `/\`); i >= 0 {
			l = l[i+1:]
		}
		if !IsATrustProcess(l) || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// splitDefaults are the destinations TUN clients use to take over the
// default route (netstat notation); they are never treated as intranet.
var splitDefaults = map[string]bool{
	"default": true, "0/1": true, "128.0/1": true, "1": true, "2/7": true, "4/6": true,
	"8/5": true, "16/4": true, "32/3": true, "64/2": true,
}

// netstatCIDR turns a macOS netstat destination ("10.1", "2.0.0/24",
// "172.16/12", "2.0.0.5" with flag H) into a canonical CIDR.
func netstatCIDR(dst, flags string) (netip.Prefix, bool) {
	addr, bits, hasBits := strings.Cut(dst, "/")
	if strings.Contains(addr, "%") || strings.Contains(addr, ":") {
		return netip.Prefix{}, false
	}
	parts := strings.Split(addr, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return netip.Prefix{}, false
	}
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	ip, err := netip.ParseAddr(strings.Join(parts, "."))
	if err != nil || !ip.Is4() {
		return netip.Prefix{}, false
	}
	n := len(strings.Split(addr, ".")) * 8
	switch {
	case hasBits:
		var v int
		for _, c := range bits {
			if c < '0' || c > '9' {
				return netip.Prefix{}, false
			}
			v = v*10 + int(c-'0')
		}
		n = v
	case strings.Contains(flags, "H"):
		n = 32
	}
	if n < 0 || n > 32 {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(ip, n).Masked(), true
}

// skipPrefix filters destinations that are never intranet routes.
func skipPrefix(p netip.Prefix) bool {
	a := p.Addr()
	return p.Bits() == 0 || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() ||
		a == netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// route is one VPN route.
type route struct {
	prefix netip.Prefix
	iface  string
}

// fullTunnels finds the utun interfaces holding a non-scoped default or
// split-default route: another proxy's TUN taking all traffic, not a
// split-tunnel VPN, so its routes and resolvers are ignored.
func fullTunnels(out string) map[string]bool {
	full := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !strings.HasPrefix(f[3], "utun") || strings.Contains(f[2], "I") {
			continue
		}
		if splitDefaults[f[0]] {
			full[f[3]] = true
		}
	}
	return full
}

// parseRoutes collects the non-scoped, non-default routes through utun
// interfaces other than Lanflow's own (and other full tunnels) from
// `netstat -rn -f inet` output.
func parseRoutes(out string, own func(string) bool) []route {
	full := fullTunnels(out)
	var rs []route
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		dst, flags, netif := f[0], f[2], f[3]
		if !strings.HasPrefix(netif, "utun") || own(netif) || full[netif] || splitDefaults[dst] {
			continue
		}
		if strings.Contains(flags, "I") { // interface-scoped: only bound sockets use it
			continue
		}
		p, ok := netstatCIDR(dst, flags)
		if !ok || skipPrefix(p) {
			continue
		}
		rs = append(rs, route{p, netif})
	}
	return rs
}

// resolver is one `scutil --dns` resolver block.
type resolver struct {
	domain  string
	servers []string
	iface   string
}

// parseResolvers reads the resolver blocks of `scutil --dns`.
func parseResolvers(out string) []resolver {
	var rs []resolver
	var cur *resolver
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "resolver #"):
			rs = append(rs, resolver{})
			cur = &rs[len(rs)-1]
		case strings.HasPrefix(t, "DNS configuration (for scoped"):
			return rs // scoped resolvers only serve sockets bound to their interface
		case t == "" || strings.HasPrefix(t, "DNS configuration"):
		case cur == nil:
		default:
			k, v, ok := strings.Cut(t, ":")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch {
			case k == "domain":
				cur.domain = strings.TrimSuffix(strings.ToLower(v), ".")
			case strings.HasPrefix(k, "nameserver["):
				cur.servers = append(cur.servers, v)
			case k == "if_index":
				// "17 (utun5)"
				if i, j := strings.Index(v, "("), strings.Index(v, ")"); i >= 0 && j > i {
					cur.iface = v[i+1 : j]
				}
			}
		}
	}
	return rs
}

// build combines routes and resolvers into Info (processes filled by the caller).
func build(procs []string, routes []route, resolvers []resolver, own func(string) bool) Info {
	info := Info{Processes: procs}
	ifaces := map[string]bool{}
	cidrs := map[netip.Prefix]bool{}
	for _, r := range routes {
		ifaces[r.iface] = true
		cidrs[r.prefix] = true
	}
	domains := map[string]bool{}
	dns := map[string]bool{}
	for _, r := range resolvers {
		if !strings.HasPrefix(r.iface, "utun") || own(r.iface) {
			continue
		}
		if r.domain == "" || r.domain == "local" || strings.HasSuffix(r.domain, ".arpa") {
			continue
		}
		ifaces[r.iface] = true
		domains[r.domain] = true
		for _, s := range r.servers {
			a, err := netip.ParseAddr(s)
			if err != nil || !a.Is4() || a.IsLoopback() {
				continue
			}
			dns[a.String()] = true
			cidrs[netip.PrefixFrom(a, 32)] = true
		}
	}
	for i := range ifaces {
		info.Ifaces = append(info.Ifaces, i)
	}
	sort.Strings(info.Ifaces)
	ps := make([]netip.Prefix, 0, len(cidrs))
	for p := range cidrs {
		ps = append(ps, p)
	}
	ps = collapse(ps)
	for _, p := range ps {
		info.CIDRs = append(info.CIDRs, p.String())
	}
	for d := range domains {
		info.Domains = append(info.Domains, d)
	}
	sort.Strings(info.Domains)
	for d := range dns {
		info.DNS = append(info.DNS, d)
	}
	sort.Strings(info.DNS)
	return info
}

// collapse sorts prefixes and drops those contained in another.
func collapse(ps []netip.Prefix) []netip.Prefix {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Bits() != ps[j].Bits() {
			return ps[i].Bits() < ps[j].Bits()
		}
		return ps[i].Addr().Less(ps[j].Addr())
	})
	var out []netip.Prefix
	for _, p := range ps {
		if slices.ContainsFunc(out, func(q netip.Prefix) bool { return q.Contains(p.Addr()) && q.Bits() <= p.Bits() }) {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr().Less(out[j].Addr()) })
	return out
}

// tasklistNames reads the image names of `tasklist /fo csv /nh`, leaving
// out the process self.
func tasklistNames(out string, self int) []string {
	recs, _ := csv.NewReader(strings.NewReader(out)).ReadAll()
	names := make([]string, 0, len(recs))
	for _, r := range recs {
		if len(r) > 1 && r[1] == strconv.Itoa(self) {
			continue
		}
		if len(r) > 0 {
			names = append(names, r[0])
		}
	}
	return names
}

// withoutSelf turns `ps -axo pid=,comm=` lines into names, leaving out
// this process (a test binary or a build named after the package).
func withoutSelf(lines []string) []string {
	self := strconv.Itoa(os.Getpid())
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		pid, name, ok := strings.Cut(strings.TrimSpace(l), " ")
		if !ok || pid == self {
			continue
		}
		out = append(out, strings.TrimSpace(name))
	}
	return out
}

// parseGateways reads `netstat -anv` (macOS, tcp and udp) and returns the
// IPv4 remote addresses of connections owned by aTrust's processes: the
// tunnel servers it talks to. Loopback, link-local, private-use ranges
// mapped into the tunnel (inside cidrs) and the benchmark range used by
// TUN clients are left out.
func parseGateways(out string, cidrs []netip.Prefix) []string {
	seen := map[netip.Addr]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !(f[0] == "tcp4" || f[0] == "udp4") {
			continue
		}
		owner := ""
		for _, x := range f {
			if i := strings.LastIndex(x, ":"); i > 0 && isDigits(x[i+1:]) && !strings.Contains(x[:i], ".") {
				owner = x[:i]
				break
			}
		}
		if owner == "" || !IsATrustProcess(owner) {
			continue
		}
		remote := f[4]
		i := strings.LastIndex(remote, ".")
		if i <= 0 {
			continue
		}
		a, err := netip.ParseAddr(remote[:i])
		if err != nil || !a.Is4() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() || a.IsMulticast() {
			continue
		}
		if netip.MustParsePrefix("198.18.0.0/15").Contains(a) || slices.ContainsFunc(cidrs, func(p netip.Prefix) bool { return p.Contains(a) }) {
			continue
		}
		seen[a] = true
	}
	var outs []netip.Addr
	for a := range seen {
		outs = append(outs, a)
	}
	sort.Slice(outs, func(i, j int) bool { return outs[i].Less(outs[j]) })
	res := make([]string, len(outs))
	for i, a := range outs {
		res[i] = a.String()
	}
	return res
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
