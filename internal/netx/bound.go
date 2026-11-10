package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"lanflow/internal/platform/netinfo"
)

// A client bound to the physical interface. netx.Direct only ignores the
// proxy settings: with a TUN (Lanflow's own or another client's) holding
// the default route its connections are captured and proxied like any
// other traffic. The bound client sets IP_BOUND_IF (macOS) or
// IP_UNICAST_IF (Windows) on every socket, its DNS queries included, so
// what it measures is the machine's own public endpoint. It never falls
// back to an unbound connection: a failed bind fails the request.

// ErrBind wraps a failure to bind a socket to the physical interface.
var ErrBind = errors.New("cannot bind to the physical interface")

// PublicDNS are the resolvers queried over the physical interface after
// the interface's own DNS servers and its gateway.
var PublicDNS = []netip.Addr{
	netip.MustParseAddr("223.5.5.5"),
	netip.MustParseAddr("119.29.29.29"),
	netip.MustParseAddr("1.1.1.1"),
	netip.MustParseAddr("8.8.8.8"),
}

// dnsTimeout bounds one resolver's answer.
const dnsTimeout = 2 * time.Second

// bindSocket sets the bind option on fd; a variable so tests can make it fail.
var bindSocket = bindToInterface

// fakeIPRange is Mihomo's default fake-ip range: an answer in it means the
// query was hijacked by a TUN after all.
var fakeIPRange = netip.MustParsePrefix("198.18.0.0/15")

// PhysicalDirect finds the physical interface carrying the default route
// (netinfo.PhysicalInterface) and returns a BoundDirect client for it. It
// fails, rather than return an unbound client, when there is none.
func PhysicalDirect(ctx context.Context, timeout time.Duration) (*http.Client, netinfo.Physical, error) {
	ifc, err := netinfo.PhysicalInterface(ctx)
	if err != nil {
		return nil, netinfo.Physical{}, err
	}
	return BoundDirect(ifc, timeout), ifc, nil
}

// BoundDirect returns a client like Direct whose connections and DNS
// lookups are all bound to ifc. Each request opens a fresh connection.
func BoundDirect(ifc netinfo.Physical, timeout time.Duration) *http.Client {
	b := newBound(ifc, nil)
	t := transport(nil)
	t.DialContext = b.dial
	t.DisableKeepAlives = true
	return &http.Client{Timeout: timeout, Transport: t}
}

// bound dials through one interface.
type bound struct {
	index   int
	dialer  *net.Dialer
	servers []netip.AddrPort // resolvers, in order
}

// newBound builds the dialer for ifc; servers nil means dnsServers(ifc).
func newBound(ifc netinfo.Physical, servers []netip.AddrPort) *bound {
	b := &bound{index: ifc.Index, servers: servers}
	if b.servers == nil {
		b.servers = dnsServers(ifc)
	}
	b.dialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: b.control}
	return b
}

func (b *bound) control(network, address string, c syscall.RawConn) error {
	if b.index <= 0 {
		return fmt.Errorf("%w: no interface index", ErrBind)
	}
	var err error
	if cerr := c.Control(func(fd uintptr) { err = bindSocket(network, address, fd, b.index) }); cerr != nil {
		return fmt.Errorf("%w: %v", ErrBind, cerr)
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBind, err)
	}
	return nil
}

// dial resolves addr over the interface (unless it is an IP literal) and
// connects to its addresses in turn, IPv4 first.
func (b *bound) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = b.lookup(ctx, host); err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		c, err := b.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
		if errors.Is(err, ErrBind) || ctx.Err() != nil {
			break
		}
	}
	return nil, last
}

// lookup asks each resolver in turn, over the interface, and returns the
// first usable answer (fake-ip, loopback and unspecified addresses are not).
func (b *bound) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if len(b.servers) == 0 {
		return nil, fmt.Errorf("resolve %s: no DNS server", host)
	}
	var last error
	for _, srv := range b.servers {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return b.dialer.DialContext(ctx, network, srv.String())
		}}
		qctx, cancel := context.WithTimeout(ctx, dnsTimeout)
		ans, err := r.LookupNetIP(qctx, "ip", host)
		cancel()
		if err != nil {
			last = err
			if errors.Is(err, ErrBind) {
				break
			}
			continue
		}
		var v4, v6 []netip.Addr
		for _, a := range ans {
			a = a.Unmap()
			if !usableAnswer(a) {
				continue
			}
			if a.Is4() {
				v4 = append(v4, a)
			} else {
				v6 = append(v6, a)
			}
		}
		if len(v4)+len(v6) > 0 {
			return append(v4, v6...), nil
		}
		last = fmt.Errorf("resolver %s answered %v for %s (fake-ip or unusable)", srv, ans, host)
	}
	return nil, fmt.Errorf("resolve %s over the physical interface: %w", host, last)
}

func usableAnswer(a netip.Addr) bool {
	return a.IsValid() && !a.IsUnspecified() && !a.IsLoopback() && !fakeIPRange.Contains(a)
}

// usableResolver reports whether a configured DNS server can be reached
// over the physical interface: a TUN's own resolver (fake-ip range,
// loopback) or a link-local one cannot.
func usableResolver(a netip.Addr) bool {
	return a.IsValid() && !a.IsUnspecified() && !a.IsLoopback() && !a.IsLinkLocalUnicast() &&
		!a.IsMulticast() && !fakeIPRange.Contains(a)
}

// dnsServers lists ifc's resolvers, its gateway and PublicDNS, deduplicated.
func dnsServers(ifc netinfo.Physical) []netip.AddrPort {
	var out []netip.AddrPort
	seen := map[netip.Addr]bool{}
	add := func(a netip.Addr) {
		a = a.Unmap()
		if !usableResolver(a) || seen[a] {
			return
		}
		seen[a] = true
		out = append(out, netip.AddrPortFrom(a, 53))
	}
	for _, a := range ifc.DNS {
		add(a)
	}
	add(ifc.Gateway)
	for _, a := range PublicDNS {
		add(a)
	}
	return out
}
