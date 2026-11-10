package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/netx"
)

// traceServer answers like Cloudflare's /cdn-cgi/trace for the given ip.
func traceServer(t *testing.T, ip string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("fl=1\nip=" + ip + "\nloc=HK\ncolo=HKG\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// rewriteRT sends every request to target instead of the URL's host, so a
// client stands for one network path (direct over IPv6, via the proxy).
type rewriteRT struct{ target string }

func (r rewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(r.target)
	if err != nil {
		return nil, err
	}
	req2 := req.Clone(req.Context())
	req2.URL.Scheme, req2.URL.Host, req2.Host = u.Scheme, u.Host, u.Host
	return http.DefaultTransport.RoundTrip(req2)
}

func clientTo(srv *httptest.Server) *http.Client {
	return &http.Client{Transport: rewriteRT{target: srv.URL}}
}

func failingClient() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp6: no route to host")
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func itemByName(items []model.LeakItem, name string) model.LeakItem {
	for _, it := range items {
		if it.Name == name {
			return it
		}
	}
	return model.LeakItem{}
}

// The leak check needs the running core, runs both probes and publishes
// the result; with TUN on, Fake-IP answers from the system resolver and
// an IPv6 request that leaves through the proxy exit both pass.
func TestLeakCheckTUNAllClear(t *testing.T) {
	exit := traceServer(t, "2606:4700::1")
	h := newHarness(t, nil)
	h.s.d.traceURL = "https://trace.invalid/cdn-cgi/trace"
	h.s.d.httpDirect6 = func(time.Duration) *http.Client { return clientTo(exit) }
	h.s.d.httpVia = func(int, time.Duration) *http.Client { return clientTo(exit) }
	h.s.d.appDNS = func(_ context.Context, server, name, qtype string) ([]string, error) {
		switch server {
		case "":
			return []string{"198.18.0.7"}, nil
		case leakPublicDNS:
			return []string{"104.16.124.96"}, nil
		}
		t.Errorf("unexpected resolver %q", server)
		return nil, errors.New("unexpected")
	}
	h.s.d.stun = func(context.Context) (string, error) { return "2606:4700::1", nil }
	if err := h.s.RunLeakCheck(); err == nil {
		t.Fatal("leak check must need the running core")
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	h.s.state.st.TUN = true
	h.s.state.net.DNS = []string{"223.5.5.5"}
	h.s.state.runtimeYAML = []byte("dns:\n  enhanced-mode: fake-ip\n  fake-ip-range: 198.18.0.1/16\n")
	h.s.mu.Unlock()

	if err := h.s.RunLeakCheck(); err != nil {
		t.Fatal(err)
	}
	lk := h.s.LeakCheck()
	if lk.Checking || lk.CheckedAt.IsZero() || len(lk.Items) != 5 {
		t.Fatalf("result = %+v", lk)
	}
	if dns := itemByName(lk.Items, "DNS"); dns.Status != model.LeakOK || !strings.Contains(dns.Detail, "Fake-IP") {
		t.Fatalf("DNS item = %+v", dns)
	}
	if v6 := itemByName(lk.Items, "IPv6"); v6.Status != model.LeakOK || !strings.Contains(v6.Detail, "2606:4700::1") {
		t.Fatalf("IPv6 item = %+v", v6)
	}
	// The STUN-mapped address is the proxied exit: UDP goes through TUN.
	if rtc := itemByName(lk.Items, model.LeakItemWebRTC); rtc.Status != model.LeakOK || !strings.Contains(rtc.Summary, "2606:4700::1") || len(rtc.Rows) != 3 {
		t.Fatalf("WebRTC item = %+v", rtc)
	}
	// Fake-IP answers come from the core: nothing to compare, all clear.
	if cmp := itemByName(lk.Items, model.LeakItemDNSCmp); cmp.Status != model.LeakOK || len(cmp.Rows) != 3 || cmp.Rows[2].Value != "104.16.124.96" || cmp.Title == "" {
		t.Fatalf("DNS 对比 item = %+v", cmp)
	}
	// IP 类型 is informational: exit and (when known) the real endpoint.
	if typ := itemByName(lk.Items, model.LeakItemIPType); typ.Status != model.LeakOK || !strings.Contains(typ.Summary, "2606:4700::1") || len(typ.Rows) != 2 {
		t.Fatalf("IP 类型 item = %+v", typ)
	}
}

// With TUN on, real addresses from the system resolver in fake-ip mode
// and an IPv6 request that leaves from the machine's own address are
// both reported as leaks, with the reason and what to do.
func TestLeakCheckTUNLeaks(t *testing.T) {
	own := traceServer(t, "2001:db8::10")
	exit := traceServer(t, "2606:4700::1")
	h := newHarness(t, nil)
	h.s.d.traceURL = "https://trace.invalid/cdn-cgi/trace"
	h.s.d.httpDirect6 = func(time.Duration) *http.Client { return clientTo(own) }
	h.s.d.httpVia = func(int, time.Duration) *http.Client { return clientTo(exit) }
	h.s.d.appDNS = func(_ context.Context, server, name, _ string) ([]string, error) {
		if name == "example.com" {
			return []string{"93.184.215.14"}, nil
		}
		if server == leakPublicDNS {
			return []string{"104.16.124.96"}, nil // the public resolver disagrees with the system one
		}
		return []string{"104.16.123.96"}, nil
	}
	h.s.d.stun = func(context.Context) (string, error) { return "1.2.3.4", nil } // fakeLoc.Direct's address
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool { return h.s.You() != nil }) {
		t.Fatal("You() not fetched")
	}
	h.s.mu.Lock()
	h.s.state.st.TUN = true
	h.s.state.net.DNS = []string{"192.168.1.1"}
	h.s.state.net.IPv6 = "2001:db8::10"
	h.s.state.runtimeYAML = []byte("dns:\n  enhanced-mode: fake-ip\n")
	h.s.mu.Unlock()
	if err := h.s.RunLeakCheckWith("https://Example.com/path"); err != nil {
		t.Fatal(err)
	}
	lk := h.s.LeakCheck()
	dns := itemByName(lk.Items, "DNS")
	if dns.Status != model.LeakFail || !strings.Contains(dns.Detail, "192.168.1.1") || !strings.Contains(dns.Detail, "自动设置系统 DNS") {
		t.Fatalf("DNS item = %+v", dns)
	}
	v6 := itemByName(lk.Items, "IPv6")
	if v6.Status != model.LeakFail || !strings.Contains(v6.Detail, "2001:db8::10") {
		t.Fatalf("IPv6 item = %+v", v6)
	}
	// UDP left from the real address although TUN is on.
	rtc := itemByName(lk.Items, model.LeakItemWebRTC)
	if rtc.Status != model.LeakFail || !strings.Contains(rtc.Summary, "1.2.3.4") || !strings.Contains(rtc.Detail, "udp: false") {
		t.Fatalf("WebRTC item = %+v", rtc)
	}
	// The test host's system answer matches neither the core (fake ctrl
	// answers nothing) nor the public resolver: flagged; the user's
	// domain, cleaned to its host, agrees everywhere and is listed too.
	cmp := itemByName(lk.Items, model.LeakItemDNSCmp)
	if cmp.Status != model.LeakWarn || !strings.Contains(cmp.Summary, leakTestHost) || strings.Contains(cmp.Summary, "example.com") || len(cmp.Rows) != 6 {
		t.Fatalf("DNS 对比 item = %+v", cmp)
	}
	if !strings.HasPrefix(cmp.Rows[4].Value, "example.com：93.184.215.14") || !strings.Contains(cmp.Rows[1].Value, "104.16.123.96") {
		t.Fatalf("DNS 对比 rows = %+v", cmp.Rows)
	}
}

// System-proxy mode without TUN: only apps honoring the proxy are
// covered, so both items are partial; no IPv6 route means nothing to leak.
func TestLeakCheckSystemProxyPartial(t *testing.T) {
	h := newHarness(t, nil)
	h.s.d.traceURL = "https://trace.invalid/cdn-cgi/trace"
	h.s.d.httpDirect6 = func(time.Duration) *http.Client { return failingClient() }
	exit := traceServer(t, "203.0.113.9")
	h.s.d.httpVia = func(int, time.Duration) *http.Client { return clientTo(exit) }
	h.s.d.appDNS = func(context.Context, string, string, string) ([]string, error) {
		return []string{"104.16.123.96"}, nil
	}
	h.s.d.stun = func(context.Context) (string, error) { return "", netx.ErrSTUNUnreachable }
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	h.s.state.net.DNS = []string{"192.168.1.1"}
	h.s.mu.Unlock()
	if err := h.s.RunLeakCheck(); err != nil {
		t.Fatal(err)
	}
	lk := h.s.LeakCheck()
	if dns := itemByName(lk.Items, "DNS"); dns.Status != model.LeakWarn || !strings.Contains(dns.Detail, "TUN") {
		t.Fatalf("DNS item = %+v", dns)
	}
	if v6 := itemByName(lk.Items, "IPv6"); v6.Status != model.LeakOK {
		t.Fatalf("IPv6 item = %+v", v6)
	}
	// No STUN answer: UDP is blocked, so WebRTC cannot reveal anything.
	if rtc := itemByName(lk.Items, model.LeakItemWebRTC); rtc.Status != model.LeakOK || !strings.Contains(rtc.Summary, "STUN") {
		t.Fatalf("WebRTC item = %+v", rtc)
	}
	// The exit came from the proxied trace; the fake locator knows no ASN.
	if typ := itemByName(lk.Items, model.LeakItemIPType); typ.Status != model.LeakOK || !strings.Contains(typ.Summary, "203.0.113.9") || !strings.Contains(typ.Summary, "类型未知") {
		t.Fatalf("IP 类型 item = %+v", typ)
	}

	// Direct mode: nothing to check.
	if err := h.s.SetMode(model.ModeDirect); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunLeakCheck(); err != nil {
		t.Fatal(err)
	}
	if items := h.s.LeakCheck().Items; len(items) != 5 {
		t.Fatalf("direct mode items = %d", len(items))
	}
	for _, it := range h.s.LeakCheck().Items {
		if it.Status != model.LeakSkipped {
			t.Fatalf("direct mode item = %+v", it)
		}
	}
}

// The WebRTC judgement table.
func TestWebRTCJudgements(t *testing.T) {
	cases := []struct {
		name string
		p    webrtcProbe
		want string
	}{
		{"udp blocked", webrtcProbe{sysProxy: true, mappedErr: errors.New("timeout")}, model.LeakOK},
		{"mapped is the exit", webrtcProbe{tun: true, mapped: "203.0.113.9", exit: "203.0.113.9", direct: "1.2.3.4"}, model.LeakOK},
		{"real ip under tun", webrtcProbe{tun: true, mapped: "1.2.3.4", exit: "203.0.113.9", direct: "1.2.3.4"}, model.LeakFail},
		{"real ip under system proxy", webrtcProbe{sysProxy: true, mapped: "1.2.3.4", exit: "203.0.113.9", direct: "1.2.3.4"}, model.LeakWarn},
		{"nothing to compare with", webrtcProbe{sysProxy: true, mapped: "1.2.3.4"}, model.LeakWarn},
		{"some third address", webrtcProbe{tun: true, mapped: "9.9.9.9", exit: "203.0.113.9", direct: "1.2.3.4"}, model.LeakWarn},
		{"own ipv6 under system proxy", webrtcProbe{sysProxy: true, mapped: "2001:db8::10", exit: "203.0.113.9", direct: "1.2.3.4", local6: "2001:db8::10"}, model.LeakWarn},
	}
	for _, c := range cases {
		got := webrtcLeakItem(c.p)
		if got.Status != c.want || got.Summary == "" || got.Detail == "" || len(got.Rows) != 3 {
			t.Errorf("%s: %+v", c.name, got)
		}
		if c.want != model.LeakOK && c.p.exit != "" && !strings.Contains(got.Detail, "TUN") {
			t.Errorf("%s: detail must say what to do: %q", c.name, got.Detail)
		}
	}
}

// The DNS comparison judgement table.
func TestDNSCompareJudgements(t *testing.T) {
	agree := dnsAnswers{name: "a.example", core: []string{"1.1.1.1"}, sys: []string{"1.1.1.1"}, pub: []string{"2.2.2.2"}}
	pubAgree := dnsAnswers{name: "b.example", core: []string{"1.1.1.1"}, sys: []string{"2.2.2.2"}, pub: []string{"2.2.2.2"}}
	hijack := dnsAnswers{name: "c.example", core: []string{"1.1.1.1"}, sys: []string{"9.9.9.9"}, pub: []string{"2.2.2.2"}}
	fake := dnsAnswers{name: "d.example", core: []string{"1.1.1.1"}, sys: []string{"198.18.0.5"}, pub: []string{"2.2.2.2"}}
	timeout := dnsAnswers{name: "e.example", core: []string{"1.1.1.1"}, sys: []string{"1.1.1.1"}, pubErr: errors.New("i/o timeout")}
	cases := []struct {
		name string
		p    dnsCompareProbe
		want string
		rows int
	}{
		{"agrees with core", dnsCompareProbe{names: []dnsAnswers{agree}}, model.LeakOK, 3},
		{"agrees with public", dnsCompareProbe{names: []dnsAnswers{pubAgree}}, model.LeakOK, 3},
		{"hijacked", dnsCompareProbe{names: []dnsAnswers{agree, hijack}}, model.LeakWarn, 6},
		{"fake-ip is the core's answer", dnsCompareProbe{names: []dnsAnswers{fake}}, model.LeakOK, 3},
		{"system dns is the core listener", dnsCompareProbe{listen: "127.0.0.1:1053", sysDNS: []string{"127.0.0.1"}, names: []dnsAnswers{hijack}}, model.LeakOK, 3},
		{"public resolver timed out but system agrees", dnsCompareProbe{names: []dnsAnswers{timeout}}, model.LeakOK, 3},
		{"nothing resolved", dnsCompareProbe{names: []dnsAnswers{{name: "f.example", sysErr: errors.New("timeout")}}}, model.LeakWarn, 3},
	}
	for _, c := range cases {
		got := dnsCompareItem(c.p)
		if got.Status != c.want || got.Summary == "" || len(got.Rows) != c.rows {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if got := dnsCompareItem(dnsCompareProbe{names: []dnsAnswers{agree, hijack}}); !strings.Contains(got.Summary, "c.example") || strings.Contains(got.Summary, "a.example") {
		t.Errorf("hijacked name must be named: %q", got.Summary)
	}
	if got := dnsCompareItem(dnsCompareProbe{names: []dnsAnswers{timeout}}); !strings.Contains(got.Rows[2].Value, "timeout") {
		t.Errorf("failed lookups show the reason: %+v", got.Rows)
	}
}

// IP 类型 words the exit's kind and lists both endpoints.
func TestIPTypeItem(t *testing.T) {
	dc := ipTypeItem(ipTypeProbe{exit: model.EndpointInfo{IP: "45.76.211.38", ASN: "AS20473", ISP: "Vultr"}, direct: model.EndpointInfo{IP: "116.227.38.12", ASN: "AS4812", ISP: "中国电信"}})
	if dc.Status != model.LeakOK || dc.Summary != "出口 45.76.211.38 · AS20473 Vultr · 机房 IP" || !strings.Contains(dc.Detail, "机房") || len(dc.Rows) != 2 || !strings.Contains(dc.Rows[1].Value, "家宽") {
		t.Fatalf("datacenter exit = %+v", dc)
	}
	home := ipTypeItem(ipTypeProbe{exit: model.EndpointInfo{IP: "1.2.3.4", ASN: "AS7922", ISP: "Comcast"}})
	if home.Status != model.LeakOK || !strings.Contains(home.Detail, "不易被识别") || home.Rows[1].Value != "—" {
		t.Fatalf("residential exit = %+v", home)
	}
	none := ipTypeItem(ipTypeProbe{exitErr: errors.New("Get \"https://x\": dial tcp: connection refused")})
	if none.Status != model.LeakWarn || !strings.Contains(none.Detail, "connection refused") {
		t.Fatalf("no exit = %+v", none)
	}
}

// leakDomain accepts a host or URL and rejects what cannot be a domain.
func TestLeakDomain(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "  ": "", "Example.COM": "example.com", "https://Example.com/path?q=1": "example.com", "example.com:443": "example.com",
		"example.com.": "example.com", "localhost": "", "1.2.3.4": "", leakTestHost: "", "a b.com": "", "sub.example.co.uk/": "sub.example.co.uk",
	} {
		if got := leakDomain(in); got != want {
			t.Errorf("leakDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

// The judgement table: what each observation means.
func TestLeakJudgements(t *testing.T) {
	cases := []struct {
		name string
		p    dnsProbe
		want string
	}{
		{"system DNS points at the listener", dnsProbe{tun: true, listen: "127.0.0.1:1053", sysDNS: []string{"127.0.0.1"}, sysAnswers: []string{"1.2.3.4"}}, model.LeakOK},
		{"redir-host answers agree", dnsProbe{tun: true, sysAnswers: []string{"1.2.3.4", "5.6.7.8"}, coreAnswers: []string{"5.6.7.8"}}, model.LeakOK},
		{"redir-host answers differ", dnsProbe{tun: true, sysAnswers: []string{"1.2.3.4"}, coreAnswers: []string{"5.6.7.8"}}, model.LeakWarn},
		{"resolver failed", dnsProbe{tun: true, sysErr: errors.New("timeout")}, model.LeakWarn},
		{"nothing on", dnsProbe{sysAnswers: []string{"1.2.3.4"}}, model.LeakWarn},
		{"fake-ip under tun", dnsProbe{tun: true, fakeIP: true, sysAnswers: []string{"198.18.0.9"}}, model.LeakOK},
		{"fake-ip from upstream, no path to the core", dnsProbe{sysProxy: true, fakeIP: true, sysDNS: []string{"114.114.114.114"}, sysAnswers: []string{"198.18.0.9"}}, model.LeakWarn},
	}
	for _, c := range cases {
		if got := dnsLeakItem(c.p); got.Status != c.want || got.Summary == "" || got.Detail == "" {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	v6 := ipv6LeakItem(ipv6Probe{localV6: "2001:db8::1", direct6Err: errors.New("no route")})
	if v6.Status != model.LeakWarn || !strings.Contains(v6.Summary, "2001:db8::1") {
		t.Errorf("ipv6 with address but no route: %+v", v6)
	}
	if got := ipv6LeakItem(ipv6Probe{sysProxy: true, direct6: "2001:db8::1", via: "2606::1"}); got.Status != model.LeakWarn {
		t.Errorf("ipv6 under system proxy: %+v", got)
	}
}

// fetchTraceIP reads the ip= line and rejects other answers.
func TestFetchTraceIP(t *testing.T) {
	ok := traceServer(t, "203.0.113.9")
	ip, err := fetchTraceIP(context.Background(), ok.Client(), ok.URL)
	if err != nil || ip != "203.0.113.9" {
		t.Fatalf("ip = %q, %v", ip, err)
	}
	bad := traceServer(t, "")
	if _, err := fetchTraceIP(context.Background(), bad.Client(), bad.URL); err == nil {
		t.Fatal("HTTP 503 must fail")
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("fl=1\n")) }))
	defer empty.Close()
	if _, err := fetchTraceIP(context.Background(), empty.Client(), empty.URL); err == nil {
		t.Fatal("missing ip= must fail")
	}
}
