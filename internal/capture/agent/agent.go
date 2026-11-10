// Package agent is `lanflow capture-agent` (docs/capture.md §5): the
// process Mihomo hands selected connections to through a password
// protected `type: http` outbound. For each CONNECT it reads the
// ClientHello and decides on its own (the rules are only the first
// filter) whether to decrypt: the selection, the bypass list, local
// sources only, ECH, pinning and the loop breaker. A decrypted connection
// is terminated on both sides and its plaintext copied byte for byte; a
// copy of the bytes is parsed (HTTP/1, HTTP/2, WebSocket) into records.
// Every other connection is passed through unchanged, the ClientHello
// replayed as received. All upstream connections go back into Mihomo
// through its dedicated listener, so routing stays Mihomo's.
package agent

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lanflow/internal/capture/ca"
	"lanflow/internal/model"
)

// Selector is one set of outbound credentials Mihomo uses to reach the
// agent: the domain selection, or one program selection (M2). The
// credentials tell the agent which selection a connection matched.
type Selector struct {
	User     string `json:"user"`
	Pass     string `json:"pass"`
	Kind     string `json:"kind"` // "domain" | "app"
	Name     string `json:"name"` // the app's name for "app"
	Outbound string `json:"outbound"`
}

// Config configures an agent.
type Config struct {
	Listen    string     `json:"listen"` // "127.0.0.1:0" or a fixed port (restart keeps it)
	Selectors []Selector `json:"selectors"`
	UpAddr    string     `json:"upAddr"` // the return listener lanflow-mitm-up
	UpUser    string     `json:"upUser"`
	UpPass    string     `json:"upPass"`
	Domains   []string   `json:"domains"`
	Bypass    []string   `json:"bypass"`
	Enabled   bool       `json:"enabled"`
	// Controller is the core's controller address (socket / pipe) for the
	// source check and attribution; "" skips both.
	Controller string `json:"controller"`
	SpillDir   string `json:"spillDir"`
	MaxFlows   int    `json:"maxFlows"`
	BodyCap    int64  `json:"bodyCap"`
	MemLimit   int64  `json:"memLimit"`
	MaxConns   int    `json:"maxConns"`
	LoopWindow int    `json:"loopWindowMs"`
	LoopMax    int    `json:"loopMax"`
	// NoLoopGuard turns the breaker off (tests measuring a loop).
	NoLoopGuard bool `json:"noLoopGuard,omitempty"`
}

// Update changes a running agent's selection (POST /config).
type Update struct {
	Enabled   *bool       `json:"enabled,omitempty"`
	Domains   *[]string   `json:"domains,omitempty"`
	Bypass    *[]string   `json:"bypass,omitempty"`
	Selectors *[]Selector `json:"selectors,omitempty"`
	UpAddr    *string     `json:"upAddr,omitempty"`
	UpPass    *string     `json:"upPass,omitempty"`
}

type compiled struct {
	enabled bool
	sel     []Selector
	domains *matcher
	bypass  *matcher
	upAddr  string
	upUser  string
	upPass  string
}

// Agent is a running capture agent.
type Agent struct {
	cfg   atomic.Pointer[compiled]
	root  *ca.Root
	st    *store
	ln    net.Listener
	src   *sourceCheck
	loop  *loopGuard
	pins  *pinTable
	sem   chan struct{}
	roots *x509.CertPool // upstream roots; nil = the system's

	mu     sync.Mutex
	active map[net.Conn]bool // decrypted connections (closed when turned off)
	closed bool
	wg     sync.WaitGroup
	// Accepts counts accepted connections (tests).
	Accepts atomic.Int64
}

