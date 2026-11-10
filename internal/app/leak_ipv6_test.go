package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/platform/netinfo"
)

// Under TUN the IPv6 routing table tells a bypass from "no IPv6": the
// VM-style LAN (a ULA default route on en0, nothing through the TUN) must
// fail even though the forced-IPv6 request gets no answer.
func TestIPv6LeakRouteJudgements(t *testing.T) {
	noRoute := errors.New("dial tcp6: i/o timeout")
	cases := []struct {
		name    string
		p       ipv6Probe
		want    string
		summary string
	}{
		{"tun, v6 default on en0, tun has no v6 route", ipv6Probe{tun: true, direct6Err: noRoute, routeIface: "en0"}, model.LeakFail, "en0"},
		{"tun owns ::/0, proxy has no v6", ipv6Probe{tun: true, direct6Err: noRoute, routeIface: "en0", routeViaTUN: true}, model.LeakOK, "TUN"},
		{"tun owns ::/0, request left from the node's v6 exit", ipv6Probe{tun: true, direct6: "2606:4700::9", via: "203.0.113.9", routeIface: "en0", routeViaTUN: true, localV6: "2001:db8::1"}, model.LeakOK, "2606:4700::9"},
		{"tun owns ::/0 but the request still left from our address", ipv6Probe{tun: true, direct6: "2001:db8::1", via: "203.0.113.9", routeIface: "en0", routeViaTUN: true, localV6: "2001:db8::1"}, model.LeakFail, "IPv6"},
		{"tun, same exit over v6 and v4", ipv6Probe{tun: true, direct6: "203.0.113.9", via: "203.0.113.9"}, model.LeakOK, "IPv6"},
		{"no tun, no v6 at all", ipv6Probe{sysProxy: true, direct6Err: noRoute}, model.LeakOK, "IPv6"},
		{"tun, no v6 at all (no default route)", ipv6Probe{tun: true, direct6Err: noRoute}, model.LeakOK, "IPv6"},
	}
	for _, c := range cases {
		got := ipv6LeakItem(c.p)
		if got.Status != c.want || !strings.Contains(got.Summary, c.summary) || got.Detail == "" {
			t.Errorf("%s: status %s (want %s) summary %q detail %q", c.name, got.Status, c.want, got.Summary, got.Detail)
		}
		if got.Status == model.LeakFail && !strings.Contains(got.Detail, "inet6-address") {
			t.Errorf("%s: a FAIL must say what to check: %q", c.name, got.Detail)
		}
	}
}

// The service feeds the route reader's answer into the IPv6 item.
func TestLeakCheckUsesIPv6Route(t *testing.T) {
	h := newHarness(t, nil)
	h.s.d.traceURL = "https://trace.invalid/cdn-cgi/trace"
	h.s.d.httpDirect6 = func(time.Duration) *http.Client { return failingClient() }
	h.s.d.httpVia = func(int, time.Duration) *http.Client { return failingClient() }
	h.s.d.appDNS = func(context.Context, string, string, string) ([]string, error) { return []string{"198.18.0.7"}, nil }
	h.s.d.stun = func(context.Context) (string, error) { return "", errors.New("no answer") }
	h.s.d.v6Route = func(context.Context) netinfo.V6Route { return netinfo.V6Route{Interface: "en0"} }
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	h.s.state.st.TUN = true
	h.s.mu.Unlock()
	if err := h.s.RunLeakCheck(); err != nil {
		t.Fatal(err)
	}
	if v6 := itemByName(h.s.LeakCheck().Items, "IPv6"); v6.Status != model.LeakFail || !strings.Contains(v6.Summary, "en0") {
		t.Fatalf("IPv6 item = %+v", v6)
	}
	h.s.d.v6Route = func(context.Context) netinfo.V6Route { return netinfo.V6Route{Interface: "en0", ViaTUN: true} }
	if err := h.s.RunLeakCheck(); err != nil {
		t.Fatal(err)
	}
	if v6 := itemByName(h.s.LeakCheck().Items, "IPv6"); v6.Status != model.LeakOK {
		t.Fatalf("IPv6 item with the TUN owning ::/0 = %+v", v6)
	}
}
