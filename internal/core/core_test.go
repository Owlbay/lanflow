package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortSocketPath returns a controller address that fits sun_path even
// when t.TempDir() is long (macOS).
func shortSocketPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\lanflow-test-%d-%s`, os.Getpid(), strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	}
	name := fmt.Sprintf("lf-t-%d-%d.sock", os.Getpid(), time.Now().UnixNano()%1_000_000)
	p := filepath.Join(os.TempDir(), name)
	if len(p) >= unixSocketPathLimit {
		p = filepath.Join("/tmp", name)
	}
	t.Cleanup(func() { _ = os.Remove(p) })
	return p
}

// ---- log rotation ---------------------------------------------------------

func TestRotatingFileRotatesAtLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core.log")
	rf, err := openRotatingFile(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := bytes.Repeat([]byte("x"), 39)
	line = append(line, '\n') // 40 bytes
	for i := 0; i < 5; i++ {  // 200 bytes total -> rotates twice
		if _, err := rf.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := rf.Close(); err != nil {
		t.Fatal(err)
	}
	cur, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Size() > 100 {
		t.Fatalf("current log is %d bytes, want <= 100", cur.Size())
	}
	old, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("expected rotated file: %v", err)
	}
	if old.Size() > 100 || old.Size() == 0 {
		t.Fatalf("rotated log size %d, want 0 < n <= 100", old.Size())
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("only one old file must be kept, got .2 (err=%v)", err)
	}
	// Reopening appends and keeps rotating.
	rf, err = openRotatingFile(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := rf.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	_ = rf.Close()
	before, _ := os.Stat(path)
	if n, err := rf.Write(line); err != nil || n != len(line) {
		t.Fatalf("write after close: n=%d err=%v, want discarded without error", n, err)
	}
	if after, _ := os.Stat(path); after.Size() != before.Size() {
		t.Fatal("write after close reached the file")
	}
}

// A failing disk must not fail Write: the error would end exec.Cmd's copy
// goroutine and get the child killed by SIGPIPE. The error is kept once.
func TestRotatingFileWriteNeverFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core.log")
	rf, err := openRotatingFile(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	_, _ = rf.Write([]byte("0123456789"))
	// Rotation now fails: the directory disappears under the file.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if n, err := rf.Write([]byte("more\n")); err != nil || n != 5 {
			t.Fatalf("write %d: n=%d err=%v", i, n, err)
		}
	}
	if rf.Err() == nil {
		t.Fatal("the failure was not recorded")
	}
}

func TestTailLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	var b strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	b.WriteString("\n\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got := strings.Split(tailLines(path, 20), "\n")
	if len(got) != 20 || got[0] != "line 31" || got[19] != "line 50" {
		t.Fatalf("tail = %q", got)
	}
	if tailLines(filepath.Join(t.TempDir(), "missing"), 5) != "" {
		t.Fatal("missing file should give empty tail")
	}
}

// ---- controller address ----------------------------------------------------

func TestControllerAddressShort(t *testing.T) {
	addr := controllerAddress()
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(addr, `\\.\pipe\lanflow-`) {
			t.Fatalf("pipe name = %q", addr)
		}
		return
	}
	if len(addr) >= unixSocketPathLimit {
		t.Fatalf("socket path %q is %d bytes, want < %d", addr, len(addr), unixSocketPathLimit)
	}
	if !filepath.IsAbs(addr) || !strings.HasSuffix(addr, ".sock") || !strings.Contains(addr, "lanflow-") {
		t.Fatalf("socket path = %q", addr)
	}
	// A very long TMPDIR falls back to the user's ~/.lanflow (the TUN
	// helper refuses /tmp), and to /tmp only when home is too long too.
	home := t.TempDir()
	if len(home) > 60 {
		home = "/tmp/lfh" + strings.Repeat("x", 4)
		_ = os.MkdirAll(home, 0o700)
		t.Cleanup(func() { os.RemoveAll(home) })
	}
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", "/"+strings.Repeat("longdir/", 20))
	addr = controllerAddress()
	if !strings.HasPrefix(addr, filepath.Join(home, ".lanflow")+"/") || len(addr) >= unixSocketPathLimit {
		t.Fatalf("fallback address = %q", addr)
	}
	// An unset TMPDIR (os.TempDir() = /tmp) must not put the socket in /tmp.
	t.Setenv("TMPDIR", "")
	if addr = controllerAddress(); !strings.HasPrefix(addr, filepath.Join(home, ".lanflow")+"/") {
		t.Fatalf("address with TMPDIR unset = %q", addr)
	}
	t.Setenv("HOME", "/"+strings.Repeat("longhome/", 20))
	addr = controllerAddress()
	if !strings.HasPrefix(addr, "/tmp/") {
		t.Fatalf("last-resort address = %q", addr)
	}
}

// ---- version ---------------------------------------------------------------

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"Mihomo Meta v1.19.33 darwin arm64 with go1.26.10 Wed Oct 21 16:44:27 UTC 2026\nUse tags: with_gvisor\n", "v1.19.33", false},
		{"Mihomo Meta alpha-7c6e3d2 linux amd64 with go1.25 unknown time\n", "alpha-7c6e3d2", false},
		{"Mihomo Meta 1.19.0 windows amd64 with go1.25\n", "v1.19.0", false},
		{"Mihomo v1.18.0 linux\n", "v1.18.0", false},
		{"", "", true},
		{"clash: command not found", "", true},
	}
	for _, c := range cases {
		got, err := parseVersion(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseVersion(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("parseVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestVersionFakeBinary(t *testing.T) {
	bin := fakeBinary(t)
	v, err := version(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if v != "v1.19.33" {
		t.Fatalf("version = %q", v)
	}
	if _, err := version(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil || !strings.Contains(err.Error(), "无法获取内核版本") {
		t.Fatalf("missing binary err = %v", err)
	}
}

// ---- validate --------------------------------------------------------------

func TestShapeValidateError(t *testing.T) {
	got := shapeValidateError(fakeValidateOut)
	want := "配置校验失败：proxy group[0]: g: 'xx' not found"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got = shapeValidateError("time=\"x\" level=error msg=\"yaml: line 3: mapping values are not allowed in this context\"\nconfiguration test failed\n")
	if got != "配置校验失败：yaml: line 3: mapping values are not allowed in this context" {
		t.Fatalf("got %q", got)
	}
	got = shapeValidateError("something odd happened\n")
	if got != "配置校验失败：something odd happened" {
		t.Fatalf("got %q", got)
	}
	if shapeValidateError("") != "配置校验失败：内核未给出具体原因" {
		t.Fatal("empty output")
	}
}

func TestValidateFakeBinary(t *testing.T) {
	bin := fakeBinary(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(cfg, []byte("proxies: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := validate(context.Background(), bin, dir, cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "配置校验失败：proxy group[0]: g: 'xx' not found" {
		t.Fatalf("err = %q", err)
	}
	err = validate(context.Background(), filepath.Join(dir, "nope"), dir, cfg)
	if err == nil || !strings.Contains(err.Error(), "无法运行内核进行配置校验") {
		t.Fatalf("missing binary err = %v", err)
	}
}

// ---- findBinary ------------------------------------------------------------

func TestFindBinary(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// custom: must be an executable regular file
	if _, err := findBinary(filepath.Join(dir, "missing")); err == nil || !strings.Contains(err.Error(), "自定义内核路径不可用") {
		t.Fatalf("missing custom: %v", err)
	}
	if _, err := findBinary(dir); err == nil || !strings.Contains(err.Error(), "不是普通文件") {
		t.Fatalf("dir as custom: %v", err)
	}
	if runtime.GOOS != "windows" {
		p := write("noexec", 0o644)
		if _, err := findBinary(p); err == nil || !strings.Contains(err.Error(), "没有可执行权限") {
			t.Fatalf("non-executable custom: %v", err)
		}
	}
	custom := write("custom-mihomo", 0o755)
	if got, err := findBinary(custom); err != nil || got != custom {
		t.Fatalf("custom = %q, %v", got, err)
	}

	// bundled: nothing anywhere -> ErrNotFound with hint
	exeDir := filepath.Join(dir, "app", "Contents", "MacOS")
	cwd := filepath.Join(dir, "cwd")
	executablePath = func() (string, error) { return filepath.Join(exeDir, "lanflow"), nil }
	getwd = func() (string, error) { return cwd, nil }
	t.Cleanup(func() { executablePath, getwd = os.Executable, os.Getwd })

	_, err := findBinary("")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "fetch-core.sh") {
		t.Fatalf("not found err = %v", err)
	}
	// ./bin during development
	devBin := write(filepath.Join("cwd", "bin", binaryName), 0o755)
	if got, err := findBinary(""); err != nil || got != devBin {
		t.Fatalf("dev bin = %q, %v", got, err)
	}
	// next to executable wins over ./bin
	sideBin := write(filepath.Join("app", "Contents", "MacOS", binaryName), 0o755)
	if got, err := findBinary(""); err != nil || got != sideBin {
		t.Fatalf("side bin = %q, %v", got, err)
	}
	// app bundle Resources wins over everything
	resBin := write(filepath.Join("app", "Contents", "Resources", "mihomo"), 0o755)
	if got, err := findBinary(""); err != nil || got != resBin {
		t.Fatalf("resources bin = %q, %v", got, err)
	}
}

// ---- exec runner with the fake core ----------------------------------------

func TestExecRunnerGracefulStop(t *testing.T) {
	spec := fakeCoreSpec(t)
	if runtime.GOOS != "windows" {
		// a stale socket must be removed before start
		if err := os.WriteFile(spec.Controller, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := newExecRunner()
	ctx := context.Background()
	if err := r.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if !r.Running() {
		t.Fatal("should be running")
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(spec.Controller); !os.IsNotExist(err) {
			t.Fatalf("stale socket not removed: %v", err)
		}
	}
	if err := r.Start(ctx, spec); err == nil {
		t.Fatal("second Start while running must fail")
	}
	exited := r.Exited()
	waitFor(t, func() bool { return strings.Contains(tailLines(spec.LogFile, 5), "fake core started") })

	start := time.Now()
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > stopGrace {
		t.Fatalf("stop took %s; graceful shutdown did not work", d)
	}
	if r.Running() {
		t.Fatal("should not be running")
	}
	select {
	case err := <-exited:
		t.Fatalf("Exited fired after an explicit Stop: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if !strings.Contains(tailLines(spec.LogFile, 5), "fake core shutting down") {
		t.Fatalf("expected graceful shutdown line in log, got:\n%s", tailLines(spec.LogFile, 5))
	}
	if !strings.Contains(tailLines(spec.LogFile, 5), "-d "+spec.WorkDir) {
		t.Fatalf("args not passed, log:\n%s", tailLines(spec.LogFile, 5))
	}
	// Stop on a stopped runner is a no-op.
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecRunnerUnexpectedExit(t *testing.T) {
	spec := fakeCoreSpec(t, fakeExitEnv+"=3", fakeExitAfterEnv+"=100")
	r := newExecRunner()
	if err := r.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-r.Exited():
		if err == nil {
			t.Fatal("nil exit error")
		}
		msg := err.Error()
		for _, want := range []string{"Mihomo 内核意外退出", "exit status 3", "address already in use", "fake core started"} {
			if !strings.Contains(msg, want) {
				t.Errorf("exit error lacks %q:\n%s", want, msg)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Exited did not fire")
	}
	if r.Running() {
		t.Fatal("still running after exit")
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(spec.Controller); !os.IsNotExist(err) {
			t.Fatalf("socket not cleaned after exit: %v", err)
		}
	}
}

func TestExecRunnerRejectsBadSpec(t *testing.T) {
	r := newExecRunner()
	err := r.Start(context.Background(), Spec{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty spec err = %v", err)
	}
	spec := fakeCoreSpec(t)
	spec.Bin = filepath.Join(t.TempDir(), "nope")
	if err := r.Start(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "无法启动 Mihomo 内核") {
		t.Fatalf("missing bin err = %v", err)
	}
}

// ---- manager with an in-memory runner --------------------------------------

// memRunner is a Runner that never spawns anything.
type memRunner struct {
	mu       sync.Mutex
	running  bool
	exited   chan error
	starts   int
	stops    int
	startErr error
}

func newMemRunner() *memRunner { return &memRunner{exited: make(chan error, 1)} }

func (r *memRunner) Start(context.Context, Spec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	if r.startErr != nil {
		return r.startErr
	}
	r.running = true
	r.exited = make(chan error, 1)
	return nil
}

func (r *memRunner) Stop(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops++
	r.running = false
	return nil
}

func (r *memRunner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

func (r *memRunner) Exited() <-chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exited
}

// crash simulates an unexpected exit.
func (r *memRunner) crash(err error) {
	r.mu.Lock()
	r.running = false
	ch := r.exited
	r.mu.Unlock()
	ch <- err
}

func (r *memRunner) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts, r.stops
}

func memSpec(t *testing.T) Spec {
	dir := t.TempDir()
	return Spec{Bin: "mihomo", WorkDir: dir, Config: filepath.Join(dir, "c.yaml"), Controller: "x", LogFile: filepath.Join(dir, "core.log")}
}

func TestManagerStartReadyStop(t *testing.T) {
	r := newMemRunner()
	var readyCalls int
	var mu sync.Mutex
	m := NewManager(Options{Runner: r, ReadyTimeout: 5 * time.Second, Ready: func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		readyCalls++
		if readyCalls < 3 {
			return errors.New("not yet")
		}
		return nil
	}})
	ctx := context.Background()
	if err := m.Start(ctx, memSpec(t)); err != nil {
		t.Fatal(err)
	}
	if !m.Running() {
		t.Fatal("manager should be running")
	}
	mu.Lock()
	if readyCalls != 3 {
		t.Fatalf("ready called %d times, want 3", readyCalls)
	}
	mu.Unlock()
	// Restart while running stops first.
	if err := m.Start(ctx, memSpec(t)); err != nil {
		t.Fatal(err)
	}
	if starts, stops := r.counts(); starts != 2 || stops != 1 {
		t.Fatalf("starts=%d stops=%d, want 2/1", starts, stops)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if m.Running() {
		t.Fatal("manager should be stopped")
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, stops := r.counts(); stops != 2 {
		t.Fatalf("stops=%d, want 2 (second Stop is a no-op)", stops)
	}
	select {
	case err := <-m.Crashes():
		t.Fatalf("crash reported after Stop: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestManagerReadyTimeout(t *testing.T) {
	r := newMemRunner()
	m := NewManager(Options{Runner: r, ReadyTimeout: 500 * time.Millisecond, Ready: func(context.Context) error { return errors.New("connection refused") }})
	spec := memSpec(t)
	if err := os.WriteFile(spec.LogFile, []byte("level=info msg=\"booting\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := m.Start(context.Background(), spec)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	for _, want := range []string{"未就绪", "connection refused", "booting"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("timeout error lacks %q: %v", want, err)
		}
	}
	if m.Running() || r.Running() {
		t.Fatal("runner must be stopped after a failed start")
	}
}

func TestManagerEarlyExit(t *testing.T) {
	r := newMemRunner()
	m := NewManager(Options{Runner: r, ReadyTimeout: 5 * time.Second, Ready: func(context.Context) error { return errors.New("not yet") }})
	go func() {
		waitFor(t, func() bool { s, _ := r.counts(); return s == 1 })
		r.crash(errors.New("Mihomo 内核意外退出（exit status 1），最近日志：\nport in use"))
	}()
	err := m.Start(context.Background(), memSpec(t))
	if err == nil || !strings.Contains(err.Error(), "port in use") {
		t.Fatalf("err = %v", err)
	}
	if m.Running() {
		t.Fatal("should not be running")
	}
}

func TestManagerCrashForwardedAndSetRunner(t *testing.T) {
	r := newMemRunner()
	m := NewManager(Options{Runner: r, Ready: func(context.Context) error { return nil }})
	if err := m.Start(context.Background(), memSpec(t)); err != nil {
		t.Fatal(err)
	}
	r.crash(errors.New("boom"))
	select {
	case err := <-m.Crashes():
		if err == nil || err.Error() != "boom" {
			t.Fatalf("crash = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("crash not forwarded")
	}
	waitFor(t, func() bool { return !m.Running() })

	// SetRunner while running stops the old runner and uses the new one.
	if err := m.Start(context.Background(), memSpec(t)); err != nil {
		t.Fatal(err)
	}
	r2 := newMemRunner()
	m.SetRunner(r2)
	if r.Running() || m.Running() {
		t.Fatal("old runner must be stopped by SetRunner")
	}
	if err := m.Start(context.Background(), memSpec(t)); err != nil {
		t.Fatal(err)
	}
	if s, _ := r2.counts(); s != 1 || !r2.Running() {
		t.Fatal("new runner not used")
	}
	_ = m.Stop(context.Background())
}

func TestManagerStartErrorPropagates(t *testing.T) {
	r := newMemRunner()
	r.startErr = errors.New("无法启动 Mihomo 内核")
	m := NewManager(Options{Runner: r})
	if err := m.Start(context.Background(), memSpec(t)); err == nil || !errors.Is(err, r.startErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestManagerConcurrentStartStop(t *testing.T) {
	r := newMemRunner()
	m := NewManager(Options{Runner: r, Ready: func(context.Context) error { return nil }})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = m.Start(context.Background(), memSpec(t)) }()
		go func() { defer wg.Done(); _ = m.Stop(context.Background()) }()
	}
	wg.Wait()
	_ = m.Stop(context.Background())
	if m.Running() || r.Running() {
		t.Fatal("expected stopped state after final Stop")
	}
}

// waitFor polls cond up to 10 s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
