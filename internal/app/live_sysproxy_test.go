package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/netx"
	"lanflow/internal/platform/sysproxy"
)

// TestLiveSystemProxy changes the REAL system proxy for a few seconds:
// Lanflow's core (direct-only nodes, free port) becomes the system proxy,
// a request goes through it, then Lanflow lets go and the original setting
// must be back exactly. Opt-in: LANFLOW_LIVE_SYSPROXY=1.
func TestLiveSystemProxy(t *testing.T) {
	if os.Getenv("LANFLOW_LIVE_SYSPROXY") != "1" {
		t.Skip("set LANFLOW_LIVE_SYSPROXY=1 to change the real system proxy briefly")
	}
	bin, _ := filepath.Abs("../../bin/mihomo")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("bin/mihomo not found")
	}
	before, err := sysproxy.Get()
	if err != nil {
		t.Fatalf("read system proxy: %v", err)
	}
	saved, err := sysproxy.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	t.Cleanup(func() { // safety net whatever happens below
		if now, _ := sysproxy.Get(); !reflect.DeepEqual(now, before) {
			t.Logf("safety net: restoring the original system proxy")
			_ = sysproxy.Restore(saved)
		}
	})
	t.Logf("system proxy before: %+v", before)

	root := t.TempDir()
	res, _ := filepath.Abs("../../bin")
	s, err := NewService(Options{DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "log"), CacheDir: filepath.Join(root, "cache"), ResourceDir: res})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	set := s.Settings()
	set.CorePath, set.MixedPort, set.ProbeExits = bin, freePort(t), false
	if err := s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub.yaml")
	os.WriteFile(sub, []byte("proxies:\n  - name: 直连节点\n    type: direct\nproxy-groups:\n  - name: 节点选择\n    type: select\n    proxies: [直连节点]\nrules:\n  - MATCH,节点选择\n"), 0o600)
	if _, err := s.ImportSubscriptionFile(sub, true); err != nil {
		t.Fatal(err)
	}
	e2eWait(t, 20*time.Second, "core running", func() bool { return s.Status().Core == model.CoreRunning })

	if err := s.SetSystemProxy(true); err != nil {
		t.Fatalf("SetSystemProxy(true): %v", err)
	}
	on, _ := sysproxy.Get()
	t.Logf("system proxy while on: %+v", on)
	if !on.Enabled || on.Host != "127.0.0.1" || on.Port != set.MixedPort {
		t.Fatalf("system proxy does not point at Lanflow: %+v", on)
	}

	// A request through the system proxy setting reaches the core.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	resp, err := netx.ViaPort(on.Port, 5*time.Second).Get(srv.URL)
	if err != nil {
		t.Fatalf("request via system proxy: %v", err)
	}
	resp.Body.Close()

	if err := s.SetSystemProxy(false); err != nil {
		t.Fatalf("SetSystemProxy(false): %v", err)
	}
	after, _ := sysproxy.Get()
	t.Logf("system proxy after: %+v", after)
	if after.Enabled != before.Enabled || (before.Enabled && (after.Host != before.Host || after.Port != before.Port)) {
		t.Fatalf("system proxy not restored: before %+v after %+v", before, after)
	}
}
