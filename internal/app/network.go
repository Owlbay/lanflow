package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

const siteTimeout = 5 * time.Second

// RefreshNetwork re-reads the local network information, looks the direct
// public endpoint up again and re-measures the main exit.
func (s *Service) RefreshNetwork() error {
	err := s.refreshNetInfo()
	s.refreshYou()
	s.scheduleMainProbe()
	return err
}

// refreshNetInfo re-reads the local network information only.
func (s *Service) refreshNetInfo() error {
	ctx, cancel := s.ctrlCtx(nil, 5*time.Second)
	defer cancel()
	info, err := s.d.netinfo(ctx)
	s.mu.Lock()
	if err == nil || info.LANIP != "" {
		info.PublicIPStatus, info.PublicIPError = s.state.net.PublicIPStatus, s.state.net.PublicIPError
		s.state.net = info
	}
	s.state.st.LANAddress = info.LANIP
	s.mu.Unlock()
	s.em.emit(TopicNet, TopicStatus)
	if err != nil {
		return fmt.Errorf(i18n.T("读取网络信息失败：%v"), err)
	}
	return nil
}

// TestSites measures every configured site through the proxy and directly.
func (s *Service) TestSites() error {
	s.mu.Lock()
	sites := append([]model.Site(nil), s.state.settings.Sites...)
	running := s.state.st.Core == model.CoreRunning
	port := s.state.settings.MixedPort
	checks := make([]model.SiteCheck, len(sites))
	for i, site := range sites {
		checks[i] = model.SiteCheck{Name: site.Name, URL: site.URL, ViaProxy: -2, Direct: -2}
		if !running {
			checks[i].ViaProxy = 0
		}
	}
	s.state.sites = checks
	s.mu.Unlock()
	s.em.emit(TopicNet)

	direct := s.d.httpDirect(siteTimeout)
	var via *http.Client
	if running {
		via = s.d.httpVia(port, siteTimeout)
	}
	var wg sync.WaitGroup
	set := func(i int, proxy bool, ms int) {
		s.mu.Lock()
		if i < len(s.state.sites) && s.state.sites[i].URL == sites[i].URL {
			if proxy {
				s.state.sites[i].ViaProxy = ms
			} else {
				s.state.sites[i].Direct = ms
			}
		}
		s.mu.Unlock()
		s.em.emit(TopicNet)
	}
	for i, site := range sites {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			set(i, false, measure(s.rootCtx, direct, u))
		}(i, site.URL)
		if via != nil {
			wg.Add(1)
			go func(i int, u string) {
				defer wg.Done()
				set(i, true, measure(s.rootCtx, via, u))
			}(i, site.URL)
		}
	}
	wg.Wait()
	return nil
}

// measure returns the milliseconds a HEAD (falling back to GET) takes, -1 on failure.
func measure(ctx context.Context, c *http.Client, u string) int {
	start := time.Now()
	ok := doRequest(ctx, c, http.MethodHead, u)
	if !ok {
		start = time.Now()
		ok = doRequest(ctx, c, http.MethodGet, u)
	}
	if !ok {
		return -1
	}
	ms := int(time.Since(start) / time.Millisecond)
	if ms < 1 {
		ms = 1
	}
	return ms
}

func doRequest(ctx context.Context, c *http.Client, method, u string) bool {
	ctx, cancel := context.WithTimeout(ctx, siteTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	// any HTTP answer means the site is reachable; 405 for HEAD is handled by the GET retry
	return method != http.MethodHead || resp.StatusCode != http.StatusMethodNotAllowed
}

// ---- DNS ----

var rcodeNames = map[int]string{0: "NOERROR", 1: "FORMERR", 2: "SERVFAIL", 3: "NXDOMAIN", 4: "NOTIMP", 5: "REFUSED"}

// defaultFakeIPRange is Mihomo's fake-ip range when the config names none
// (the composer writes 198.18.0.1/16; subscriptions may pick another).
var defaultFakeIPRange = func() *net.IPNet { _, n, _ := net.ParseCIDR("198.18.0.0/15"); return n }()

const appDNSTimeout = 4 * time.Second

// runtimeDNS is what the running config says about DNS, for the query tool.
type runtimeDNS struct {
	Listen    string     // dns.listen, "" when the core has no DNS server
	FakeIP    bool       // enhanced-mode fake-ip
	FakeRange *net.IPNet // fake-ip-range, nil = default
}

// parseRuntimeDNS reads the DNS keys of the runtime YAML. Anything
// unreadable yields the zero value.
func parseRuntimeDNS(y []byte) runtimeDNS {
	var doc struct {
		DNS struct {
			Listen string `yaml:"listen"`
			Mode   string `yaml:"enhanced-mode"`
			Range  string `yaml:"fake-ip-range"`
		} `yaml:"dns"`
	}
	if err := yaml.Unmarshal(y, &doc); err != nil {
		return runtimeDNS{}
	}
	out := runtimeDNS{Listen: strings.TrimSpace(doc.DNS.Listen), FakeIP: doc.DNS.Mode == "fake-ip"}
	if _, n, err := net.ParseCIDR(strings.TrimSpace(doc.DNS.Range)); err == nil {
		out.FakeRange = n
	}
	return out
}

// listenAddr turns a dns.listen value into an address this machine can
// query: wildcard hosts become loopback, a bare port gets one.
func listenAddr(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]", "*":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// lookupVia resolves name the way an application does: through the DNS
// server at server ("host:port") or, with server "", the system resolver.
// Only A, AAAA and CNAME have a standard-library lookup.
func lookupVia(ctx context.Context, server, name, qtype string) ([]string, error) {
	r := net.DefaultResolver
	if server != "" {
		r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", server)
		}}
	}
	switch qtype {
	case "A", "AAAA":
		network := "ip4"
		if qtype == "AAAA" {
			network = "ip6"
		}
		ips, err := r.LookupIP(ctx, network, name)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(ips))
		for _, ip := range ips {
			out = append(out, ip.String())
		}
		return out, nil
	case "CNAME":
		cname, err := r.LookupCNAME(ctx, name)
		if err != nil {
			return nil, err
		}
		return []string{cname}, nil
	}
	return nil, errors.New("unsupported type")
}

