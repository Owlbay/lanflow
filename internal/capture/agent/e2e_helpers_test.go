package agent

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/capture/ca"
	"lanflow/internal/controller"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// ---- upstream test PKI ----

type testPKI struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Upstream Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	p := x509.NewCertPool()
	p.AddCert(c)
	return &testPKI{cert: c, key: k, pool: p}
}

func (p *testPKI) leaf(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), DNSNames: names,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.cert, &k.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k, Leaf: l}
}

// selfSigned is a certificate no root vouches for.
func selfSigned(t *testing.T, name string) tls.Certificate {
	p := newTestPKI(t)
	c := p.leaf(t, name)
	return c
}

// upstream is an HTTPS (h2 + h1) echo server that also keeps the raw
// bytes each connection received.
type upstream struct {
	ln   net.Listener
	port int
	cert tls.Certificate
	mu   sync.Mutex
	raw  [][]byte
}

func startUpstream(t *testing.T, cert tls.Certificate, ech []tls.EncryptedClientHelloKey) *upstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &upstream{ln: ln, port: ln.Addr().(*net.TCPAddr).Port, cert: cert}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}, EncryptedClientHelloKeys: ech}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Proto", r.Proto)
		json.NewEncoder(w).Encode(map[string]any{"path": r.URL.Path, "body": string(body), "ech": r.TLS != nil && r.TLS.ECHAccepted})
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
		}
	})
	srv := &http.Server{Handler: mux, TLSConfig: cfg}
	rawLn := &recordingListener{Listener: ln, u: u}
	go srv.ServeTLS(rawLn, "", "")
	t.Cleanup(func() { srv.Close() })
	return u
}

type recordingListener struct {
	net.Listener
	u *upstream
}

func (l *recordingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.u.mu.Lock()
	l.u.raw = append(l.u.raw, nil)
	i := len(l.u.raw) - 1
	l.u.mu.Unlock()
	return &recordingConn{Conn: c, u: l.u, i: i}, nil
}

type recordingConn struct {
	net.Conn
	u *upstream
	i int
}

func (c *recordingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.u.mu.Lock()
		c.u.raw[c.i] = append(c.u.raw[c.i], b[:n]...)
		c.u.mu.Unlock()
	}
	return n, err
}

func (u *upstream) conns() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.raw)
}

// plainEcho is an HTTP/1 server on TCP that records the request bytes it
// sees in plaintext (for the byte-identity check behind TLS termination).
type plainTLS struct {
	port int
	mu   sync.Mutex
	got  []byte
}

