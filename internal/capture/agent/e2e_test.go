package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"lanflow/internal/capture/ca"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// End-to-end tests against the real kernel (bin/mihomo): everything
// listens on 127.0.0.1 random ports with a temporary -d directory; no
// TUN, no system proxy, no system trust store (docs/capture.md §12).

func TestE2ESelectedIsDecryptedAndRecorded(t *testing.T) {
	r := newRig(t, rigOpts{domains: []string{"sel.test", "raw.test"}})
	up := startUpstream(t, r.pki.leaf(t, "sel.test", "other.test"), nil)

	for _, h2 := range []bool{true, false} {
		c := r.client(h2)
		resp, err := c.Post(fmt.Sprintf("https://sel.test:%d/hello?x=1", up.port), "application/json", strings.NewReader(`{"password":"p","n":1}`))
		if err != nil {
			t.Fatalf("h2=%v: %v\n%s", h2, err, r.dumpFlows())
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), `"path":"/hello"`) {
			t.Fatalf("h2=%v: %d %s", h2, resp.StatusCode, body)
		}
		if !issuedByLanflow(resp.TLS.PeerCertificates[0]) {
			t.Fatalf("h2=%v: selected host not decrypted (issuer %s)", h2, resp.TLS.PeerCertificates[0].Issuer)
		}
		wantProto := map[bool]string{true: "h2", false: "h1"}[h2]
		f, ok := r.waitFlow(func(f model.CaptureFlow) bool {
			return f.Proto == wantProto && f.Path == "/hello?x=1" && f.Method == "POST"
		})
		if !ok {
			t.Fatalf("h2=%v: no record\n%s", h2, r.dumpFlows())
		}
		if f.Status != 200 || f.Host != "sel.test" || !f.Has(model.FlowDecrypted) || f.ContentType != "application/json" {
			t.Fatalf("record %+v", f)
		}
		d, _ := r.agent.Flow(f.ID)
		if string(d.ReqBody) != `{"password":"p","n":1}` || !strings.Contains(string(d.RespBody), `"path":"/hello"`) {
			t.Fatalf("bodies %q %q", d.ReqBody, d.RespBody)
		}
		if d.TLS.SNI != "sel.test" || d.TLS.ALPN == "" && h2 || len(d.TLS.UpstreamCert) == 0 {
			t.Fatalf("tls %+v", d.TLS)
		}
		// attribution from the kernel's connection list (no process: the
		// kernel looks processes up only for rules that need them)
		if f.Attribution != "certain" {
			t.Fatalf("attribution %q %q", f.Process, f.Attribution)
		}
	}

	// byte identity: what the upstream receives is exactly what the client sent
	raw := startPlainTLS(t, r.pki.leaf(t, "raw.test"))
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", r.mixed))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT raw.test:%d HTTP/1.1\r\nHost: raw.test:%d\r\n\r\n", raw.port, raw.port)
	br := bufio.NewReader(conn)
	if l, _ := br.ReadString('\n'); !strings.Contains(l, "200") {
		t.Fatalf("CONNECT: %q", l)
	}
	for l, _ := br.ReadString('\n'); l != "\r\n" && l != ""; l, _ = br.ReadString('\n') {
	}
	tc := tls.Client(conn, &tls.Config{ServerName: "raw.test", RootCAs: r.clPool, NextProtos: []string{"http/1.1"}})
	sent := "GET /exact HTTP/1.1\r\nhost: raw.test\r\nX-lower-Case: A\r\nZ-First: 1\r\nA-Second: 2\r\n\r\n"
	if _, err := io.WriteString(tc, sent); err != nil {
		t.Fatal(err)
	}
	reply, _ := io.ReadAll(tc)
	if !strings.HasSuffix(string(reply), "ok") || !issuedByLanflow(tc.ConnectionState().PeerCertificates[0]) {
		t.Fatalf("raw reply %q", reply)
	}
	raw.mu.Lock()
	got := string(raw.got)
	raw.mu.Unlock()
	if got != sent {
		t.Fatalf("upstream got different bytes:\n%q\nwant\n%q", got, sent)
	}
}