// allFakeIP reports whether every answer is an address of the fake-ip range.
func allFakeIP(answers []string, rng *net.IPNet) bool {
	if len(answers) == 0 {
		return false
	}
	if rng == nil {
		rng = defaultFakeIPRange
	}
	for _, a := range answers {
		ip := net.ParseIP(strings.TrimSpace(a))
		if ip == nil || !rng.Contains(ip) {
			return false
		}
	}
	return true
}

// DNSQuery resolves name through the core's DNS. Answers is what the core's
// resolver returns (the real records: /dns/query bypasses fake-ip); when
// applications on this machine resolve through the core, AppAnswers is
// what they get for the same name, which in fake-ip mode is a Fake-IP.
func (s *Service) DNSQuery(name, qtype string) (model.DNSResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.DNSResult{}, errors.New(i18n.T("请输入要查询的域名。"))
	}
	if qtype = strings.ToUpper(strings.TrimSpace(qtype)); qtype == "" {
		qtype = "A"
	}
	if !s.running() {
		return model.DNSResult{}, errCoreNotRunning
	}
	ctx, cancel := s.ctrlCtx(nil, ctrlTimeout)
	defer cancel()
	r, err := s.d.ctrl.DNSQuery(ctx, name, qtype)
	if err != nil {
		return model.DNSResult{}, fmt.Errorf(i18n.T("DNS 查询失败：%v"), err)
	}
	out := model.DNSResult{Name: name, Type: qtype, Status: rcodeNames[r.Status]}
	if out.Status == "" {
		out.Status = fmt.Sprintf("RCODE %d", r.Status)
	}
	for _, a := range r.Answer {
		out.Answers = append(out.Answers, a.Data)
	}

	s.mu.Lock()
	rd := s.state.runtimeDNS
	tun := s.state.st.TUN
	s.mu.Unlock()
	server, source := "", ""
	switch {
	case rd.Listen != "":
		server, source = listenAddr(rd.Listen), "listener"
	case tun:
		source = "system" // TUN hijacks port 53, so the system resolver reaches the core
	}
	if source != "" && (qtype == "A" || qtype == "AAAA" || qtype == "CNAME") {
		actx, acancel := s.ctrlCtx(nil, appDNSTimeout)
		app, err := s.d.appDNS(actx, server, name, qtype)
		acancel()
		if err == nil && len(app) > 0 {
			out.AppAnswers, out.AppSource = app, source
		}
	}
	if len(out.AppAnswers) > 0 {
		out.FakeIP = allFakeIP(out.AppAnswers, rd.FakeRange)
	} else {
		out.FakeIP = allFakeIP(out.Answers, rd.FakeRange)
	}
	return out, nil
}

// FlushDNSCache clears the core's DNS cache.
func (s *Service) FlushDNSCache() error {
	if !s.running() {
		return errCoreNotRunning
	}
	ctx, cancel := s.ctrlCtx(nil, ctrlTimeout)
	defer cancel()
	if err := s.d.ctrl.FlushDNSCache(ctx); err != nil {
		return fmt.Errorf(i18n.T("清除 DNS 缓存失败：%v"), err)
	}
	return nil
}

// FlushFakeIPCache clears the core's fake-ip mappings.
func (s *Service) FlushFakeIPCache() error {
	if !s.running() {
		return errCoreNotRunning
	}
	ctx, cancel := s.ctrlCtx(nil, ctrlTimeout)
	defer cancel()
	if err := s.d.ctrl.FlushFakeIPCache(ctx); err != nil {
		return fmt.Errorf(i18n.T("清除 Fake-IP 缓存失败：%v"), err)
	}
	return nil
}
