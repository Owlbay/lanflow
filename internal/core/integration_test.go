package core

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// realCore returns ./bin/mihomo (relative to the repo root) or skips.
func realCore(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "bin", binaryName))
	if err != nil {
		t.Fatal(err)
	}
	if checkBinary(p) != nil {
		t.Skipf("real core not present at %s (run scripts/fetch-core.sh)", p)
	}
	return p
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

func writeMinimalConfig(t *testing.T, dir string, port int) string {
	t.Helper()
	cfg := filepath.Join(dir, "config.yaml")
	body := fmt.Sprintf("mixed-port: %d\nbind-address: 127.0.0.1\nallow-lan: false\nmode: direct\nlog-level: info\nproxies: []\n", port)
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestIntegrationVersionAndValidate(t *testing.T) {
	bin := realCore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	v, err := Version(ctx, bin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v, "v1.") {
		t.Fatalf("version = %q", v)
	}
	t.Logf("core version %s", v)

	dir := t.TempDir()
	good := writeMinimalConfig(t, dir, freePort(t))
	if err := Validate(ctx, bin, dir, good); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("mode: rule\nproxies: []\nproxy-groups:\n  - name: g\n    type: select\n    proxies: [xx]\nrules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = Validate(ctx, bin, dir, bad)
	if err == nil {
		t.Fatal("invalid config accepted")
	}
	if !strings.HasPrefix(err.Error(), "配置校验失败：") || !strings.Contains(err.Error(), "'xx' not found") {
		t.Fatalf("validate error = %q", err)
	}
	t.Logf("validate error: %s", err)
}

func TestIntegrationStartReadyStop(t *testing.T) {
	bin := realCore(t)
	dir := t.TempDir()
	spec := Spec{
		Bin:        bin,
		WorkDir:    dir,
		Config:     writeMinimalConfig(t, dir, freePort(t)),
		Controller: shortSocketPath(t),
		LogFile:    filepath.Join(dir, "core.log"),
	}
	m := NewManager(Options{}) // exec runner + default GET /version probe
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	start := time.Now()
	if err := m.Start(ctx, spec); err != nil {
		t.Fatalf("start: %v\nlog:\n%s", err, tailLines(spec.LogFile, 30))
	}
	t.Logf("ready after %s", time.Since(start).Round(time.Millisecond))
	if !m.Running() {
		t.Fatal("manager not running")
	}
	// The controller answers over Dial.
	if err := probeController(ctx, spec.Controller); err != nil {
		t.Fatalf("probe: %v", err)
	}

	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if m.Running() {
		t.Fatal("manager still running")
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(spec.Controller); !os.IsNotExist(err) {
			t.Fatalf("socket still present after stop: %v", err)
		}
	}
	if err := probeController(ctx, spec.Controller); err == nil {
		t.Fatal("controller still answering after stop")
	}
	select {
	case err := <-m.Crashes():
		t.Fatalf("crash reported for a clean stop: %v", err)
	default:
	}
	log := tailLines(spec.LogFile, 30)
	if !strings.Contains(log, "listening") {
		t.Fatalf("log looks wrong:\n%s", log)
	}
}

func TestIntegrationPortConflictReportsLog(t *testing.T) {
	bin := realCore(t)
	// Occupy a port so Mihomo fails to bind its mixed listener; the core
	// logs an error but keeps running, so this exercises the "ready" path
	// (controller still comes up). We just make sure start still works and
	// the log contains the bind error for diagnosis.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	spec := Spec{
		Bin:        bin,
		WorkDir:    dir,
		Config:     writeMinimalConfig(t, dir, port),
		Controller: shortSocketPath(t),
		LogFile:    filepath.Join(dir, "core.log"),
	}
	m := NewManager(Options{ReadyTimeout: 20 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	err = m.Start(ctx, spec)
	log := tailLines(spec.LogFile, 30)
	if err != nil {
		// Acceptable too: some versions exit on bind failure. Then the
		// error must carry the log tail.
		if !strings.Contains(err.Error(), "address already in use") && !strings.Contains(log, "address already in use") {
			t.Fatalf("start failed without a bind diagnosis: %v\nlog:\n%s", err, log)
		}
		return
	}
	// The controller comes up before the listeners are bound: the bind
	// error reaches the log a moment after Start returns.
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(log, "address already in use") && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		log = tailLines(spec.LogFile, 30)
	}
	if !strings.Contains(log, "address already in use") {
		t.Fatalf("bind error not visible in log:\n%s", log)
	}
}
