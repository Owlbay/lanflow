package app

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/netx"
)

// TestEndToEndRealCore drives the real Service with the real Mihomo in
// ./bin: import a local subscription, start, select a node, proxy a
// request, see the connection, add a custom rule (reload), switch mode,
// read the runtime config, stop. It never touches the system proxy or the
// TUN helper. Skipped when bin/mihomo is absent (scripts/fetch-core.sh).
func TestEndToEndRealCore(t *testing.T) {
	bin, _ := filepath.Abs("../../bin/mihomo")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("bin/mihomo not found; run scripts/fetch-core.sh")
	}
	if testing.Short() {
		t.Skip("short mode")
	}
	root := t.TempDir()
	res, _ := filepath.Abs("../../bin")
	s, err := NewService(Options{DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "log"), CacheDir: filepath.Join(root, "cache"), ResourceDir: res})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	t.Cleanup(func() {
		if t.Failed() {
			if b, err := os.ReadFile(filepath.Join(root, "log", "mihomo.log")); err == nil {
				t.Logf("core log:\n%s", b)
			}
		}
	})

	set := s.Settings()
	set.CorePath = bin
	set.MixedPort = freePort(t)
	set.ProbeExits = false
	if err := s.SaveSettings(set); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	sub := filepath.Join(root, "sub.yaml")
	os.WriteFile(sub, []byte(`proxies:
  - name: 直连节点
    type: direct
  - name: 直连节点2
    type: direct
proxy-groups:
  - name: 节点选择
    type: select
    proxies: [直连节点, 直连节点2]
rules:
  - IP-CIDR,127.0.0.0/8,节点选择,no-resolve
  - MATCH,DIRECT
`), 0o600)
	if _, err := s.ImportSubscriptionFile(sub, true); err != nil {
		t.Fatalf("import: %v", err)
	}
	e2eWait(t, 20*time.Second, "core running", func() bool { return s.Status().Core == model.CoreRunning })
	if v := s.Status().CoreVersion; !strings.HasPrefix(v, "v1.") {
		t.Errorf("CoreVersion = %q", v)
	}

	e2eWait(t, 10*time.Second, "proxies loaded", func() bool { return len(s.Proxies().Groups) > 0 })
	px := s.Proxies()
	if px.MainGroup != "节点选择" {
		t.Errorf("MainGroup = %q, groups %+v", px.MainGroup, px.Groups)
	}
	for _, g := range px.Groups {
		if g.Name == "LANFLOW-PROBE" {
			t.Error("probe group must be hidden")
		}
	}
	if err := s.SelectProxy("节点选择", "直连节点2"); err != nil {
		t.Fatalf("SelectProxy: %v", err)
	}
	e2eWait(t, 5*time.Second, "selection", func() bool {
		for _, g := range s.Proxies().Groups {
			if g.Name == "节点选择" {
				return g.Now == "直连节点2"
			}
		}
		return false
	})

	// A slow local endpoint keeps the connection open long enough to see it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2500 * time.Millisecond)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	done := make(chan error, 1)
	go func() {
		resp, err := netx.ViaPort(set.MixedPort, 10*time.Second).Get(srv.URL)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(b) != "ok" {
				err = io.ErrUnexpectedEOF
			}
		}
		done <- err
	}()
	e2eWait(t, 5*time.Second, "connection tracked", func() bool {
		for _, c := range s.Connections() {
			if strings.HasPrefix(c.Host, "127.0.0.1") && len(c.Chains) == 2 && c.Chains[0] == "节点选择" && c.Chains[1] == "直连节点2" {
				return true
			}
		}
		return false
	})
	if err := <-done; err != nil {
		t.Fatalf("request through the core: %v", err)
	}
	e2eWait(t, 5*time.Second, "closed connection", func() bool { return len(s.ClosedConnections()) > 0 })
	if up, down := s.TrafficHistory(); len(up) == 0 || len(down) == 0 {
		t.Error("no traffic history")
	}
	if rs := s.ExitRoutes(); len(rs) == 0 || rs[0].Group != "节点选择" || rs[0].Node != "直连节点2" {
		t.Errorf("ExitRoutes = %+v", rs)
	}

	// Custom rule → recompose, validate, hot reload.
	if _, err := s.SaveCustomRule(model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: "lanflow.test", Target: "节点选择"}); err != nil {
		t.Fatalf("SaveCustomRule: %v", err)
	}
	e2eWait(t, 10*time.Second, "custom rule live", func() bool {
		rs := s.Rules()
		return len(rs) > 0 && rs[0].Custom && rs[0].Payload == "lanflow.test"
	})
	if _, err := s.SaveCustomRule(model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: "x.test", Target: "不存在的组"}); err == nil {
		t.Error("a rule targeting a missing group must be rejected")
	}

	if err := s.SetMode(model.ModeGlobal); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if s.Status().Mode != model.ModeGlobal {
		t.Error("mode not global")
	}
	s.SetMode(model.ModeRule)

	lines, err := s.RuntimeConfig()
	if err != nil || len(lines) == 0 {
		t.Fatalf("RuntimeConfig: %v %d", err, len(lines))
	}
	var managed, custom bool
	for _, l := range lines {
		managed = managed || l.Source == "澜流设置"
		custom = custom || (l.Source == "自定义规则" && strings.Contains(l.Text, "lanflow.test"))
	}
	if !managed || !custom {
		t.Errorf("runtime lines lack sources: managed=%v custom=%v", managed, custom)
	}

	if err := s.StopCore(); err != nil {
		t.Fatalf("StopCore: %v", err)
	}
	if s.Status().Core != model.CoreStopped {
		t.Errorf("Core = %v after stop", s.Status().Core)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func e2eWait(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