func TestE2EUnselectedKeepsOriginalCertificate(t *testing.T) {
	r := newRig(t, rigOpts{domains: []string{"sel.test"}})
	cert := r.pki.leaf(t, "other.test", "sel.test")
	up := startUpstream(t, cert, nil)
	before := r.agent.Accepts.Load()
	resp, err := r.client(true).Get(fmt.Sprintf("https://other.test:%d/", up.port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := ca.Fingerprint(resp.TLS.PeerCertificates[0].Raw); got != ca.Fingerprint(cert.Leaf.Raw) {
		t.Fatalf("unselected host got a different certificate: %s", resp.TLS.PeerCertificates[0].Issuer)
	}
	if n := r.agent.Accepts.Load() - before; n != 0 {
		t.Fatalf("unselected connection reached the agent (%d)", n)
	}
}

func TestE2EScrubbedReferencesAndLoopBreaker(t *testing.T) {
	// An override routing everything to the outbound: scrubbed, so an
	// unselected host never reaches the agent.
	t.Run("scrub", func(t *testing.T) {
		r := newRig(t, rigOpts{domains: []string{"sel.test"}, mutate: nil, override: ""})
		// a second compose with an override that knows the name
		r.in.GlobalOverride += "+rules:\n  - MATCH," + r.outbound() + "\nproxy-groups:\n  - {name: g, type: select, proxies: [" + r.outbound() + ", DIRECT]}\n"
		r.k.reload(r.compose(true))
		up := startUpstream(t, r.pki.leaf(t, "other.test"), nil)
		before := r.agent.Accepts.Load()
		resp, err := r.client(true).Get(fmt.Sprintf("https://other.test:%d/", up.port))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if issuedByLanflow(resp.TLS.PeerCertificates[0]) || r.agent.Accepts.Load() != before {
			t.Fatal("override reference to the outbound survived")
		}
	})

	// A config that loops on purpose (the return listener routed into the
	// outbound, as a subscription could if nothing scrubbed it): the
	// breaker refuses at the first nested connection.
	t.Run("breaker", func(t *testing.T) {
		r := newRig(t, rigOpts{domains: []string{"sel.test"}, mutate: loopConfig})
		up := startUpstream(t, r.pki.leaf(t, "sel.test"), nil)
		start := time.Now()
		_, err := r.client(true).Get(fmt.Sprintf("https://sel.test:%d/", up.port))
		if err == nil {
			t.Fatalf("a looping configuration answered\n%s", r.dumpFlows())
		}
		time.Sleep(500 * time.Millisecond)
		n := r.agent.Accepts.Load()
		if n > 4 {
			t.Fatalf("breaker let %d connections in", n)
		}
		if _, ok := r.waitFlow(func(f model.CaptureFlow) bool { return f.Has(model.FlowLoop) }); !ok {
			t.Fatalf("no loop record\n%s", r.dumpFlows())
		}
		t.Logf("loop refused after %d accepts in %v", n, time.Since(start))
	})
}

// loopConfig routes the return listener into the decrypting outbound.
func loopConfig(cfg []byte, outbound string) []byte {
	return []byte(strings.Replace(string(cfg), "    udp: false\n", "    udp: false\n    proxy: "+outbound+"\n", 1))
}

// TestM0LoopRate measures how fast an unbroken loop nests (docs/capture.md
// §11 M0: the breaker's N and K). Opt-in: it floods the kernel.
func TestM0LoopRate(t *testing.T) {
	if os.Getenv("LANFLOW_M0") != "1" {
		t.Skip("LANFLOW_M0=1 measures the unbroken loop rate")
	}
	r := newRig(t, rigOpts{domains: []string{"sel.test"}, mutate: loopConfig, noLoop: true, noCtrl: true})
	up := startUpstream(t, r.pki.leaf(t, "sel.test"), nil)
	go r.client(true).Get(fmt.Sprintf("https://sel.test:%d/", up.port))
	time.Sleep(2 * time.Second)
	t.Logf("M0: unbroken loop: %d nested connections in 2 s", r.agent.Accepts.Load())
}

func TestE2EUpstreamInvalidPassesThroughUnchanged(t *testing.T) {
	r := newRig(t, rigOpts{domains: []string{"bad.test"}})
	bad := selfSigned(t, "bad.test") // the agent's roots do not vouch for it
	up := startUpstream(t, bad, nil)
	cl := r.client(false, bad.Leaf)
	resp, err := cl.Get(fmt.Sprintf("https://bad.test:%d/x", up.port))
	if err != nil {
		t.Fatalf("%v\n%s", err, r.dumpFlows())
	}
	resp.Body.Close()
	cl.CloseIdleConnections()
	if ca.Fingerprint(resp.TLS.PeerCertificates[0].Raw) != ca.Fingerprint(bad.Leaf.Raw) {
		t.Fatal("client did not see the upstream's own certificate")
	}
	if _, ok := r.waitFlow(func(f model.CaptureFlow) bool { return f.Has(model.FlowUpstreamBad) && f.Has(model.FlowPassthrough) }); !ok {
		t.Fatalf("no upstream-invalid record\n%s", r.dumpFlows())
	}
	// the replayed ClientHello reached the upstream byte for byte: the
	// handshake above succeeded with the client's own random, so nothing
	// was re-encoded
}

func TestE2EDisableStopsDecryptionAndClosesConnections(t *testing.T) {
	r := newRig(t, rigOpts{domains: []string{"sel.test"}})
	cert := r.pki.leaf(t, "sel.test")
	up := startUpstream(t, cert, nil)
	c := r.client(true)
	url := fmt.Sprintf("https://sel.test:%d/", up.port)
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !issuedByLanflow(resp.TLS.PeerCertificates[0]) {
		t.Fatal("not decrypted before turning off")
	}
	// a long request in flight on a decrypted connection
	slowErr := make(chan error, 1)
	go func() {
		resp, err := c.Get(url + "slow")
		if err == nil {
			_, err = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		slowErr <- err
	}()
	time.Sleep(300 * time.Millisecond)

	// the off sequence (docs/capture.md §9 配置重载)
	off := false
	r.agent.Apply(Update{Enabled: &off})
	r.k.reload(r.compose(false))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	for _, cn := range r.k.connections() {
		if slices.Contains(cn.Chains, r.outbound()) {
			r.k.ctrl.CloseConnection(ctx, cn.ID)
		}
	}
	cancel()
	select {
	case err := <-slowErr:
		if err == nil {
			t.Fatal("the in-flight decrypted request survived turning off")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the decrypted connection was not closed")
	}
	c.CloseIdleConnections()
	resp, err = r.client(true).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ca.Fingerprint(resp.TLS.PeerCertificates[0].Raw) != ca.Fingerprint(cert.Leaf.Raw) {
		t.Fatal("still decrypted after turning off")
	}
	for _, cn := range r.k.connections() {
		if slices.Contains(cn.Chains, r.outbound()) {
			t.Fatalf("connection through the outbound remains: %+v", cn)
		}
	}
}

// lanAddr is a non-loopback IPv4 address of this machine.
func lanAddr(t *testing.T) string {
	ifs, _ := net.InterfaceAddrs()
	for _, a := range ifs {
		if ip, ok := a.(*net.IPNet); ok && ip.IP.To4() != nil && !ip.IP.IsLoopback() && !ip.IP.IsLinkLocalUnicast() {
			return ip.IP.String()
		}
	}
	t.Skip("no non-loopback IPv4 address")
	return ""
}

func TestE2ELANSourceIsNotDecrypted(t *testing.T) {
	lan := lanAddr(t)
	// The mixed port is bound to the LAN address only (and only that
	// address may use it); a connection from it is "from the LAN".
	r := newRig(t, rigOpts{domains: []string{"sel.test"}, allowLAN: lan})
	cert := r.pki.leaf(t, "sel.test")
	up := startUpstream(t, cert, nil)
	get := func() *http.Response {
		t.Helper()
		pool := r.clPool.Clone()
		d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(lan)}}
		tr := &http.Transport{Proxy: http.ProxyURL(mustURL(fmt.Sprintf("http://%s:%d", lan, r.mixed))), DialContext: d.DialContext,
			TLSClientConfig: &tls.Config{RootCAs: pool}}
		resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Get(fmt.Sprintf("https://sel.test:%d/", up.port))
		if err != nil {
			t.Fatalf("%v\n%s", err, r.dumpFlows())
		}
		resp.Body.Close()
		tr.CloseIdleConnections()
		return resp
	}
	resp := get()
	if ca.Fingerprint(resp.TLS.PeerCertificates[0].Raw) != ca.Fingerprint(cert.Leaf.Raw) {
		t.Fatal("a LAN source was decrypted")
	}
	if n := r.agent.Accepts.Load(); n != 0 {
		t.Fatalf("the rule let a LAN source reach the agent (%d)", n)
	}

	// Second line: with the rule's source clause removed, the agent's own
	// check (the kernel's connection list) still refuses.
	cfg := strings.Replace(string(r.compose(true)), ",(OR,((SRC-IP-CIDR,127.0.0.0/8),(SRC-IP-CIDR,::1/128),(IN-TYPE,TUN)))", "", 1)
	cfg = strings.Replace(cfg, "allow-lan: false", "allow-lan: true", 1) + fmt.Sprintf("bind-address: %s\nlan-allowed-ips: [%s/32]\n", lan, lan)
	r.k.reload([]byte(cfg))
	resp = get()
	if ca.Fingerprint(resp.TLS.PeerCertificates[0].Raw) != ca.Fingerprint(cert.Leaf.Raw) {
		t.Fatalf("the agent decrypted a LAN source\n%s", r.dumpFlows())
	}
	if _, ok := r.waitFlow(func(f model.CaptureFlow) bool { return f.Has(model.FlowNotLocal) }); !ok {
		for _, c := range r.k.connections() {
			t.Logf("%+v", c)
		}
		t.Fatalf("no not-local record\n%s", r.dumpFlows())
	}
}

func mustURL(s string) *urlT { u, _ := parseURL(s); return u }

func TestE2EAppSelectionAttribution(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self, _ = filepath.EvalSymlinks(self) // the kernel reports /private/var/…, not /var/…
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("no curl")
	}
	apps := []profile.CaptureApp{
		{Rules: []string{"PROCESS-PATH-WILDCARD," + self}},
		{Rules: []string{"PROCESS-NAME,curl"}},
	}
	r := newRig(t, rigOpts{apps: apps})
	up := startUpstream(t, r.pki.leaf(t, "app.test"), nil)

	// from this process: app0
	resp, err := r.client(true).Get(fmt.Sprintf("https://app.test:%d/mine", up.port))
	if err != nil {
		t.Fatalf("%v\n%s", err, r.dumpFlows())
	}
	resp.Body.Close()
	if !issuedByLanflow(resp.TLS.PeerCertificates[0]) {
		t.Fatal("app selection not decrypted")
	}
	f, ok := r.waitFlow(func(f model.CaptureFlow) bool { return f.Path == "/mine" })
	if !ok || f.Selector != "app0" || f.Attribution != "certain" || f.ProcessPath != self {
		t.Fatalf("attribution %+v\n%s", f, r.dumpFlows())
	}

	// from curl: app1
	pem := string(ca.EncodePEM(r.root.Cert.Raw)) + string(ca.EncodePEM(r.pki.cert.Raw))
	bundle := t.TempDir() + "/ca.pem"
	os.WriteFile(bundle, []byte(pem), 0o600)
	out, err := exec.Command(curl, "-sS", "--http1.1", "-x", fmt.Sprintf("http://127.0.0.1:%d", r.mixed), "--cacert", bundle,
		fmt.Sprintf("https://app.test:%d/curl", up.port)).CombinedOutput()
	if err != nil {
		t.Fatalf("curl: %v %s\n%s", err, out, r.dumpFlows())
	}
	f, ok = r.waitFlow(func(f model.CaptureFlow) bool { return f.Path == "/curl" })
	if !ok || f.Selector != "app1" || f.Process != "curl" || f.Attribution != "certain" {
		t.Fatalf("curl attribution %+v\n%s", f, r.dumpFlows())
	}
	// a host nobody selected, from neither app, is untouched: the test
	// process is app0, so use a fresh rig without app0
}

func TestE2EECHPassesThroughAndGreaseDoesNot(t *testing.T) {
	r := newRig(t, rigOpts{domains: []string{"ech.test", "public.test"}})
	list, keys := echKeys(t, "public.test")
	cert := r.pki.leaf(t, "ech.test", "public.test")
	up := startUpstream(t, cert, keys)
	// a real ECH ClientHello: the outer SNI is the public name
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", r.mixed))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT ech.test:%d HTTP/1.1\r\nHost: ech.test\r\n\r\n", up.port)
	br := bufio.NewReader(conn)
	for l, _ := br.ReadString('\n'); l != "\r\n" && l != ""; l, _ = br.ReadString('\n') {
	}
	tc := tls.Client(conn, &tls.Config{ServerName: "ech.test", RootCAs: r.clPool, EncryptedClientHelloConfigList: list, MinVersion: tls.VersionTLS13})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("ECH handshake: %v\n%s", err, r.dumpFlows())
	}
	st := tc.ConnectionState()
	if !st.ECHAccepted || issuedByLanflow(st.PeerCertificates[0]) {
		t.Fatalf("ECH connection decrypted or ECH lost (accepted=%v)", st.ECHAccepted)
	}
	tc.Close()
	if _, ok := r.waitFlow(func(f model.CaptureFlow) bool { return f.Has(model.FlowECH) }); !ok {
		t.Fatalf("no ECH record\n%s", r.dumpFlows())
	}
}

