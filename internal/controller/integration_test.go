package controller

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestIntegrationRealMihomo starts the real kernel from ./bin/mihomo (the
// repository root) on a unix socket and exercises the client against it.
// It is skipped when the binary is absent.
func TestIntegrationRealMihomo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-socket integration test is not run on Windows")
	}
	bin, err := filepath.Abs(filepath.Join("..", "..", "bin", "mihomo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("bin/mihomo not present; skipping integration test")
	}

	// Short temp dir: sun_path is limited to ~104 bytes on macOS.
	dir, err := os.MkdirTemp("", "lf-mh")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := `mode: direct
log-level: silent
ipv6: false
allow-lan: false
unified-delay: true
geo-auto-update: false
profile:
  store-selected: false
dns:
  enable: true
  ipv6: false
  enhanced-mode: normal
  nameserver:
    - 223.5.5.5
    - 1.1.1.1
proxies: []
proxy-groups: []
rules:
  - MATCH,DIRECT
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "ctl.sock")

	procCtx, stop := context.WithCancel(context.Background())
	defer stop()
	cmd := exec.CommandContext(procCtx, bin, "-d", dir, "-f", cfgPath, "-ext-ctl-unix", sock)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatalf("start mihomo: %v", err)
	}
	defer func() {
		stop()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("mihomo output:\n%s", output.String())
		}
	}()

	c := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	})

	// Wait for the controller to come up.
	var version string
	deadline := time.Now().Add(15 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		version, err = c.Version(ctx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("kernel did not start: %v\n%s", err, output.String())
		}
		if cmd.ProcessState != nil {
			t.Fatalf("kernel exited early:\n%s", output.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if version == "" {
		t.Fatal("empty version")
	}
	t.Logf("mihomo %s", version)

	ctx := func() context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		return ctx
	}

	conf, err := c.Configs(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if conf.Mode != "direct" || conf.LogLevel != "silent" || conf.AllowLan {
		t.Fatalf("Configs = %+v", conf)
	}

	mode := "rule"
	if err := c.PatchConfigs(ctx(), ConfigPatch{Mode: &mode}); err != nil {
		t.Fatal(err)
	}
	conf, err = c.Configs(ctx())
	if err != nil || conf.Mode != "rule" {
		t.Fatalf("after patch: %+v, %v", conf, err)
	}

	proxies, err := c.Proxies(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := proxies["DIRECT"]; !ok || d.Type != "Direct" {
		t.Fatalf("Proxies missing DIRECT: %v", proxies)
	}
	if g, ok := proxies["GLOBAL"]; !ok || g.Type != "Selector" || len(g.All) == 0 {
		t.Fatalf("Proxies missing GLOBAL selector: %+v", proxies["GLOBAL"])
	}

	// Selecting a missing member must produce a decoded server message.
	err = c.SelectProxy(ctx(), "GLOBAL", "no-such-node")
	var ce *Error
	if !errors.As(err, &ce) || ce.Status != 400 || ce.Message == "" {
		t.Fatalf("SelectProxy error = %v", err)
	}
	t.Logf("select error text: %v", err)

	rules, err := c.Rules(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Type != "Match" || rules[0].Proxy != "DIRECT" {
		t.Fatalf("Rules = %+v", rules)
	}

	// DNS needs the network; only transport/protocol failures are fatal.
	res, err := c.DNSQuery(ctx(), "www.example.com", "A")
	switch {
	case err == nil:
		t.Logf("DNS status=%d answers=%d", res.Status, len(res.Answer))
	case errors.As(err, &ce) && ce.Status == 500:
		t.Logf("DNS query failed upstream (offline?): %v", err)
	default:
		t.Fatalf("DNSQuery: %v", err)
	}

	if err := c.FlushDNSCache(ctx()); err != nil {
		t.Fatal(err)
	}

	cs, err := c.Connections(ctx())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("connections: %d", len(cs.Connections))

	// Stream traffic for 2 s: Mihomo emits one frame per second.
	var frames atomic.Int32
	sctx, scancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer scancel()
	err = c.StreamTraffic(sctx, func(Traffic) { frames.Add(1) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StreamTraffic returned %v", err)
	}
	if frames.Load() < 1 {
		t.Fatalf("no traffic frames received")
	}
	t.Logf("traffic frames: %d", frames.Load())

	// Kill the kernel and make sure the client reports it cleanly.
	stop()
	_ = cmd.Wait()
	_, err = c.Version(ctx())
	if err == nil {
		t.Fatal("Version succeeded after kernel exit")
	}
}
