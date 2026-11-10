package locate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/geo"
	"lanflow/internal/model"
	"lanflow/internal/netx"
	"lanflow/internal/platform/netinfo"
)

// traceServer serves a Cloudflare-style trace body; the body can be swapped.
type traceServer struct {
	*httptest.Server
	mu   sync.Mutex
	body string
	hits atomic.Int32
}

func newTraceServer(t *testing.T, body string) *traceServer {
	t.Helper()
	ts := &traceServer{body: body}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.hits.Add(1)
		ts.mu.Lock()
		b := ts.body
		ts.mu.Unlock()
		if b == "" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(b))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *traceServer) set(body string) {
	ts.mu.Lock()
	ts.body = body
	ts.mu.Unlock()
}

func traceBody(ip, loc, colo string) string {
	return "fl=123f45\nh=www.cloudflare.com\nip=" + ip + "\nts=1700000000.123\nvisit_scheme=https\n" +
		"uag=Lanflow\ncolo=" + colo + "\nsliver=none\nhttp=http/2\nloc=" + loc + "\ntls=TLSv1.3\nsni=plaintext\nwarp=off\n"
}

// newTestLocator wires a Locator to the trace server, with no databases
// and a controllable clock.
func newTestLocator(t *testing.T, ts *traceServer, cachePath string) (*Locator, *time.Time, *int) {
	t.Helper()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	port := 0
	l := New(cachePath, "", "")
	l.l.traceURL = ts.URL
	l.l.now = func() time.Time { return now }
	l.l.proxyClient = func(p int, timeout time.Duration) *http.Client {
		port = p
		return ts.Client()
	}
	l.l.directClient = func(context.Context, time.Duration) (*http.Client, string, error) { return ts.Client(), "en0", nil }
	return l, &now, &port
}

func TestParseTrace(t *testing.T) {
	tr := parseTrace(traceBody("203.0.113.9", "jp", "nrt"))
	if tr.IP != "203.0.113.9" || tr.Loc != "JP" || tr.Colo != "NRT" {
		t.Errorf("parseTrace = %+v", tr)
	}
	tr = parseTrace("garbage\n\n=\nip = 2001:db8::1 \n")
	if tr.IP != "2001:db8::1" || tr.Loc != "" {
		t.Errorf("parseTrace = %+v", tr)
	}
}