func startPlainTLS(t *testing.T, cert tls.Certificate) *plainTLS {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	p := &plainTLS{port: ln.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				var buf []byte
				for {
					l, err := br.ReadString('\n')
					buf = append(buf, l...)
					if err != nil || l == "\r\n" {
						break
					}
				}
				p.mu.Lock()
				p.got = buf
				p.mu.Unlock()
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return p
}

// ---- Mihomo ----

func mihomoBin(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("LANFLOW_TEST_MIHOMO"); v != "" {
		return v
	}
	p, _ := filepath.Abs(filepath.Join("..", "..", "..", "bin", "mihomo"))
	if _, err := os.Stat(p); err != nil {
		t.Skipf("bin/mihomo not present (scripts/fetch-core.sh): %v", err)
	}
	return p
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type kernel struct {
	t     *testing.T
	bin   string
	dir   string
	sock  string
	cmd   *exec.Cmd
	ctrl  *controller.Client
	mixed int
	log   *strings.Builder
	logMu *sync.Mutex
}

type logWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// startKernel runs bin/mihomo on cfg (listening on loopback only) with a
// unix-socket controller in a temporary directory.
func startKernel(t *testing.T, cfg []byte, mixed int, host string) *kernel {
	t.Helper()
	bin := mihomoBin(t)
	dir, err := os.MkdirTemp("", "lfcap")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	k := &kernel{t: t, bin: bin, dir: dir, sock: filepath.Join(dir, "c.sock"), mixed: mixed, log: &strings.Builder{}, logMu: &sync.Mutex{}}
	f := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(f, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	k.cmd = exec.Command(bin, "-d", dir, "-f", f, "-ext-ctl-unix", k.sock)
	k.cmd.Stdout = logWriter{k.logMu, k.log}
	k.cmd.Stderr = logWriter{k.logMu, k.log}
	if err := k.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		k.cmd.Process.Kill()
		k.cmd.Wait()
		if t.Failed() {
			k.logMu.Lock()
			t.Logf("mihomo log:\n%s", k.log.String())
			k.logMu.Unlock()
		}
	})
	k.ctrl = controller.New(k.dial)
	deadline := time.Now().Add(15 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := k.ctrl.Version(ctx)
		cancel()
		if err == nil {
			if c, err := net.DialTimeout("tcp", net.JoinHostPort(host, itoa(mixed)), time.Second); err == nil {
				c.Close()
				// inline rule providers load just after the listeners
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, err := k.ctrl.RuleProviders(ctx)
				cancel()
				if err == nil {
					time.Sleep(100 * time.Millisecond)
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("mihomo did not start: %v\n%s", err, k.log.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return k
}

func (k *kernel) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", k.sock)
}

// reload hot-reloads cfg (PUT /configs?force=true).
func (k *kernel) reload(cfg []byte) {
	k.t.Helper()
	f := filepath.Join(k.dir, "config.yaml")
	if err := os.WriteFile(f, cfg, 0o600); err != nil {
		k.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := k.ctrl.ReloadConfig(ctx, f); err != nil {
		k.t.Fatalf("reload: %v", err)
	}
}

func (k *kernel) connections() []controller.Connection {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cs, err := k.ctrl.Connections(ctx)
	if err != nil {
		k.t.Fatal(err)
	}
	return cs.Connections
}

// ---- the whole setup ----

const testSub = `proxies:
  - {name: dummy, type: socks5, server: 127.0.0.1, port: 9}
rules:
  - MATCH,DIRECT
`

type rig struct {
	t      *testing.T
	pki    *testPKI
	root   *ca.Root
	agent  *Agent
	k      *kernel
	in     profile.Input
	mixed  int
	clPool *x509.CertPool
	names  []string
}

type rigOpts struct {
	domains   []string
	apps      []profile.CaptureApp
	override  string // global override text (hosts are added)
	bypass    []string
	noLoop    bool
	allowLAN  string // bind the mixed port to this address instead of loopback
	mutate    func(cfg []byte, outbound string) []byte
	noCtrl    bool
	hostnames []string
}

func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	bin := mihomoBin(t)
	_ = bin
	r := &rig{t: t, pki: newTestPKI(t)}
	root, _, err := ca.Generate(&ca.MemKeys{}, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r.root = root
	r.clPool = x509.NewCertPool()
	r.clPool.AddCert(root.Cert)
	r.clPool.AddCert(r.pki.cert)

	outbound := "LANFLOW-MITM-" + randHex(4)
	sels := []Selector{{User: "lanflow", Pass: randHex(16), Kind: "domain", Name: "域名", Outbound: outbound}}
	for i := range o.apps {
		o.apps[i].Outbound = fmt.Sprintf("%s-A%d", outbound, i)
		o.apps[i].User = fmt.Sprintf("app%d", i)
		o.apps[i].Pass = randHex(16)
		sels = append(sels, Selector{User: o.apps[i].User, Pass: o.apps[i].Pass, Kind: "app", Name: fmt.Sprintf("app%d", i), Outbound: o.apps[i].Outbound})
	}
	upPort := freePort(t)
	upPass := randHex(16)
	ctrlDir, _ := os.MkdirTemp("", "lfspill")
	t.Cleanup(func() { os.RemoveAll(ctrlDir) })
	a, err := New(Config{Selectors: sels, UpAddr: fmt.Sprintf("127.0.0.1:%d", upPort), UpUser: "lanflow", UpPass: upPass,
		Domains: o.domains, Bypass: o.bypass, Enabled: true, SpillDir: ctrlDir, NoLoopGuard: o.noLoop}, root)
	if err != nil {
		t.Fatal(err)
	}
	a.SetRoots(r.pki.pool)
	go a.Serve()
	t.Cleanup(a.Close)
	r.agent = a

	r.mixed = freePort(t)
	hosts := "hosts:\n"
	names := append([]string{"sel.test", "other.test", "bad.test", "ech.test", "public.test", "app.test", "raw.test", "lan.test"}, o.hostnames...)
	for _, n := range names {
		hosts += fmt.Sprintf("  %s: 127.0.0.1\n", n)
	}
	r.in = profile.Input{
		Subscription:   []byte(testSub),
		GlobalOverride: hosts + "find-process-mode: strict\n" + o.override,
		Managed:        profile.Managed{MixedPort: r.mixed, Mode: model.ModeRule, LogLevel: "info", IPv6: true},
		Capture: profile.Capture{AgentPort: a.Port(), UpPort: upPort, UpUser: "lanflow", UpPass: upPass,
			Domains: o.domains, Domain: profile.CaptureSelector{Outbound: outbound, User: "lanflow", Pass: sels[0].Pass},
			Apps: o.apps, Bypass: o.bypass, IPv6: true, Ports: "80/443/8443/1024-65535"},
	}
	cfg := r.compose(true)
	if o.allowLAN != "" {
		cfg = append(cfg, []byte(fmt.Sprintf("bind-address: %s\nlan-allowed-ips: [%s/32]\n", o.allowLAN, o.allowLAN))...)
		cfg = []byte(strings.Replace(string(cfg), "allow-lan: false", "allow-lan: true", 1))
	}
	if o.mutate != nil {
		cfg = o.mutate(cfg, outbound)
	}
	host := "127.0.0.1"
	if o.allowLAN != "" {
		host = o.allowLAN
	}
	r.k = startKernel(t, cfg, r.mixed, host)
	if !o.noCtrl {
		a.SetController("", r.k.dial)
	}
	return r
}

func (r *rig) outbound() string { return r.in.Capture.Domain.Outbound }

// compose returns the config with or without the capture layer.
func (r *rig) compose(capture bool) []byte {
	r.t.Helper()
	in := r.in
	if !capture {
		in.Capture = profile.Capture{}
	}
	out, err := profile.Compose(in)
	if err != nil {
		r.t.Fatal(err)
	}
	return out.YAML
}

// client returns an HTTP client through the mixed port, trusting the
// Lanflow root and the upstream CA (and extra).
func (r *rig) client(h2 bool, extra ...*x509.Certificate) *http.Client {
	pool := r.clPool.Clone()
	for _, c := range extra {
		pool.AddCert(c)
	}
	px, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", r.mixed))
	tr := &http.Transport{Proxy: http.ProxyURL(px), TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: h2, DisableCompression: true}
	if !h2 {
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second}
}

// waitFlow waits for a finished record matching fn.
func (r *rig) waitFlow(fn func(model.CaptureFlow) bool) (model.CaptureFlow, bool) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fs, _, _, _ := r.agent.Flows(0, 0)
		for _, f := range fs {
			if f.Done && fn(f) {
				return f, true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return model.CaptureFlow{}, false
}

func (r *rig) dumpFlows() string {
	fs, _, _, _ := r.agent.Flows(0, 0)
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "  #%d %s %s:%d %s %s %d flags=%v sel=%q proc=%q attr=%s err=%q done=%v\n", f.ID, f.Proto, f.Host, f.Port, f.Method, f.Path, f.Status, f.Flags, f.Selector, f.Process, f.Attribution, f.Error, f.Done)
	}
	return b.String()
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func issuedByLanflow(c *x509.Certificate) bool { return ca.IsOurs(c) }

// ---- ECH ----

// echKeys builds a single ECH configuration (X25519, HKDF-SHA256,
// AES-128-GCM) for publicName and returns the config list for clients and
// the server key.
func echKeys(t *testing.T, publicName string) ([]byte, []tls.EncryptedClientHelloKey) {
	t.Helper()
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PublicKey().Bytes()
	var c []byte
	c = append(c, 7)                             // config_id
	c = binary.BigEndian.AppendUint16(c, 0x0020) // DHKEM(X25519, HKDF-SHA256)
	c = binary.BigEndian.AppendUint16(c, uint16(len(pub)))
	c = append(c, pub...)
	c = binary.BigEndian.AppendUint16(c, 4)      // cipher suites length
	c = binary.BigEndian.AppendUint16(c, 0x0001) // HKDF-SHA256
	c = binary.BigEndian.AppendUint16(c, 0x0001) // AES-128-GCM
	c = append(c, 0)                             // maximum_name_length
	c = append(c, byte(len(publicName)))
	c = append(c, publicName...)
	c = binary.BigEndian.AppendUint16(c, 0) // extensions
	var cfg []byte
	cfg = binary.BigEndian.AppendUint16(cfg, 0xfe0d)
	cfg = binary.BigEndian.AppendUint16(cfg, uint16(len(c)))
	cfg = append(cfg, c...)
	list := binary.BigEndian.AppendUint16(nil, uint16(len(cfg)))
	list = append(list, cfg...)
	return list, []tls.EncryptedClientHelloKey{{Config: cfg, PrivateKey: priv.Bytes(), SendAsRetry: true}}
}

func itoa(i int) string { return strconv.Itoa(i) }

type urlT = url.URL

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }
