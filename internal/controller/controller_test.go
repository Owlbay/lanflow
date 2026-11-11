package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeMihomo is an HTTP+WebSocket server on a unix socket that mimics the
// Mihomo external controller closely enough for the client to be tested.
type fakeMihomo struct {
	t      *testing.T
	mux    *http.ServeMux
	srv    *http.Server
	sock   string
	mu     sync.Mutex
	reqs   []recorded
	wsConn atomic.Int32 // number of WebSocket sessions accepted
}

type recorded struct {
	Method  string
	RawPath string // EscapedPath, as the client sent it
	Query   string
	Body    string
}

func newFake(t *testing.T) *fakeMihomo {
	t.Helper()
	// macOS limits sun_path to ~104 bytes; t.TempDir() can be long, so use
	// a short name under os.TempDir().
	dir, err := os.MkdirTemp("", "lf-ctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeMihomo{t: t, mux: http.NewServeMux(), sock: filepath.Join(dir, "s.sock")}
	f.srv = &http.Server{Handler: f}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	go f.srv.Serve(ln)
	t.Cleanup(func() { f.srv.Close() })
	return f
}

func (f *fakeMihomo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{Method: r.Method, RawPath: r.URL.EscapedPath(), Query: r.URL.RawQuery, Body: string(body)})
	f.mu.Unlock()
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	f.mux.ServeHTTP(w, r)
}

func (f *fakeMihomo) last() recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeMihomo) client() *Client {
	return New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", f.sock)
	})
}

// jsonOK writes v as JSON like chi/render does.
func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// mihomoErr writes Mihomo's {"message": ...} error body.
func mihomoErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

// wsServe upgrades and runs fn; mimics Mihomo's text-frame streams.
func (f *fakeMihomo) wsServe(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, send func(v any) error)) {
	if r.Header.Get("Upgrade") != "websocket" {
		// Mihomo answers plain GETs with chunked JSON lines; we don't
		// need that path, make sure the client never takes it.
		f.t.Errorf("non-websocket request to %s", r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		f.t.Logf("accept: %v", err)
		return
	}
	f.wsConn.Add(1)
	defer c.CloseNow()
	ctx := r.Context()
	fn(ctx, func(v any) error {
		b, _ := json.Marshal(v)
		return c.Write(ctx, websocket.MessageText, append(b, '\n'))
	})
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

const chineseGroup = "🇭🇰 香港/节点 A%"

// escapedChineseGroup is what must appear on the wire.
const escapedChineseGroup = "%F0%9F%87%AD%F0%9F%87%B0%20%E9%A6%99%E6%B8%AF%2F%E8%8A%82%E7%82%B9%20A%25"

func TestVersionAndConfigs(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{"meta": true, "version": "v1.19.11"})
	})
	f.mux.HandleFunc("GET /configs", func(w http.ResponseWriter, r *http.Request) {
		// Shape of config.General as rendered by GET /configs.
		io.WriteString(w, `{"port":0,"socks-port":0,"redir-port":0,"tproxy-port":0,"mixed-port":7890,
		"tun":{"enable":true,"device":"utun5","stack":"gvisor","dns-hijack":["any:53"],"auto-route":true,"auto-detect-interface":true},
		"allow-lan":true,"bind-address":"*","mode":"rule","unified-delay":false,"log-level":"warning","ipv6":true,
		"interface-name":"","routing-mark":0,"sniffing":true}`)
	})
	c := f.client()
	v, err := c.Version(ctxT(t))
	if err != nil || v != "v1.19.11" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	cfg, err := c.Configs(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "rule" || cfg.MixedPort != 7890 || !cfg.AllowLan || cfg.LogLevel != "warning" || !cfg.Tun.Enable || cfg.Tun.Stack != "gvisor" {
		t.Fatalf("Configs = %+v", cfg)
	}
}

