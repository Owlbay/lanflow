package netx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"lanflow/internal/platform/netinfo"
)

// stubBind replaces bindSocket for the test.
func stubBind(t *testing.T, f func(network, address string, fd uintptr, index int) error) {
	t.Helper()
	old := bindSocket
	bindSocket = f
	t.Cleanup(func() { bindSocket = old })
}

// counting is an HTTP server that counts the requests it saw.
func counting(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		io.WriteString(w, "ip=203.0.113.9\n")
	}))
	t.Cleanup(ts.Close)
	return ts, &n
}

// A failed bind fails the request: it is never retried unbound.
func TestBoundDirectNoFallbackOnBindFailure(t *testing.T) {
	var calls atomic.Int32
	stubBind(t, func(string, string, uintptr, int) error {
		calls.Add(1)
		return errors.New("EPERM")
	})
	ts, hits := counting(t)
	_, err := BoundDirect(netinfo.Physical{Name: "en0", Index: 4}, 3*time.Second).Get(ts.URL)
	if err == nil || !errors.Is(err, ErrBind) {
		t.Fatalf("err = %v, want ErrBind", err)
	}
	if calls.Load() == 0 {
		t.Fatal("bind was never attempted")
	}
	if hits.Load() != 0 {
		t.Fatalf("server saw %d requests; an unbound fallback leaked through", hits.Load())
	}
}

func TestBoundDirectNoIndex(t *testing.T) {
	stubBind(t, func(string, string, uintptr, int) error { return nil })
	ts, hits := counting(t)
	if _, err := BoundDirect(netinfo.Physical{}, 3*time.Second).Get(ts.URL); !errors.Is(err, ErrBind) {
		t.Fatalf("err = %v, want ErrBind", err)
	}
	if hits.Load() != 0 {
		t.Fatal("request went out without a bound interface")
	}
}

// The bind option is set with the right family and index.
func TestBoundDirectBindsEverySocket(t *testing.T) {
	var got atomic.Value
	stubBind(t, func(network, _ string, _ uintptr, index int) error {
		got.Store([2]any{network, index})
		return nil
	})
	ts, hits := counting(t)
	resp, err := BoundDirect(netinfo.Physical{Index: 7}, 3*time.Second).Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hits.Load() != 1 || got.Load() != [2]any{"tcp4", 7} {
		t.Fatalf("hits %d, bind %v", hits.Load(), got.Load())
	}
}

// fakeDNS answers every A query with ip on a local UDP port.
func fakeDNS(t *testing.T, ip string) netip.AddrPort {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	a := netip.MustParseAddr(ip).As4()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) != nil || len(m.Questions) == 0 {
				continue
			}
			q := m.Questions[0]
			m.Header.Response, m.Header.RecursionAvailable = true, true
			m.Answers = nil
			if q.Type == dnsmessage.TypeA {
				m.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: a},
				}}
			}
			out, _ := m.Pack()
			pc.WriteTo(out, from)
		}
	}()
	return netip.MustParseAddrPort(pc.LocalAddr().String())
}

// A fake-ip answer (the query was hijacked) is skipped for the next resolver.
func TestBoundLookupSkipsFakeIP(t *testing.T) {
	var binds atomic.Int32
	stubBind(t, func(string, string, uintptr, int) error { binds.Add(1); return nil })
	fake := fakeDNS(t, "198.18.0.42")
	real := fakeDNS(t, "203.0.113.7")

	b := newBound(netinfo.Physical{Index: 3}, []netip.AddrPort{fake})
	if ips, err := b.lookup(context.Background(), "trace.example.test"); err == nil {
		t.Fatalf("lookup accepted fake-ip answer %v", ips)
	}
	b = newBound(netinfo.Physical{Index: 3}, []netip.AddrPort{fake, real})
	ips, err := b.lookup(context.Background(), "trace.example.test")
	if err != nil || len(ips) != 1 || ips[0].String() != "203.0.113.7" {
		t.Fatalf("lookup = %v, %v", ips, err)
	}
	if binds.Load() == 0 {
		t.Fatal("DNS sockets were not bound")
	}
}

func TestDNSServers(t *testing.T) {
	ifc := netinfo.Physical{
		Gateway: netip.MustParseAddr("192.168.0.1"),
		DNS: []netip.Addr{
			netip.MustParseAddr("198.18.0.2"), // TUN resolver
			netip.MustParseAddr("127.0.0.1"),
			netip.MustParseAddr("fe80::1"),
			netip.MustParseAddr("192.168.0.1"),
			netip.MustParseAddr("223.5.5.5"),
		},
	}
	var got []string
	for _, s := range dnsServers(ifc) {
		got = append(got, s.String())
	}
	want := "192.168.0.1:53 223.5.5.5:53 119.29.29.29:53 1.1.1.1:53 8.8.8.8:53"
	if strings.Join(got, " ") != want {
		t.Fatalf("dnsServers = %v\nwant %s", got, want)
	}
}

// Binding for real to the loopback interface still reaches 127.0.0.1.
func TestBoundDirectLoopbackDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("IP_BOUND_IF to lo0 is checked on macOS only")
	}
	lo, err := net.InterfaceByName("lo0")
	if err != nil {
		t.Skip(err)
	}
	ts, hits := counting(t)
	resp, err := BoundDirect(netinfo.Physical{Name: "lo0", Index: lo.Index}, 3*time.Second).Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatal("no request reached the server")
	}
}

// TestLivePhysicalTrace fetches the Cloudflare trace bound to the physical
// interface and unbound. With a TUN capturing the default route the two
// IPs differ (bound = this machine's own, unbound = the proxy's exit).
// It needs the Internet, so it runs only with LANFLOW_LIVE_NET=1:
//
//	LANFLOW_LIVE_NET=1 go test ./internal/netx -run LivePhysicalTrace -v
func TestLivePhysicalTrace(t *testing.T) {
	if os.Getenv("LANFLOW_LIVE_NET") != "1" {
		t.Skip("set LANFLOW_LIVE_NET=1 to run against the real network")
	}
	const url = "https://www.cloudflare.com/cdn-cgi/trace"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bc, ifc, err := PhysicalDirect(ctx, 10*time.Second)
	if err != nil {
		t.Skipf("no physical interface: %v", err)
	}
	bound, err := traceIP(bc, url)
	if err != nil {
		t.Fatalf("bound to %s (#%d): %v", ifc.Name, ifc.Index, err)
	}
	plain, err := traceIP(Direct(10*time.Second), url)
	if err != nil {
		t.Logf("unbound request failed: %v", err)
	}
	tun, _ := netinfo.OtherTUN(ctx)
	t.Logf("interface %s (#%d); bound trace ip=%s; unbound ip=%s; TUN holding default route: %q", ifc.Name, ifc.Index, bound, plain, tun)
	if tun != "" && plain != "" && plain == bound {
		t.Errorf("a TUN (%s) holds the default route, yet the bound request left from the same IP as the unbound one", tun)
	}
}

func traceIP(c *http.Client, url string) (string, error) {
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "ip="); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("no ip in trace")
}