func TestProbeAndCache(t *testing.T) {
	ts := newTraceServer(t, traceBody("203.0.113.9", "JP", "NRT"))
	dir := t.TempDir()
	cache := filepath.Join(dir, "sub", "exits.json")
	l, now, port := newTestLocator(t, ts, cache)

	var selected []string
	sel := func(ctx context.Context, node string) error {
		selected = append(selected, node)
		return nil
	}
	info, err := l.Probe(context.Background(), sel, 7890, "🇹🇼 台湾 01")
	if err != nil {
		t.Fatal(err)
	}
	if *port != 7890 {
		t.Errorf("probe port = %d", *port)
	}
	if len(selected) != 1 || selected[0] != "🇹🇼 台湾 01" {
		t.Errorf("sel calls = %v", selected)
	}
	if info.Source != model.LocateProbe || info.Colo != "NRT" || info.Exit.IP != "203.0.113.9" {
		t.Errorf("info = %+v", info)
	}
	if info.Place == nil || info.Place.City != "东京" || info.Place.Country != "JP" {
		t.Errorf("place = %+v", info.Place)
	}
	if info.Exit.Place == nil || info.Exit.Place.Country != "JP" {
		t.Errorf("exit place should fall back to the trace place: %+v", info.Exit.Place)
	}
	if !info.MeasuredAt.Equal(*now) {
		t.Errorf("MeasuredAt = %v", info.MeasuredAt)
	}

	// Cache file is JSON node → entry, written atomically (no temp files left).
	data, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]cacheEntry
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if e, ok := m["🇹🇼 台湾 01"]; !ok || e.Colo != "NRT" {
		t.Errorf("cache content = %s", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(cache))
	if len(entries) != 1 {
		t.Errorf("leftover files: %v", entries)
	}

	// Guess prefers the cached probe...
	g := l.Guess("🇹🇼 台湾 01")
	if g.Source != model.LocateProbe || g.Place.City != "东京" {
		t.Errorf("Guess (cached) = %+v", g)
	}
	// ...also from a fresh Locator reading the same file...
	l2 := New(cache, "", "")
	l2.l.now = l.l.now
	if g := l2.Guess("🇹🇼 台湾 01"); g.Source != model.LocateProbe || g.Colo != "NRT" {
		t.Errorf("Guess (fresh locator) = %+v", g)
	}
	// ...until it expires, then the name guess is used.
	*now = now.Add(24*time.Hour + time.Minute)
	g = l.Guess("🇹🇼 台湾 01")
	if g.Source != model.LocateName || g.Place == nil || g.Place.Country != "TW" {
		t.Errorf("Guess (expired) = %+v", g)
	}
	// Unknown name without cache.
	if g := l.Guess("Relay-7 专线"); g.Source != model.LocateUnknown || g.Place != nil || g.Node != "Relay-7 专线" {
		t.Errorf("Guess (unknown) = %+v", g)
	}

	// Storing a new probe prunes the expired one.
	if _, err := l.Probe(context.Background(), sel, 7890, "HK 01"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(cache)
	m = nil
	json.Unmarshal(data, &m)
	if _, stale := m["🇹🇼 台湾 01"]; stale || len(m) != 1 {
		t.Errorf("expired entry not pruned: %s", data)
	}
}

func TestProbeErrors(t *testing.T) {
	ts := newTraceServer(t, traceBody("203.0.113.9", "JP", "NRT"))
	l, _, _ := newTestLocator(t, ts, "")

	_, err := l.Probe(context.Background(), func(context.Context, string) error { return errors.New("boom") }, 1, "HK 01")
	if err == nil || !strings.Contains(err.Error(), "切换探测节点") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("sel error = %v", err)
	}
	ok := func(context.Context, string) error { return nil }

	ts.set("") // HTTP 502
	if _, err := l.Probe(context.Background(), ok, 1, "HK 01"); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("status error = %v", err)
	}
	ts.set("<html>captive portal</html>")
	if _, err := l.Probe(context.Background(), ok, 1, "HK 01"); err == nil || !strings.Contains(err.Error(), "ip 字段") {
		t.Errorf("no-ip error = %v", err)
	}
	if _, err := l.Probe(context.Background(), nil, 1, "HK 01"); err == nil {
		t.Error("nil sel must fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ts.set(traceBody("203.0.113.9", "JP", "NRT"))
	if _, err := l.Probe(ctx, ok, 1, "HK 01"); err == nil {
		t.Error("cancelled context must fail")
	}
	// Nothing was cached on failure.
	if g := l.Guess("HK 01"); g.Source != model.LocateName {
		t.Errorf("failed probes must not be cached: %+v", g)
	}
}

func TestProbeSerialized(t *testing.T) {
	ts := newTraceServer(t, traceBody("203.0.113.9", "SG", "SIN"))
	l, _, _ := newTestLocator(t, ts, "")
	var active, maxActive int32
	sel := func(ctx context.Context, node string) error {
		n := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&maxActive)
			if n <= m || atomic.CompareAndSwapInt32(&maxActive, m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := l.Probe(context.Background(), sel, 1, "SG"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if maxActive != 1 {
		t.Errorf("probes overlapped: max active = %d", maxActive)
	}
}

func TestDirect(t *testing.T) {
	ts := newTraceServer(t, traceBody("198.51.100.7", "JP", "NRT"))
	l, _, _ := newTestLocator(t, ts, "")
	ep, err := l.Direct(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ep.IP != "198.51.100.7" || ep.Place == nil || ep.Place.City != "东京" || ep.BoundIface != "en0" {
		t.Errorf("Direct = %+v", ep)
	}
	// Mainland users are served by PoPs abroad: trust loc over colo.
	ts.set(traceBody("1.2.3.4", "CN", "LAX"))
	ep, err = l.Direct(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ep.Place == nil || ep.Place.Country != "CN" {
		t.Errorf("Direct (CN via LAX) = %+v", ep.Place)
	}
	ts.set("")
	if _, err := l.Direct(context.Background()); err == nil || !strings.Contains(err.Error(), "本机出口") {
		t.Errorf("Direct error = %v", err)
	}
}

// Without a physical interface, or when binding fails, Direct reports an
// error and never measures through an unbound client.
func TestDirectNoFallback(t *testing.T) {
	ts := newTraceServer(t, traceBody("198.51.100.7", "JP", "NRT"))
	l, _, _ := newTestLocator(t, ts, "")
	l.l.directClient = func(context.Context, time.Duration) (*http.Client, string, error) {
		return nil, "", netinfo.ErrNoPhysical
	}
	if ep, err := l.Direct(context.Background()); err == nil || !strings.Contains(err.Error(), "物理网卡") || ep.IP != "" {
		t.Errorf("Direct without interface = %+v, %v", ep, err)
	}
	failing := ts.Client()
	failing.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial: %w: EPERM", netx.ErrBind)
	})
	l.l.directClient = func(context.Context, time.Duration) (*http.Client, string, error) { return failing, "en0", nil }
	if ep, err := l.Direct(context.Background()); err == nil || !strings.Contains(err.Error(), "无法绑定物理网卡 en0") || ep.IP != "" {
		t.Errorf("Direct with bind failure = %+v, %v", ep, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLookupWithoutDatabases(t *testing.T) {
	l := New("", filepath.Join(t.TempDir(), "missing.metadb"), "")
	ep := l.Lookup("1.1.1.1")
	if ep.IP != "1.1.1.1" || ep.Place != nil || ep.ASN != "" || ep.ISP != "" {
		t.Errorf("Lookup without db = %+v", ep)
	}
	if ep := l.Lookup("not-an-ip"); ep.IP != "not-an-ip" || ep.Place != nil {
		t.Errorf("Lookup invalid = %+v", ep)
	}
}

func TestMetaCountry(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"cn", "CN"},
		{[]any{"au", "cloudflare"}, "AU"},
		{[]any{"google", "us"}, "US"},
		{[]any{"cloudflare"}, ""},
		{[]string{"hk"}, "HK"},
		{"private", ""},
		{42, ""},
	}
	for _, c := range cases {
		if got := metaCountry(c.in); got != c.want {
			t.Errorf("metaCountry(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortenOrg(t *testing.T) {
	cases := map[string]string{
		"CHINANET-BACKBONE No.31,Jin-rong Street":     "中国电信",
		"China Telecom (Group)":                       "中国电信",
		"CHINA UNICOM China169 Backbone":              "中国联通",
		"China Mobile Communications Group Co., Ltd.": "中国移动",
		"Cloudflare, Inc.":                            "Cloudflare",
		"Google LLC":                                  "Google",
		"HKT Limited":                                 "HKT",
		"Amazon.com, Inc.":                            "Amazon AWS",
		"Hangzhou Alibaba Advertising Co.,Ltd.":       "阿里云",
		"Zenlayer Inc":                                "Zenlayer",
		"Some Random Hosting Ltd.":                    "Some Random Hosting",
		"AS12345 Example Networks GmbH":               "Example Networks",
		"":                                            "",
	}
	for in, want := range cases {
		if got := shortenOrg(in); got != want {
			t.Errorf("shortenOrg(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLookupRealDatabases runs against the databases another component
// downloads into bin/geo; skipped when they are absent.
func TestLookupRealDatabases(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	mmdb := filepath.Join(root, "bin", "geo", "geoip.metadb")
	asn := filepath.Join(root, "bin", "geo", "GeoLite2-ASN.mmdb")
	if _, err := os.Stat(mmdb); err != nil {
		t.Skip("bin/geo/geoip.metadb not present")
	}
	l := New("", mmdb, asn)
	ep := l.Lookup("1.1.1.1")
	if ep.Place == nil || ep.Place.Country != "AU" {
		t.Errorf("1.1.1.1 place = %+v", ep.Place)
	} else if ep.Place.City != "澳大利亚" {
		t.Errorf("country-only lookups show the country name, got %q", ep.Place.City)
	}
	if _, err := os.Stat(asn); err == nil {
		if ep.ASN != "AS13335" || ep.ISP != "Cloudflare" {
			t.Errorf("1.1.1.1 asn = %q isp = %q", ep.ASN, ep.ISP)
		}
		ep = l.Lookup("202.96.209.133")
		if ep.Place == nil || ep.Place.Country != "CN" || ep.ISP != "中国电信" {
			t.Errorf("202.96.209.133 = %+v isp=%q", ep.Place, ep.ISP)
		}
	}
	if ep := l.Lookup("2001:4860:4860::8888"); ep.Place == nil || ep.Place.Country != "US" {
		t.Errorf("IPv6 lookup = %+v", ep.Place)
	}
	if ep := l.Lookup("10.0.0.1"); ep.Place != nil {
		t.Errorf("private IP should have no place: %+v", ep.Place)
	}
}

// fakeResolver maps IPs to endpoints for analyze tests.
func fakeResolver(m map[string]model.EndpointInfo) func(string) model.EndpointInfo {
	return func(ip string) model.EndpointInfo {
		if ep, ok := m[ip]; ok {
			ep.IP = ip
			return ep
		}
		return model.EndpointInfo{IP: ip}
	}
}

func place(city string) *model.Place {
	p, ok := geo.FromName(city)
	if !ok {
		panic("unknown place " + city)
	}
	return &p
}

func countryPlace(code string) *model.Place {
	p, _ := geo.Country(code)
	p.City = geo.CountryName(code)
	return &p
}

func TestAnalyzeMatrix(t *testing.T) {
	l := New("", "", "")
	l.l.resolve = fakeResolver(map[string]model.EndpointInfo{
		"1.1.1.1": {Place: place("东京"), ISP: "NTT"},
		"2.2.2.2": {Place: countryPlace("CN"), ISP: "中国电信", ASN: "AS4134"},
		"3.3.3.3": {Place: countryPlace("JP")},
		"4.4.4.4": {Place: place("上海"), ISP: "中国电信"},
		"9.9.9.9": {},
		"8.8.8.8": {Place: countryPlace("US")},
		"5.5.5.5": {Place: countryPlace("HK")},
		"6.6.6.6": {Place: countryPlace("TW")},
		"7.7.7.7": {Place: countryPlace("JP")},
	})

	probed := func(node, exitIP, colo string) model.ExitInfo {
		p, _ := geo.FromTrace("", colo)
		ep := l.l.resolve(exitIP)
		if ep.Place == nil {
			ep.Place = &p
		}
		return model.ExitInfo{Node: node, Place: &p, Source: model.LocateProbe, Exit: ep, Colo: colo, MeasuredAt: time.Now()}
	}

	cases := []struct {
		name     string
		info     model.ExitInfo
		entryIP  string
		hops     int
		wantPath model.PathKind
		wantNote string // substring, "" = must be empty
	}{
		{"same ip is direct", probed("JP 01", "1.1.1.1", "NRT"), "1.1.1.1", 1, model.PathDirect, ""},
		{"same city is direct", probed("JP 01", "7.7.7.7", "NRT"), "1.1.1.1", 1, model.PathDirect, ""},
		{"country-level entry same country is direct", probed("JP 01", "7.7.7.7", "NRT"), "3.3.3.3", 1, model.PathDirect, ""},
		{"different place is relay", probed("US 01", "8.8.8.8", "LAX"), "2.2.2.2", 1, model.PathRelay, "入口 中国 中国电信 → 落地 洛杉矶"},
		{"relay with city-level entry", probed("US 01", "8.8.8.8", "LAX"), "4.4.4.4", 1, model.PathRelay, "入口 上海 中国电信 → 落地 洛杉矶"},
		{"chain", probed("US 01", "8.8.8.8", "LAX"), "2.2.2.2", 3, model.PathChain, "3 跳链式代理"},
		{"chain without entry", probed("US 01", "8.8.8.8", "LAX"), "", 2, model.PathChain, "2 跳"},
		{"ip country vs colo", probed("US 01", "8.8.8.8", "HKG"), "2.2.2.2", 1, model.PathSuspicious, "IP 归属美国，实际经香港机房"},
		{"name vs measurement", probed("🇹🇼 台湾 01", "5.5.5.5", "HKG"), "2.2.2.2", 1, model.PathSuspicious, "名称为台湾，实测出口在香港"},
		{"suspicious beats chain", probed("🇹🇼 台湾 01", "5.5.5.5", "HKG"), "2.2.2.2", 3, model.PathSuspicious, "名称为台湾"},
		{"mainland-only name is not a contradiction", probed("上海 中转 01", "7.7.7.7", "NRT"), "2.2.2.2", 1, model.PathRelay, "落地 东京"},
		{"unknown entry ip stays unknown", probed("JP 01", "7.7.7.7", "NRT"), "9.9.9.9", 1, model.PathUnknown, ""},
		{"no entry stays unknown", probed("JP 01", "7.7.7.7", "NRT"), "", 1, model.PathUnknown, ""},
		{"name guess only, no entry", model.ExitInfo{Node: "🇭🇰 HK", Place: place("香港"), Source: model.LocateName}, "", 0, model.PathUnknown, ""},
		{"name guess with entry elsewhere is relay", model.ExitInfo{Node: "🇭🇰 HK", Place: place("香港"), Source: model.LocateName}, "2.2.2.2", 1, model.PathRelay, "落地 香港"},
		{"name guess never suspicious", model.ExitInfo{Node: "🇹🇼 台湾", Place: place("香港"), Source: model.LocateName}, "5.5.5.5", 1, model.PathDirect, ""},
	}
	for _, tc := range cases {
		info := tc.info
		l.Analyze(&info, tc.entryIP, tc.hops)
		if info.Path != tc.wantPath {
			t.Errorf("%s: path = %q, want %q (note %q)", tc.name, info.Path, tc.wantPath, info.PathNote)
		}
		if tc.wantNote == "" && info.PathNote != "" || tc.wantNote != "" && !strings.Contains(info.PathNote, tc.wantNote) {
			t.Errorf("%s: note = %q, want %q", tc.name, info.PathNote, tc.wantNote)
		}
		if info.ChainHops != tc.hops {
			t.Errorf("%s: hops = %d", tc.name, info.ChainHops)
		}
		if tc.entryIP != "" && (info.Entry == nil || info.Entry.IP != tc.entryIP) {
			t.Errorf("%s: entry = %+v", tc.name, info.Entry)
		}
		if tc.entryIP == "" && info.Entry != nil {
			t.Errorf("%s: entry should be nil", tc.name)
		}
	}
	l.Analyze(nil, "", 0) // must not panic
}

func TestAnalyzeLatencyFloor(t *testing.T) {
	ts := newTraceServer(t, traceBody("8.8.8.8", "US", "LAX"))
	l, now, _ := newTestLocator(t, ts, "")
	l.l.resolve = fakeResolver(map[string]model.EndpointInfo{
		"8.8.8.8": {Place: countryPlace("US")},
		"4.4.4.4": {Place: place("上海"), ISP: "中国电信"},
	})
	// Make the probe appear to take 30 ms: far below Shanghai → Los Angeles.
	calls := 0
	base := *now
	l.l.now = func() time.Time {
		calls++
		if calls == 2 {
			return base.Add(30 * time.Millisecond)
		}
		return base
	}
	info, err := l.Probe(context.Background(), func(context.Context, string) error { return nil }, 1, "US 01")
	if err != nil {
		t.Fatal(err)
	}
	l.Analyze(&info, "4.4.4.4", 1)
	if info.Path != model.PathSuspicious || !strings.Contains(info.PathNote, "物理下限") {
		t.Errorf("path = %q note = %q", info.Path, info.PathNote)
	}
	// Country-level entry: no latency judgement, plain relay.
	l.l.resolve = fakeResolver(map[string]model.EndpointInfo{
		"8.8.8.8": {Place: countryPlace("US")},
		"2.2.2.2": {Place: countryPlace("CN")},
	})
	l.Analyze(&info, "2.2.2.2", 1)
	if info.Path != model.PathRelay {
		t.Errorf("path = %q note = %q", info.Path, info.PathNote)
	}
}

func TestMinRTT(t *testing.T) {
	sh, la := *place("上海"), *place("洛杉矶")
	got := minRTT(sh, la)
	if got < 95*time.Millisecond || got > 115*time.Millisecond {
		t.Errorf("Shanghai→Los Angeles floor = %v, want ≈104ms", got)
	}
	if minRTT(sh, sh) != 0 {
		t.Error("same place must be 0")
	}
	if d := distanceKm(0, 0, 0, 180); d < 20000 || d > 20100 {
		t.Errorf("antipodal distance = %v", d)
	}
}

func TestCorruptCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exits.json")
	os.WriteFile(path, []byte("{not json"), 0o644)
	l := New(path, "", "")
	if g := l.Guess("JP 01"); g.Source != model.LocateName {
		t.Errorf("corrupt cache should be ignored: %+v", g)
	}
}