// New listens and returns an agent; Serve runs it.
func New(cfg Config, root *ca.Root) (*Agent, error) {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if host, _, err := net.SplitHostPort(cfg.Listen); err != nil || !net.ParseIP(host).IsLoopback() {
		return nil, fmt.Errorf("capture agent listens on loopback only, not %q", cfg.Listen)
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, err
	}
	max := cfg.MaxConns
	if max <= 0 {
		max = 256
	}
	a := &Agent{root: root, ln: ln, pins: newPinTable(), sem: make(chan struct{}, max), active: map[net.Conn]bool{},
		st: newStore(cfg.MaxFlows, cfg.BodyCap, cfg.MemLimit, cfg.SpillDir, 0)}
	a.loop = newLoopGuard(time.Duration(cfg.LoopWindow)*time.Millisecond, cfg.LoopMax)
	if cfg.NoLoopGuard {
		a.loop = nil
	}
	if cfg.Controller != "" {
		a.src = newSourceCheck(cfg.Controller)
	}
	a.cfg.Store(compile(cfg))
	return a, nil
}

func compile(c Config) *compiled {
	return &compiled{enabled: c.Enabled, sel: append([]Selector(nil), c.Selectors...), domains: newMatcher(c.Domains),
		bypass: newMatcher(c.Bypass), upAddr: c.UpAddr, upUser: c.UpUser, upPass: c.UpPass}
}

// SetRoots replaces the upstream trust roots (tests; nil = system).
func (a *Agent) SetRoots(p *x509.CertPool) { a.roots = p }

// SetController points the source check at a controller (tests).
func (a *Agent) SetController(addr string, dial func(ctx context.Context) (net.Conn, error)) {
	a.src = newSourceCheckDial(dial)
}

// Addr is the listening address.
func (a *Agent) Addr() string { return a.ln.Addr().String() }

// Port is the listening port.
func (a *Agent) Port() int { return a.ln.Addr().(*net.TCPAddr).Port }

// Apply changes the selection; turning off closes decrypted connections.
func (a *Agent) Apply(u Update) {
	old := a.cfg.Load()
	c := *old
	if u.Enabled != nil {
		c.enabled = *u.Enabled
	}
	if u.Domains != nil {
		c.domains = newMatcher(*u.Domains)
	}
	if u.Bypass != nil {
		c.bypass = newMatcher(*u.Bypass)
	}
	if u.Selectors != nil {
		c.sel = append([]Selector(nil), (*u.Selectors)...)
	}
	if u.UpAddr != nil {
		c.upAddr = *u.UpAddr
	}
	if u.UpPass != nil {
		c.upPass = *u.UpPass
	}
	a.cfg.Store(&c)
	if old.enabled && !c.enabled {
		a.closeActive()
	}
}

// Enabled reports the switch.
func (a *Agent) Enabled() bool { return a.cfg.Load().enabled }

func (a *Agent) closeActive() {
	a.mu.Lock()
	cs := make([]net.Conn, 0, len(a.active))
	for c := range a.active {
		cs = append(cs, c)
	}
	a.mu.Unlock()
	for _, c := range cs {
		c.Close()
	}
}

// Serve accepts until Close.
func (a *Agent) Serve() error {
	for {
		c, err := a.ln.Accept()
		if err != nil {
			a.mu.Lock()
			closed := a.closed
			a.mu.Unlock()
			if closed {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		a.Accepts.Add(1)
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.handle(c)
		}()
	}
}

// Close stops listening, closes every connection and removes the spill file.
func (a *Agent) Close() {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	a.ln.Close()
	a.closeActive()
	a.st.close()
}

// ---- one connection ----

const (
	connectTimeout = 15 * time.Second
	helloTimeout   = 30 * time.Second
	handshakeLimit = 20 * time.Second
	maxConnectHead = 8 << 10
)

type connectReq struct {
	host string
	port int
	user string
	pass string
}

func readConnect(br *bufio.Reader) (connectReq, error) {
	var cr connectReq
	start, hdr, err := readHead(br)
	if err != nil {
		return cr, err
	}
	n := len(start)
	for _, kv := range hdr {
		n += len(kv.Key) + len(kv.Value)
	}
	if n > maxConnectHead {
		return cr, errors.New("CONNECT head too large")
	}
	parts := strings.Fields(start)
	if len(parts) != 3 || parts[0] != "CONNECT" || !strings.HasPrefix(parts[2], "HTTP/1.") {
		return cr, errors.New("not a CONNECT")
	}
	h, p, err := net.SplitHostPort(parts[1])
	if err != nil {
		return cr, err
	}
	cr.host = strings.ToLower(h)
	cr.port, err = strconv.Atoi(p)
	if err != nil || cr.port <= 0 || cr.port > 65535 {
		return cr, errors.New("bad port")
	}
	if auth := headerValue(hdr, "proxy-authorization"); strings.HasPrefix(auth, "Basic ") {
		if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[6:])); err == nil {
			cr.user, cr.pass, _ = strings.Cut(string(b), ":")
		}
	}
	return cr, nil
}

// auth finds the selector of the credentials in constant time.
func (c *compiled) auth(user, pass string) (Selector, bool) {
	var found Selector
	ok := 0
	for _, s := range c.sel {
		u := subtle.ConstantTimeCompare([]byte(s.User), []byte(user))
		p := subtle.ConstantTimeCompare([]byte(s.Pass), []byte(pass))
		if u&p == 1 && s.Pass != "" {
			found = s
			ok = 1
		}
	}
	return found, ok == 1
}

func (a *Agent) handle(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(connectTimeout))
	br := bufio.NewReaderSize(c, maxHello+4096)
	req, err := readConnect(br)
	if err != nil {
		io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n")
		return
	}
	cfg := a.cfg.Load()
	sel, ok := cfg.auth(req.user, req.pass)
	if !ok {
		io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"lanflow\"\r\nConnection: close\r\n\r\n")
		return
	}
	if a.selfTarget(cfg, req.host, req.port) {
		io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n")
		return
	}
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	c.SetDeadline(time.Now().Add(helloTimeout))
	if _, err := br.Peek(1); err != nil {
		return
	}
	base := model.CaptureFlow{Start: time.Now(), Host: req.host, Port: req.port, Selector: sel.Name}
	if sel.Kind == "app" {
		base.Attribution = "certain"
	}
	target := net.JoinHostPort(req.host, strconv.Itoa(req.port))

	first, _ := br.Peek(1)
	isTLS := first[0] == 0x16
	var ch *clientHello
	if isTLS {
		ch, err = peekHello(br)
		if err != nil {
			ch = nil
		}
	}
	c.SetDeadline(time.Time{})
	if a.loop != nil && (a.loop.isOwn(br, isTLS && ch != nil) || a.loop.tooFast(target)) {
		a.refuseLoop(base)
		return
	}
	// The kernel's view of the connection: source, process and the name
	// it was asked for (a CONNECT to an already resolved destination
	// carries only the IP; the return leg and the ECH check use the name).
	local := a.locate(sel, req, &base)
	if base.Host != req.host {
		req.host = base.Host
	}
	switch {
	case isTLS && ch == nil:
		base.Proto = "tls"
		a.passthrough(c, br, cfg, req, base, "无法解析 ClientHello", model.FlowPassthrough)
	case isTLS:
		base.Proto = "tls"
		why, flags := a.decide(cfg, sel, req, ch)
		if why == "" && !local {
			why, flags = "来源不是本机或无法确认", []string{model.FlowPassthrough, model.FlowNotLocal}
		}
		if why != "" {
			a.passthrough(c, br, cfg, req, base, why, flags...)
			return
		}
		if sel.Kind != "app" {
			base.Selector = cfg.domains.match(ch.sni) // the pattern that selected it
		}
		a.mitm(c, br, cfg, sel, req, ch, base)
	case !looksHTTP(br):
		base.Proto = "tcp"
		a.passthrough(c, br, cfg, req, base, "非 HTTP", model.FlowPassthrough)
	default:
		// plaintext HTTP: recorded when selected, forwarded unchanged
		base.Proto = "tcp"
		why, flags := a.decidePlain(cfg, sel, req)
		if why == "" && !local {
			why, flags = "来源不是本机或无法确认", []string{model.FlowPassthrough, model.FlowNotLocal}
		}
		if why != "" {
			a.passthrough(c, br, cfg, req, base, why, flags...)
			return
		}
		if sel.Kind != "app" {
			base.Selector = cfg.domains.match(req.host)
		}
		a.plain(c, br, cfg, sel, req, base)
	}
}

