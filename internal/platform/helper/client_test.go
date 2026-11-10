package helper

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/core"
	"lanflow/internal/platform/helper/protocol"
)

// fakeHelper is an in-process protocol server standing in for the daemon.
type fakeHelper struct {
	mu        sync.Mutex
	running   bool
	exitError string
	starts    int
	stops     int
	lastSpec  protocol.Spec
	refuse    string // when set, start is refused with this message
	stopped   chan struct{}
	listener  net.Listener
	dialErrOn bool
}

func startFakeHelper(t *testing.T) *fakeHelper {
	t.Helper()
	f := &fakeHelper{stopped: make(chan struct{})}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.listener = l
	ctx, cancel := context.WithCancel(context.Background())
	srv := &protocol.Server{Handler: f.handle}
	go func() { _ = srv.Serve(ctx, l); close(f.stopped) }()
	addr := l.Addr().String()
	old := dialHelper
	dialHelper = func(ctx context.Context) (net.Conn, error) {
		f.mu.Lock()
		dead := f.dialErrOn
		f.mu.Unlock()
		if dead {
			return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
	t.Cleanup(func() { cancel(); dialHelper = old })
	return f
}

func (f *fakeHelper) handle(_ context.Context, req protocol.Request) protocol.Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req.Op {
	case protocol.OpPing:
		return protocol.Response{OK: true, Version: "fake", Protocol: protocol.Version}
	case protocol.OpStart:
		if f.refuse != "" {
			return protocol.Response{Error: f.refuse}
		}
		f.starts++
		f.running = true
		f.exitError = ""
		f.lastSpec = *req.Spec
		return protocol.Response{OK: true, Running: true, PID: 7}
	case protocol.OpStop:
		f.stops++
		f.running = false
		return protocol.Response{OK: true}
	case protocol.OpStatus:
		return protocol.Response{OK: true, Running: f.running, ExitError: f.exitError, PID: 7}
	}
	return protocol.Response{Error: "unknown"}
}

func (f *fakeHelper) crash(msg string) {
	f.mu.Lock()
	f.running = false
	f.exitError = msg
	f.mu.Unlock()
}

func newTestRunner() *runner {
	r := newRunner().(*runner)
	r.pollInterval = 20 * time.Millisecond
	return r
}

func TestInstalledPing(t *testing.T) {
	ctx := context.Background()
	if installed(ctx) {
		t.Skip("a real helper answers on this machine")
	}
	f := startFakeHelper(t)
	if !installed(ctx) {
		t.Fatal("installed() false with a fake helper running")
	}
	f.mu.Lock()
	f.dialErrOn = true
	f.mu.Unlock()
	start := time.Now()
	if installed(ctx) {
		t.Fatal("installed() true when dial fails")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("installed() did not respect the 1 s timeout")
	}
}

func TestRunnerStartStopNoExit(t *testing.T) {
	f := startFakeHelper(t)
	r := newTestRunner()
	ctx := context.Background()
	spec := core.Spec{Bin: "/b", WorkDir: "/w", Config: "/c", Controller: "/s", LogFile: "/l", Env: []string{"X=1"}}
	if err := r.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if !r.Running() {
		t.Fatal("not running after start")
	}
	exited := r.Exited()
	if f.lastSpec.Env[0] != "X=1" || f.lastSpec.Controller != "/s" {
		t.Fatalf("spec not forwarded: %+v", f.lastSpec)
	}
	time.Sleep(5 * r.pollInterval) // several status polls while running
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if r.Running() {
		t.Fatal("running after stop")
	}
	select {
	case err := <-exited:
		t.Fatalf("Stop must not report an exit, got %v", err)
	case <-time.After(5 * r.pollInterval):
	}
	if f.stops != 1 || f.starts != 1 {
		t.Fatalf("starts=%d stops=%d", f.starts, f.stops)
	}
}

func TestRunnerExitedOnCrash(t *testing.T) {
	f := startFakeHelper(t)
	r := newTestRunner()
	ctx := context.Background()
	if err := r.Start(ctx, core.Spec{Bin: "/b"}); err != nil {
		t.Fatal(err)
	}
	exited := r.Exited()
	f.crash("Mihomo 意外退出: exit status 1")
	select {
	case err := <-exited:
		if err == nil || !strings.Contains(err.Error(), "exit status 1") {
			t.Fatalf("exit error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Exited not delivered")
	}
	if r.Running() {
		t.Fatal("still running after crash")
	}
	// Exactly one delivery; the channel is then quiet.
	select {
	case err := <-exited:
		t.Fatalf("second delivery: %v", err)
	case <-time.After(5 * r.pollInterval):
	}

	// A new Start replaces the channel; the old one stays quiet.
	if err := r.Start(ctx, core.Spec{Bin: "/b"}); err != nil {
		t.Fatal(err)
	}
	exited2 := r.Exited()
	if exited2 == exited {
		t.Fatal("Exited channel not replaced on Start")
	}
	f.crash("second")
	select {
	case err := <-exited2:
		if err == nil || err.Error() != "second" {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second exit not delivered")
	}
}

func TestRunnerExitedWhenHelperVanishes(t *testing.T) {
	f := startFakeHelper(t)
	r := newTestRunner()
	if err := r.Start(context.Background(), core.Spec{Bin: "/b"}); err != nil {
		t.Fatal(err)
	}
	exited := r.Exited()
	f.mu.Lock()
	f.dialErrOn = true
	f.mu.Unlock()
	select {
	case err := <-exited:
		if err == nil || !strings.Contains(err.Error(), "权限组件无响应") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unreachable helper not reported")
	}
}

func TestRunnerStartRefused(t *testing.T) {
	f := startFakeHelper(t)
	f.refuse = "权限组件拒绝运行未登记的内核: /b"
	r := newTestRunner()
	err := r.Start(context.Background(), core.Spec{Bin: "/b"})
	if err == nil || !strings.Contains(err.Error(), "未登记") {
		t.Fatalf("err = %v", err)
	}
	if r.Running() {
		t.Fatal("running after refused start")
	}
}

func TestRunnerStartWithoutHelper(t *testing.T) {
	f := startFakeHelper(t)
	f.mu.Lock()
	f.dialErrOn = true
	f.mu.Unlock()
	r := newTestRunner()
	err := r.Start(context.Background(), core.Spec{Bin: "/b"})
	if err == nil || !strings.Contains(err.Error(), "无法连接权限组件") {
		t.Fatalf("err = %v", err)
	}
}

func TestExitedBeforeStartIsQuiet(t *testing.T) {
	r := newTestRunner()
	ch := r.Exited()
	if ch == nil {
		t.Fatal("Exited returned nil before Start")
	}
	select {
	case v := <-ch:
		t.Fatalf("unexpected value %v", v)
	default:
	}
}

func TestStopWithoutHelperIsNoop(t *testing.T) {
	f := startFakeHelper(t)
	f.mu.Lock()
	f.dialErrOn = true
	f.mu.Unlock()
	r := newTestRunner()
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on an idle runner without a helper: %v", err)
	}
}