func TestDecideGreaseECH(t *testing.T) {
	a := &Agent{root: &ca.Root{}, pins: newPinTable()}
	cfg := compile(Config{Enabled: true, Domains: []string{"sel.test"}})
	sel := Selector{Kind: "domain", User: "u"}
	// GREASE: the extension is there but the SNI is the target
	if why, _ := a.decide(cfg, sel, connectReq{host: "sel.test", port: 443}, &clientHello{sni: "sel.test", ech: true}); why != "" {
		t.Fatalf("GREASE ECH not decrypted: %s", why)
	}
	if why, flags := a.decide(cfg, sel, connectReq{host: "sel.test", port: 443}, &clientHello{sni: "cloudflare-ech.com", ech: true}); why == "" || !slices.Contains(flags, model.FlowECH) {
		t.Fatal("real ECH decrypted")
	}
	if why, _ := a.decide(cfg, sel, connectReq{host: "other.test", port: 443}, &clientHello{sni: "other.test"}); why == "" {
		t.Fatal("unselected decrypted")
	}
	if why, _ := a.decide(cfg, sel, connectReq{host: "sel.test", port: 443}, &clientHello{sni: "other.test"}); why == "" {
		t.Fatal("SNI mismatch decrypted")
	}
	if why, _ := a.decide(cfg, sel, connectReq{host: "127.0.0.1", port: 443}, &clientHello{sni: "sel.test"}); why != "" {
		t.Fatalf("IP target with a selected SNI: %s", why)
	}
	cfg.enabled = false
	if why, _ := a.decide(cfg, sel, connectReq{host: "sel.test", port: 443}, &clientHello{sni: "sel.test"}); why == "" {
		t.Fatal("decrypted while off")
	}
}
