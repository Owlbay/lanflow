package netinfo

import (
	"bytes"
	"context"
	"net"
	"os/exec"
	"strings"
	"time"
)

// getTimeout is the overall budget for Get; the contract requires < 3 s.
const getTimeout = 2500 * time.Millisecond

// runCommand runs an external command and returns its combined output.
// A variable so tests can substitute a fake.
var runCommand = execCommand

func execCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 200 * time.Millisecond
	prepareCommand(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// withBudget derives the context Get works under: the caller's deadline if
// it is sooner, otherwise getTimeout from now.
func withBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= getTimeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, getTimeout)
}

// lanAddress finds the IPv4 address the host would use to reach the
// Internet: it "dials" a UDP socket towards a TEST-NET address (nothing is
// sent) and reads the chosen local address. When that address is not a
// private LAN address (e.g. the default route goes through a TUN with a
// 198.18.x.x address) or the dial fails, the first private IPv4 of an up,
// non-loopback interface is used instead.
func lanAddress() string {
	var dialed string
	if conn, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		if a, ok := conn.LocalAddr().(*net.UDPAddr); ok && a.IP != nil && !a.IP.IsUnspecified() {
			dialed = a.IP.String()
			if a.IP.IsPrivate() {
				conn.Close()
				return dialed
			}
		}
		conn.Close()
	}
	if ip := firstPrivateIPv4(); ip != "" {
		return ip
	}
	return dialed
}

// firstPrivateIPv4 returns the first RFC 1918 IPv4 address of an up,
// non-loopback, non-tunnel interface, "" if none.
func firstPrivateIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || !ip4.IsPrivate() {
				continue
			}
			if isTunnelName(ifi.Name) {
				if fallback == "" {
					fallback = ip4.String()
				}
				continue
			}
			return ip4.String()
		}
	}
	return fallback
}

// isTunnelName reports whether an interface name looks like a VPN/TUN
// device rather than a physical LAN adapter.
func isTunnelName(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"utun", "tun", "tap", "ipsec", "ppp", "wg", "awdl", "llw", "bridge", "vmnet", "docker", "veth"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// isPublicIPv6 reports whether ip is a global unicast IPv6 address in
// 2000::/3 (excludes ULA fc00::/7, link-local and IPv4-mapped addresses).
func isPublicIPv6(ip net.IP) bool {
	if ip == nil || ip.To4() != nil || len(ip) != net.IPv6len {
		return false
	}
	return ip[0]&0xE0 == 0x20
}

// interfaceAddrs returns the first IPv4 and the first public IPv6 address
// of the named interface ("" when absent).
func interfaceAddrs(name string) (ipv4, ipv6 string) {
	if name == "" {
		return "", ""
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return "", ""
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return "", ""
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if v4 := ipn.IP.To4(); v4 != nil {
			if ipv4 == "" {
				ipv4 = v4.String()
			}
		} else if ipv6 == "" && isPublicIPv6(ipn.IP) {
			ipv6 = ipn.IP.String()
		}
	}
	return ipv4, ipv6
}

// dedup removes duplicates and blanks while preserving order.
func dedup(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// redacted reports whether a value is macOS' placeholder for data the app
// is not allowed to see (no Location Services permission).
func redacted(v string) bool {
	return strings.EqualFold(strings.TrimSpace(v), "<redacted>")
}