func looksHTTP(br *bufio.Reader) bool {
	b, _ := br.Peek(min(br.Buffered(), 8))
	for _, m := range []string{"GET ", "POST ", "PUT ", "HEAD ", "DELETE ", "OPTIONS ", "PATCH ", "TRACE "} {
		if strings.HasPrefix(string(b), m) || (len(b) < len(m) && strings.HasPrefix(m, string(b)) && len(b) >= 3) {
			return true
		}
	}
	return false
}

// selfTarget refuses CONNECTs aimed at the agent or the return listener
// themselves (loopback may otherwise be legitimate: hosts entries).
func (a *Agent) selfTarget(cfg *compiled, host string, port int) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		if host != "localhost" {
			return false
		}
	} else if !ip.IsLoopback() && !ip.IsUnspecified() {
		return false
	}
	if port == a.Port() {
		return true
	}
	if _, p, err := net.SplitHostPort(cfg.upAddr); err == nil && strconv.Itoa(port) == p {
		return true
	}
	return false
}

// decide returns "" to decrypt, or why not with the flags to record.
func (a *Agent) decide(cfg *compiled, sel Selector, req connectReq, ch *clientHello) (string, []string) {
	pass := model.FlowPassthrough
	if !cfg.enabled {
		return "解密已关闭", []string{pass}
	}
	if a.root == nil {
		return "没有根证书", []string{pass}
	}
	sni := strings.ToLower(strings.TrimSuffix(ch.sni, "."))
	if sni == "" || isIP(sni) {
		return "没有 SNI", []string{pass}
	}
	hostIsName := !isIP(req.host)
	// ECH: the outer SNI of a real ECH ClientHello is the public name, not
	// the target. Chrome sends a GREASE ECH extension to every server, so
	// the extension alone means nothing (docs/capture.md §5.4).
	if ch.ech && hostIsName && sni != req.host {
		return "ECH 加密的 ClientHello", []string{pass, model.FlowECH}
	}
	if hostIsName && sni != req.host {
		return "SNI 与目标主机不一致", []string{pass}
	}
	switch sel.Kind {
	case "app":
	default:
		if cfg.domains.match(sni) == "" {
			return "不在选择范围内", []string{pass}
		}
	}
	if singleLabel(sni) || cfg.bypass.match(sni) != "" || (!hostIsName && cfg.bypass.match(req.host) != "") {
		return "在跳过列表中", []string{pass}
	}
	if a.pins.skipped(sel.User + "|" + sni) {
		return "证书固定，暂时跳过", []string{pass, model.FlowPinned}
	}
	return "", nil
}

func (a *Agent) decidePlain(cfg *compiled, sel Selector, req connectReq) (string, []string) {
	pass := model.FlowPassthrough
	if !cfg.enabled {
		return "解密已关闭", []string{pass}
	}
	if sel.Kind != "app" && cfg.domains.match(req.host) == "" {
		return "不在选择范围内", []string{pass}
	}
	if singleLabel(req.host) || cfg.bypass.match(req.host) != "" {
		return "在跳过列表中", []string{pass}
	}
	return "", nil
}

func (a *Agent) refuseLoop(base model.CaptureFlow) {
	base.Flags = []string{model.FlowLoop}
	base.Error = "检测到回环，已拒绝"
	base.Done = true
	a.st.add(base)
}