func TestPatchReloadGeo(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("PATCH /configs", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	f.mux.HandleFunc("PUT /configs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("force") != "true" {
			mihomoErr(w, 400, "missing force")
			return
		}
		var req struct{ Path string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !filepath.IsAbs(req.Path) {
			mihomoErr(w, 400, "path is not a absolute path")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("POST /configs/geo", func(w http.ResponseWriter, r *http.Request) {
		mihomoErr(w, 500, "download failed")
	})
	c := f.client()

	mode, lan := "global", false
	if err := c.PatchConfigs(ctxT(t), ConfigPatch{Mode: &mode, AllowLan: &lan}); err != nil {
		t.Fatal(err)
	}
	if got := f.last(); got.Method != "PATCH" || got.RawPath != "/configs" || got.Body != `{"mode":"global","allow-lan":false}` {
		t.Fatalf("PATCH recorded %+v", got)
	}

	cfg := filepath.Join(t.TempDir(), "config.yaml") // absolute on every platform
	if err := c.ReloadConfig(ctxT(t), cfg); err != nil {
		t.Fatal(err)
	}
	wantBody, _ := json.Marshal(map[string]string{"path": cfg})
	if got := f.last(); got.Method != "PUT" || got.Query != "force=true" || got.Body != string(wantBody) {
		t.Fatalf("PUT recorded %+v", got)
	}
	err := c.ReloadConfig(ctxT(t), "relative.yaml")
	if err == nil || err.Error() != "重载配置失败：path is not a absolute path" {
		t.Fatalf("relative reload err = %v", err)
	}

	err = c.UpdateGeo(ctxT(t))
	var ce *Error
	if !errors.As(err, &ce) || ce.Status != 500 || err.Error() != "更新 Geo 数据失败：download failed" {
		t.Fatalf("UpdateGeo err = %v", err)
	}
	if got := f.last(); got.Method != "POST" || got.RawPath != "/configs/geo" {
		t.Fatalf("geo recorded %+v", got)
	}
}

func TestProxiesSelectDelay(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("GET /proxies", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"proxies":{
		  "DIRECT":{"alive":true,"extra":{},"history":[],"name":"DIRECT","type":"Direct","udp":true,"uot":false,"xudp":false,"tfo":false,"mptcp":false,"smux":false,"interface":"","routing-mark":0,"provider-name":"","dialer-proxy":""},
		  "`+chineseGroup+`":{"alive":true,"all":["节点1","节点2"],"extra":{},"hidden":false,"history":[],"icon":"","name":"`+chineseGroup+`","now":"节点1","testUrl":"https://www.gstatic.com/generate_204","type":"Selector","udp":true,"uot":false,"xudp":false,"tfo":false,"mptcp":false,"smux":false,"interface":"","routing-mark":0,"provider-name":"","dialer-proxy":""},
		  "节点1":{"alive":true,"extra":{},"history":[{"time":"2025-01-02T03:04:05.123Z","delay":123}],"name":"节点1","type":"Shadowsocks","udp":true,"uot":false,"xudp":false,"tfo":false,"mptcp":false,"smux":false,"interface":"","routing-mark":0,"provider-name":"机场A","dialer-proxy":""}
		}}`)
	})
	f.mux.HandleFunc("PUT /proxies/{name}", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.PathValue("name") != chineseGroup {
			mihomoErr(w, 404, "Resource not found")
			return
		}
		if req.Name == "bad" {
			mihomoErr(w, 400, "Selector update error: proxy not exist")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("GET /proxies/{name}/delay", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("url") != "https://www.gstatic.com/generate_204" || q.Get("timeout") != "3000" {
			mihomoErr(w, 400, "Body invalid")
			return
		}
		switch r.PathValue("name") {
		case "节点1":
			jsonOK(w, map[string]int{"delay": 88})
		case "slow":
			mihomoErr(w, 504, "Timeout")
		default:
			mihomoErr(w, 503, "An error occurred in the delay test")
		}
	})
	f.mux.HandleFunc("GET /group/{name}/delay", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != chineseGroup || r.URL.Query().Get("timeout") != "5000" {
			mihomoErr(w, 404, "Resource not found")
			return
		}
		jsonOK(w, map[string]int{"节点1": 100, "节点2": 250})
	})
	c := f.client()

	ps, err := c.Proxies(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	g := ps[chineseGroup]
	if g.Type != "Selector" || g.Now != "节点1" || len(g.All) != 2 || !g.Alive {
		t.Fatalf("group = %+v", g)
	}
	n := ps["节点1"]
	if n.Provider != "机场A" || len(n.History) != 1 || n.History[0].Delay != 123 || n.History[0].Time.UTC().Year() != 2025 {
		t.Fatalf("node = %+v", n)
	}

	if err := c.SelectProxy(ctxT(t), chineseGroup, "节点2"); err != nil {
		t.Fatal(err)
	}
	if got := f.last(); got.RawPath != "/proxies/"+escapedChineseGroup || got.Body != `{"name":"节点2"}` {
		t.Fatalf("select recorded %+v", got)
	}
	err = c.SelectProxy(ctxT(t), chineseGroup, "bad")
	if err == nil || err.Error() != "切换节点失败：Selector update error: proxy not exist" {
		t.Fatalf("select err = %v", err)
	}
	err = c.SelectProxy(ctxT(t), "nope", "x")
	if err == nil || err.Error() != "切换节点失败：Resource not found" {
		t.Fatalf("select 404 err = %v", err)
	}

	d, err := c.ProxyDelay(ctxT(t), "节点1", "https://www.gstatic.com/generate_204", 3*time.Second)
	if err != nil || d != 88 {
		t.Fatalf("delay = %d, %v", d, err)
	}
	if got := f.last(); got.RawPath != "/proxies/%E8%8A%82%E7%82%B91/delay" || got.Query != "timeout=3000&url=https%3A%2F%2Fwww.gstatic.com%2Fgenerate_204" {
		t.Fatalf("delay recorded %+v", got)
	}
	_, err = c.ProxyDelay(ctxT(t), "slow", "https://www.gstatic.com/generate_204", 3*time.Second)
	if err == nil || err.Error() != "延迟测试失败：Timeout" {
		t.Fatalf("slow err = %v", err)
	}
	_, err = c.ProxyDelay(ctxT(t), "dead", "https://www.gstatic.com/generate_204", 3*time.Second)
	var ce *Error
	if !errors.As(err, &ce) || ce.Status != 503 {
		t.Fatalf("dead err = %v", err)
	}

	m, err := c.GroupDelay(ctxT(t), chineseGroup, "https://www.gstatic.com/generate_204", 5*time.Second)
	if err != nil || m["节点1"] != 100 || m["节点2"] != 250 || len(m) != 2 {
		t.Fatalf("group delay = %v, %v", m, err)
	}
	if got := f.last(); got.RawPath != "/group/"+escapedChineseGroup+"/delay" {
		t.Fatalf("group delay recorded %+v", got)
	}
}

func TestRulesAndProviders(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("GET /rules", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"rules":[
		 {"index":0,"type":"DomainSuffix","payload":"google.com","proxy":"PROXY","size":-1,"extra":{"disabled":false,"hitCount":3,"hitAt":"2025-01-02T03:04:05Z","missCount":1,"missAt":"0001-01-01T00:00:00Z"}},
		 {"index":1,"type":"GeoIP","payload":"CN","proxy":"DIRECT","size":1234},
		 {"index":2,"type":"Match","payload":"","proxy":"PROXY","size":-1}]}`)
	})
	f.mux.HandleFunc("PATCH /rules/disable", func(w http.ResponseWriter, r *http.Request) {
		var m map[int]bool // exactly what Mihomo decodes into
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil || !m[1] || m[2] {
			mihomoErr(w, 400, "Body invalid")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("GET /providers/rules", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"providers":{"广告":{"behavior":"Domain","format":"MrsRule","name":"广告","ruleCount":1200,"type":"Rule","updatedAt":"2025-03-04T05:06:07.000Z","vehicleType":"HTTP"},
		"inline":{"behavior":"Classical","format":"","name":"inline","ruleCount":2,"type":"Rule","updatedAt":"0001-01-01T00:00:00Z","vehicleType":"Inline","payload":["DOMAIN,a.b,DIRECT"]}}}`)
	})
	f.mux.HandleFunc("PUT /providers/rules/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != "广告" {
			mihomoErr(w, 404, "Resource not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c := f.client()

	rules, err := c.Rules(ctxT(t))
	if err != nil || len(rules) != 3 {
		t.Fatalf("rules = %v, %v", rules, err)
	}
	if rules[0].Extra == nil || rules[0].Extra.HitCount != 3 || rules[1].Extra != nil || rules[1].Size != 1234 || rules[2].Type != "Match" {
		t.Fatalf("rules = %+v", rules)
	}
	if err := c.DisableRules(ctxT(t), map[int]bool{1: true, 2: false}); err != nil {
		t.Fatal(err)
	}
	if got := f.last(); got.Method != "PATCH" || got.RawPath != "/rules/disable" {
		t.Fatalf("disable recorded %+v", got)
	}
	if err := c.DisableRules(ctxT(t), nil); err != nil {
		t.Fatal(err)
	}

	rp, err := c.RuleProviders(ctxT(t))
	if err != nil || len(rp) != 2 {
		t.Fatalf("providers = %v, %v", rp, err)
	}
	if p := rp["广告"]; p.Behavior != "Domain" || p.Format != "MrsRule" || p.RuleCount != 1200 || p.VehicleType != "HTTP" || p.UpdatedAt.Year() != 2025 {
		t.Fatalf("provider = %+v", p)
	}
	if err := c.UpdateRuleProvider(ctxT(t), "广告"); err != nil {
		t.Fatal(err)
	}
	if got := f.last(); got.RawPath != "/providers/rules/%E5%B9%BF%E5%91%8A" {
		t.Fatalf("update recorded %+v", got)
	}
	if err := c.UpdateRuleProvider(ctxT(t), "none"); err == nil || err.Error() != "更新规则集失败：Resource not found" {
		t.Fatalf("update 404 = %v", err)
	}
}

func TestConnectionsDNSCache(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("GET /connections", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"downloadTotal":1000,"uploadTotal":200,"connections":[{"id":"7a3b","metadata":{"network":"tcp","type":"HTTP Connect","sourceIP":"127.0.0.1","destinationIP":"1.2.3.4","sourceGeoIP":null,"destinationGeoIP":["CN"],"sourceIPASN":"","destinationIPASN":"","sourcePort":"51234","destinationPort":"443","inboundIP":"127.0.0.1","inboundPort":"7890","inboundName":"DEFAULT-MIXED","inboundUser":"","host":"example.com","dnsMode":"normal","uid":0,"process":"curl","processPath":"/usr/bin/curl","specialProxy":"","specialRules":"","remoteDestination":"1.2.3.4","dscp":0,"sniffHost":"example.com"},"upload":10,"download":20,"start":"2025-01-02T03:04:05Z","chains":["节点1","🇭🇰 香港"],"providerChains":["机场A",""],"rule":"DomainSuffix","rulePayload":"example.com"}],"memory":123456}`)
	})
	f.mux.HandleFunc("DELETE /connections", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	f.mux.HandleFunc("DELETE /connections/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	f.mux.HandleFunc("GET /dns/query", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "example.com" || r.URL.Query().Get("type") != "AAAA" {
			mihomoErr(w, 400, "invalid query type")
			return
		}
		io.WriteString(w, `{"AD":false,"Answer":[{"TTL":300,"data":"2606:2800::1","name":"example.com.","type":28}],"CD":false,"Question":[{"Name":"example.com.","Qtype":28,"Qclass":1}],"RA":true,"RD":true,"Status":0,"TC":false}`)
	})
	f.mux.HandleFunc("POST /cache/dns/flush", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	f.mux.HandleFunc("POST /cache/fakeip/flush", func(w http.ResponseWriter, r *http.Request) {
		mihomoErr(w, 400, "fakeip pool is nil")
	})
	c := f.client()

	cs, err := c.Connections(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if cs.DownloadTotal != 1000 || cs.Memory != 123456 || len(cs.Connections) != 1 {
		t.Fatalf("connections = %+v", cs)
	}
	cn := cs.Connections[0]
	if cn.ID != "7a3b" || cn.Metadata.DestinationPort != "443" || cn.Metadata.Type != "HTTP Connect" || cn.Chains[0] != "节点1" || cn.Rule != "DomainSuffix" || cn.Metadata.SniffHost != "example.com" || cn.Metadata.Process != "curl" {
		t.Fatalf("conn = %+v", cn)
	}
	if err := c.CloseConnection(ctxT(t), "7a3b"); err != nil {
		t.Fatal(err)
	}
	if got := f.last(); got.Method != "DELETE" || got.RawPath != "/connections/7a3b" {
		t.Fatalf("close recorded %+v", got)
	}
	if err := c.CloseAllConnections(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if got := f.last(); got.Method != "DELETE" || got.RawPath != "/connections" {
		t.Fatalf("close all recorded %+v", got)
	}

	res, err := c.DNSQuery(ctxT(t), "example.com", "AAAA")
	if err != nil || res.Status != 0 || len(res.Answer) != 1 || res.Answer[0].Type != 28 || res.Answer[0].TTL != 300 || res.Answer[0].Data != "2606:2800::1" {
		t.Fatalf("dns = %+v, %v", res, err)
	}
	if _, err := c.DNSQuery(ctxT(t), "example.com", "BOGUS"); err == nil || err.Error() != "DNS 查询失败：invalid query type" {
		t.Fatalf("dns err = %v", err)
	}

	if err := c.FlushDNSCache(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if got := f.last(); got.Method != "POST" || got.RawPath != "/cache/dns/flush" {
		t.Fatalf("flush recorded %+v", got)
	}
	if err := c.FlushFakeIPCache(ctxT(t)); err == nil || err.Error() != "清空 Fake-IP 缓存失败：fakeip pool is nil" {
		t.Fatalf("fakeip err = %v", err)
	}
}

func TestErrorsAndTimeouts(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	f.mux.HandleFunc("GET /rules", func(w http.ResponseWriter, r *http.Request) {
		// Non-JSON error body (e.g. a reverse proxy) must still produce a
		// readable message.
		http.Error(w, "gateway down", http.StatusBadGateway)
	})
	f.mux.HandleFunc("GET /proxies", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"proxies": not json`)
	})
	c := f.client()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := c.Version(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "获取内核版本失败：") {
		t.Fatalf("timeout err = %v", err)
	}

	_, err = c.Rules(ctxT(t))
	var ce *Error
	if !errors.As(err, &ce) || ce.Status != 502 || !strings.Contains(err.Error(), "gateway down") {
		t.Fatalf("502 err = %v", err)
	}

	_, err = c.Proxies(ctxT(t))
	if err == nil || !strings.Contains(err.Error(), "无法解析") {
		t.Fatalf("bad json err = %v", err)
	}

	// Dial failure (socket gone).
	f.srv.Close()
	os.Remove(f.sock)
	_, err = c.Version(ctxT(t))
	if err == nil || !strings.HasPrefix(err.Error(), "获取内核版本失败：无法连接内核") {
		t.Fatalf("dial err = %v", err)
	}
}

func TestDefaultTimeoutApplied(t *testing.T) {
	// The default timeout must not shorten a caller deadline and must be
	// applied when the caller has none.
	ctx, cancel := withTimeout(context.Background(), defaultTimeout)
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok || time.Until(dl) > defaultTimeout || time.Until(dl) < defaultTimeout-time.Second {
		t.Fatalf("deadline = %v", time.Until(dl))
	}
	parent, pc := context.WithTimeout(context.Background(), time.Second)
	defer pc()
	ctx2, c2 := withTimeout(parent, defaultTimeout)
	defer c2()
	if dl2, _ := ctx2.Deadline(); time.Until(dl2) > time.Second {
		t.Fatalf("parent deadline not kept: %v", time.Until(dl2))
	}
}

func TestStreamsReconnect(t *testing.T) {
	f := newFake(t)
	f.mux.HandleFunc("GET /traffic", func(w http.ResponseWriter, r *http.Request) {
		f.wsServe(w, r, func(ctx context.Context, send func(any) error) {
			// Mihomo also sends upTotal/downTotal; extra fields must be
			// ignored.
			for i := 1; i <= 2; i++ {
				if send(map[string]int64{"up": int64(i), "down": int64(i * 10), "upTotal": 1, "downTotal": 2}) != nil {
					return
				}
			}
			// Then drop the session: the client must reconnect.
		})
	})
	f.mux.HandleFunc("GET /memory", func(w http.ResponseWriter, r *http.Request) {
		f.wsServe(w, r, func(ctx context.Context, send func(any) error) {
			_ = send(map[string]uint64{"inuse": 42, "oslimit": 0})
			<-ctx.Done()
		})
	})
	var logLevel, connInterval atomic.Value
	f.mux.HandleFunc("GET /logs", func(w http.ResponseWriter, r *http.Request) {
		logLevel.Store(r.URL.Query().Get("level"))
		f.wsServe(w, r, func(ctx context.Context, send func(any) error) {
			_ = send(map[string]string{"type": "warning", "payload": "[TCP] dial failed"})
			<-ctx.Done()
		})
	})
	f.mux.HandleFunc("GET /connections", func(w http.ResponseWriter, r *http.Request) {
		connInterval.Store(r.URL.Query().Get("interval"))
		f.wsServe(w, r, func(ctx context.Context, send func(any) error) {
			_ = send(map[string]any{"downloadTotal": 5, "uploadTotal": 6, "connections": []any{}, "memory": 7})
			<-ctx.Done()
		})
	})
	c := f.client()

	// Traffic: server closes after 2 frames; expect ≥ 4 frames meaning at
	// least one reconnect happened (backoff starts at 0.5 s).
	ctx, cancel := context.WithCancel(context.Background())
	var frames atomic.Int32
	var first Traffic
	done := make(chan error, 1)
	go func() {
		done <- c.StreamTraffic(ctx, func(tr Traffic) {
			if frames.Add(1) == 1 {
				first = tr
			}
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for frames.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StreamTraffic returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StreamTraffic did not return after cancel")
	}
	if frames.Load() < 4 || f.wsConn.Load() < 2 {
		t.Fatalf("frames=%d sessions=%d", frames.Load(), f.wsConn.Load())
	}
	if first.Up != 1 || first.Down != 10 {
		t.Fatalf("first frame = %+v", first)
	}

	// Memory / Logs / Connections: one frame each, then cancel.
	runOne := func(name string, start func(ctx context.Context, got chan<- struct{}) error) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		got := make(chan struct{}, 1)
		done := make(chan error, 1)
		go func() { done <- start(ctx, got) }()
		select {
		case <-got:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatalf("%s: no frame", name)
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s returned %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not return after cancel", name)
		}
	}
	runOne("memory", func(ctx context.Context, got chan<- struct{}) error {
		return c.StreamMemory(ctx, func(m Memory) {
			if m.InUse == 42 {
				select {
				case got <- struct{}{}:
				default:
				}
			}
		})
	})
	runOne("logs", func(ctx context.Context, got chan<- struct{}) error {
		return c.StreamLogs(ctx, "warning", func(l Log) {
			if l.Type == "warning" && l.Payload == "[TCP] dial failed" {
				select {
				case got <- struct{}{}:
				default:
				}
			}
		})
	})
	if logLevel.Load() != "warning" {
		t.Fatalf("logs level query = %v", logLevel.Load())
	}
	runOne("connections", func(ctx context.Context, got chan<- struct{}) error {
		return c.StreamConnections(ctx, 250*time.Millisecond, func(cs Connections) {
			if cs.Memory == 7 && cs.DownloadTotal == 5 {
				select {
				case got <- struct{}{}:
				default:
				}
			}
		})
	})
	if connInterval.Load() != "250" {
		t.Fatalf("connections interval query = %v", connInterval.Load())
	}
}

func TestStreamReturnsOnCancelWhileServerDown(t *testing.T) {
	// No server at all: the stream must keep retrying and still return
	// promptly with ctx.Err() when cancelled.
	c := New(func(ctx context.Context) (net.Conn, error) {
		return nil, errors.New("connection refused")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.StreamLogs(ctx, "info", func(Log) { t.Error("unexpected frame") })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}