// dialUp connects to target through the return listener.
func (a *Agent) dialUp(ctx context.Context, cfg *compiled, host string, port int) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	c, err := d.DialContext(ctx, "tcp", cfg.upAddr)
	if err != nil {
		return nil, fmt.Errorf("连接回流入口失败：%w", err)
	}
	target := net.JoinHostPort(host, strconv.Itoa(port))
	auth := base64.StdEncoding.EncodeToString([]byte(cfg.upUser + ":" + cfg.upPass))
	c.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("回流入口无响应：%w", err)
	}
	if resp.StatusCode != 200 {
		c.Close()
		return nil, fmt.Errorf("回流入口拒绝：%s", resp.Status)
	}
	c.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufConn{Conn: c, r: br}, nil
	}
	return c, nil
}

// bufConn reads through a bufio.Reader that may hold bytes already.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// CloseWrite half-closes when the underlying connection can.
func (b *bufConn) CloseWrite() error {
	if cw, ok := b.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// passthrough forwards the connection unchanged (the ClientHello, still
// unread in br, goes first) and records it at connection level.
func (a *Agent) passthrough(c net.Conn, br *bufio.Reader, cfg *compiled, req connectReq, base model.CaptureFlow, why string, flags ...string) {
	base.Flags = append(base.Flags, flags...)
	base.Error = why
	rec := a.st.add(base)
	up, err := a.dialUp(context.Background(), cfg, req.host, req.port)
	if err != nil {
		a.st.update(rec, func(d *model.CaptureFlowDetail) {
			d.Flow.Error = why + "；" + err.Error()
			d.Flow.Done = true
		})
		return
	}
	defer up.Close()
	if a.loop != nil {
		if b, _ := br.Peek(min(br.Buffered(), loopPrefix)); len(b) > 0 {
			a.loop.markOwn(b)
		}
	}
	upN, downN := splice(c, br, up, up)
	a.st.update(rec, func(d *model.CaptureFlowDetail) {
		d.Flow.ReqSize, d.Flow.RespSize = upN, downN
		d.Flow.Done = true
		d.Flow.DurationMs = time.Since(d.Flow.Start).Milliseconds()
	})
}

// splice copies both ways until both directions end (or one fails) and
// returns the bytes sent upstream and downstream.
func splice(client net.Conn, clientR io.Reader, up net.Conn, upR io.Reader) (int64, int64) {
	var upN, downN int64
	done := make(chan struct{}, 2)
	go func() {
		upN, _ = io.Copy(up, clientR)
		closeWrite(up)
		done <- struct{}{}
	}()
	go func() {
		downN, _ = io.Copy(client, upR)
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	// give the other direction a moment to drain, then cut
	t := time.AfterFunc(30*time.Second, func() { client.Close(); up.Close() })
	<-done
	t.Stop()
	return upN, downN
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		if cw.CloseWrite() == nil {
			return
		}
	}
	c.Close()
}

func (a *Agent) track(cs ...net.Conn) func() {
	a.mu.Lock()
	for _, c := range cs {
		a.active[c] = true
	}
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		for _, c := range cs {
			delete(a.active, c)
		}
		a.mu.Unlock()
	}
}

// locate runs the source check and fills the attribution; ok is false
// when the connection does not come from this machine (or cannot be
// shown to).
func (a *Agent) locate(sel Selector, req connectReq, base *model.CaptureFlow) bool {
	if a.src == nil {
		if base.Attribution == "" {
			base.Attribution = "unknown"
		}
		return true
	}
	res := a.src.find(sel.Outbound, req.host, req.port)
	if !res.found {
		base.Attribution = "unknown"
		return false
	}
	if res.host != "" && isIP(req.host) {
		base.Host = res.host
	}
	base.Process, base.ProcessPath = res.process, res.path
	if base.Attribution == "" {
		base.Attribution = res.attribution
	}
	return res.local
}

// mitm decrypts: upstream handshake first, then the client's with a leaf
// for the SNI and the ALPN the upstream chose; then the plaintext is
// copied both ways and parsed on the side.
func (a *Agent) mitm(c net.Conn, br *bufio.Reader, cfg *compiled, sel Selector, req connectReq, ch *clientHello, base model.CaptureFlow) {
	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	default:
		a.passthrough(c, br, cfg, req, base, "并发解密连接已达上限", model.FlowPassthrough)
		return
	}
	t0 := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), handshakeLimit)
	defer cancel()
	raw, err := a.dialUp(ctx, cfg, req.host, req.port)
	if err != nil {
		base.Error, base.Done = err.Error(), true
		a.st.add(base)
		return
	}
	dialMs := time.Since(t0).Milliseconds()
	var upConn net.Conn = raw
	if a.loop != nil {
		upConn = &firstWrite{Conn: raw, g: a.loop}
	}
	clientCert := false
	ucfg := &tls.Config{
		ServerName: ch.sni,
		NextProtos: ch.alpn,
		RootCAs:    a.roots,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			clientCert = true
			return nil, errors.New("upstream requests a client certificate")
		},
		VerifyConnection: func(cs tls.ConnectionState) error {
			for _, chain := range cs.VerifiedChains {
				if len(chain) > 0 && ca.IsOurs(chain[len(chain)-1]) {
					return errors.New("upstream chain ends at a Lanflow root")
				}
			}
			return nil
		},
	}
	up := tls.Client(upConn, ucfg)
	if err := up.HandshakeContext(ctx); err != nil {
		raw.Close()
		why, flag := "上游握手失败，已透传", model.FlowPassthrough
		var cv *tls.CertificateVerificationError
		switch {
		case clientCert:
			why, flag = "上游要求客户端证书，已透传", model.FlowClientCert
		case errors.As(err, &cv) || strings.Contains(err.Error(), "Lanflow root"):
			why, flag = "上游证书无效，已透传", model.FlowUpstreamBad
		}
		a.passthrough(c, br, cfg, req, base, why, model.FlowPassthrough, flag)
		return
	}
	upTLSMs := time.Since(t0).Milliseconds()
	ust := up.ConnectionState()
	leaf, err := a.root.Leaf(ch.sni)
	if err != nil {
		up.Close()
		a.passthrough(c, br, cfg, req, base, "签发证书失败："+err.Error(), model.FlowPassthrough)
		return
	}
	scfg := &tls.Config{Certificates: []tls.Certificate{*leaf}, MinVersion: tls.VersionTLS12}
	if ust.NegotiatedProtocol != "" {
		scfg.NextProtos = []string{ust.NegotiatedProtocol}
	}
	down := tls.Server(&bufConn{Conn: c, r: br}, scfg)
	down.SetDeadline(time.Now().Add(handshakeLimit))
	if err := down.Handshake(); err != nil {
		up.Close()
		msg := err.Error()
		base.Flags = append(base.Flags, model.FlowPassthrough)
		base.Error = "客户端未接受证书：" + msg
		if isCertAlert(msg) {
			if a.pins.fail(sel.User + "|" + strings.ToLower(ch.sni)) {
				base.Flags = append(base.Flags, model.FlowPinned)
				base.Error = "证书固定：此主机暂时跳过解密"
			}
		}
		base.Done = true
		a.st.add(base)
		return
	}
	down.SetDeadline(time.Time{})
	dst := down.ConnectionState()
	if dst.NegotiatedProtocol != ust.NegotiatedProtocol {
		up.Close()
		base.Error, base.Done = "两侧 ALPN 不一致", true
		a.st.add(base)
		return
	}
	defer a.track(c, raw)()
	if !a.cfg.Load().enabled { // turned off meanwhile
		up.Close()
		return
	}

	base.Flags = append(base.Flags, model.FlowDecrypted)
	cr := &connRec{st: a.st, base: base, start: base.Start,
		tls: model.CaptureTLS{SNI: ch.sni, Version: tls.VersionName(ust.Version), Cipher: tls.CipherSuiteName(ust.CipherSuite),
			ALPN: ust.NegotiatedProtocol, ClientALPN: ch.alpn, ECH: ch.ech},
		timing: model.CaptureTiming{UpstreamDialMs: dialMs, UpstreamTLSMs: upTLSMs, ClientTLSMs: time.Since(t0).Milliseconds()}}
	for _, cert := range ust.PeerCertificates {
		cr.tls.UpstreamCert = append(cr.tls.UpstreamCert, ca.Fingerprint(cert.Raw))
	}
	a.relay(down, up, cr, ust.NegotiatedProtocol, "https", req.host)
}

func isCertAlert(msg string) bool {
	for _, s := range []string{"bad certificate", "unknown certificate", "unknown certificate authority", "access denied", "certificate unknown"} {
		if strings.Contains(msg, "remote error: tls: "+s) {
			return true
		}
	}
	return false
}

// plain records a plaintext HTTP connection while forwarding it.
func (a *Agent) plain(c net.Conn, br *bufio.Reader, cfg *compiled, sel Selector, req connectReq, base model.CaptureFlow) {
	up, err := a.dialUp(context.Background(), cfg, req.host, req.port)
	if err != nil {
		base.Error, base.Done = err.Error(), true
		a.st.add(base)
		return
	}
	defer a.track(c, up)()
	if a.loop != nil {
		up = &firstWrite{Conn: up, g: a.loop}
	}
	base.Flags = append(base.Flags, model.FlowDecrypted)
	cr := &connRec{st: a.st, base: base, start: base.Start}
	a.relay(&bufConn{Conn: c, r: br}, up, cr, "http/1.1", "http", req.host)
}

const pipeMax = 2 << 20

// relay copies client<->up byte for byte with copies to the parser.
func (a *Agent) relay(client, up net.Conn, cr *connRec, proto, scheme, host string) {
	defer up.Close()
	defer client.Close()
	reqPipe, respPipe := newLossyPipe(pipeMax), newLossyPipe(pipeMax)
	var pw sync.WaitGroup
	switch proto {
	case "h2":
		cr.base.Proto = "h2"
		p := newH2Parser(cr)
		pw.Add(2)
		go func() { defer pw.Done(); p.run(reqPipe, true) }()
		go func() { defer pw.Done(); p.run(respPipe, false) }()
	case "", "http/1.1", "http/1.0":
		cr.base.Proto = "h1"
		p := newH1Parser(cr, scheme, host)
		pw.Add(2)
		go func() { defer pw.Done(); p.requests(reqPipe) }()
		go func() { defer pw.Done(); p.responses(respPipe); p.drain("") }()
	default:
		cr.base.Proto = proto
		cr.base.Flags = append(cr.base.Flags, model.FlowUnknown)
		rec := cr.newFlow("", scheme, "", "", nil)
		reqPipe.Abort()
		respPipe.Abort()
		upN, downN := splice(client, client, up, up)
		cr.st.update(rec, func(d *model.CaptureFlowDetail) {
			d.Flow.ReqSize, d.Flow.RespSize, d.Flow.Done = upN, downN, true
			d.Flow.DurationMs = time.Since(d.Flow.Start).Milliseconds()
		})
		return
	}
	splice(client, teeReader{r: client, w: reqPipe}, up, teeReader{r: up, w: respPipe})
	reqPipe.Close()
	respPipe.Close()
	pw.Wait()
}

// ---- reads for the control API ----

// Store access for ctl.go and tests.
func (a *Agent) Flows(since, epoch int64) ([]model.CaptureFlow, int64, int64, int64) {
	return a.st.since(since, epoch)
}

func (a *Agent) Flow(id int64) (model.CaptureFlowDetail, bool) { return a.st.detail(id) }

func (a *Agent) Clear() { a.st.clear() }

func (a *Agent) Count() int { return a.st.count() }

// OnChange sets a callback for record changes.
func (a *Agent) OnChange(fn func()) { a.st.onChange = fn }

// Pinned lists the (selector|SNI) pairs skipped for pinning.
func (a *Agent) Pinned() []string { return a.pins.list() }
