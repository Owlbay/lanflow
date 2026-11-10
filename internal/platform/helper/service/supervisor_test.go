//go:build darwin

package service

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/platform/helper/protocol"
)

// fakeProcess is a Process controlled by the test.
type fakeProcess struct {
	pid             int
	ignoreInterrupt bool
	exit            chan error
	once            sync.Once
	mu              sync.Mutex
	signals         []string
}

func (p *fakeProcess) Pid() int    { return p.pid }
func (p *fakeProcess) Wait() error { return <-p.exit }
func (p *fakeProcess) Interrupt() error {
	p.record("interrupt")
	if !p.ignoreInterrupt {
		p.finish(nil)
	}
	return nil
}
func (p *fakeProcess) Kill() error {
	p.record("kill")
	p.finish(errors.New("killed"))
	return nil
}
func (p *fakeProcess) record(sig string) {
	p.mu.Lock()
	p.signals = append(p.signals, sig)
	p.mu.Unlock()
}
func (p *fakeProcess) finish(err error) {
	p.once.Do(func() { p.exit <- err; close(p.exit) })
}
func (p *fakeProcess) sent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.signals...)
}

// fakeStarter records commands and hands out fake processes.
type fakeStarter struct {
	mu              sync.Mutex
	last            Command
	procs           []*fakeProcess
	failed          error
	ignoreInterrupt bool
}

func (f *fakeStarter) start(_ context.Context, c Command) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed != nil {
		return nil, f.failed
	}
	f.last = c
	p := &fakeProcess{pid: 1000 + len(f.procs), exit: make(chan error, 1), ignoreInterrupt: f.ignoreInterrupt}
	f.procs = append(f.procs, p)
	return p, nil
}

func newTestSupervisor(t *testing.T) (*Supervisor, *fixture, *fakeStarter) {
	t.Helper()
	f := newFixture(t)
	fs := &fakeStarter{}
	s := NewSupervisor(f.policy)
	s.Start = fs.start
	s.Hooks = Hooks{} // no chown/socket polling in tests
	s.Logf = t.Logf
	return s, f, fs
}

func TestSupervisorLifecycle(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	ctx := context.Background()
	if s.Running() {
		t.Fatal("running before start")
	}
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if !st.Running || st.PID != 1000 || st.ExitError != "" {
		t.Fatalf("status after start: %+v", st)
	}
	// Command line matches internal/core's flags.
	args := strings.Join(fs.last.Args, " ")
	resolvedWork, _ := filepath.EvalSymlinks(f.spec.WorkDir)
	resolvedCfg, _ := filepath.EvalSymlinks(f.spec.Config)
	want := "-d " + resolvedWork + " -f " + resolvedCfg + " -ext-ctl-unix " + f.spec.Controller
	if args != want || fs.last.Path != f.core || fs.last.Dir != resolvedWork {
		t.Fatalf("command = %s %s (dir %s), want %s", fs.last.Path, args, fs.last.Dir, want)
	}
	for _, kv := range fs.last.Env {
		if strings.HasPrefix(kv, "PATH=") && strings.Contains(kv, os.Getenv("HOME")) {
			t.Fatalf("helper leaked its own environment: %s", kv)
		}
	}
	// Only one at a time.
	if err := s.StartCore(ctx, f.spec); err == nil {
		t.Fatal("second start accepted")
	}
	// Log file was created inside the data dir.
	if _, err := os.Stat(f.spec.LogFile); err != nil {
		t.Fatalf("log file: %v", err)
	}
	if _, err := io.WriteString(fs.last.Output, "hello\n"); err != nil {
		t.Fatal(err)
	}

	if err := s.StopCore(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Running() {
		t.Fatal("running after stop")
	}
	st = s.Status()
	if st.Running || st.ExitError != "" {
		t.Fatalf("status after stop: %+v (stop must not count as unexpected exit)", st)
	}
	if got := fs.procs[0].sent(); len(got) != 1 || got[0] != "interrupt" {
		t.Fatalf("signals = %v, want graceful interrupt only", got)
	}
	// Stop when nothing runs is a no-op.
	if err := s.StopCore(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f.spec.LogFile)
	if !strings.Contains(string(b), "hello") {
		t.Fatalf("log content: %q", b)
	}
}

func TestSupervisorUnexpectedExit(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	ctx := context.Background()
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	fs.procs[0].finish(errors.New("exit status 1"))
	deadline := time.Now().Add(2 * time.Second)
	for s.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	st := s.Status()
	if st.Running || !strings.Contains(st.ExitError, "意外退出") || !strings.Contains(st.ExitError, "exit status 1") {
		t.Fatalf("status: %+v", st)
	}
	// A new start clears the exit error.
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); !st.Running || st.ExitError != "" || st.PID != 1001 {
		t.Fatalf("status after restart: %+v", st)
	}
	_ = s.StopCore(ctx)
}

func TestSupervisorRefusesInvalidSpec(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	bad := f.spec
	bad.Bin = "/usr/bin/true"
	if err := s.StartCore(context.Background(), bad); err == nil {
		t.Fatal("invalid spec accepted")
	}
	if len(fs.procs) != 0 {
		t.Fatal("process spawned despite refusal")
	}
	if s.Running() {
		t.Fatal("running after refusal")
	}
}

func TestSupervisorStartFailure(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	fs.failed = errors.New("exec format error")
	err := s.StartCore(context.Background(), f.spec)
	if err == nil || !strings.Contains(err.Error(), "exec format error") {
		t.Fatalf("err = %v", err)
	}
	if s.Running() {
		t.Fatal("running after failed start")
	}
}

func TestSupervisorKillsAfterGrace(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	fs.ignoreInterrupt = true
	ctx := context.Background()
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	// Use a short context so the test does not wait the full grace period.
	sctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := s.StopCore(sctx); err != nil {
		t.Fatal(err)
	}
	if got := fs.procs[0].sent(); len(got) != 2 || got[0] != "interrupt" || got[1] != "kill" {
		t.Fatalf("signals = %v", got)
	}
	if s.Running() {
		t.Fatal("still running after kill")
	}
}

func TestLogRotation(t *testing.T) {
	dir := resolvedTempDir(t)
	path := filepath.Join(dir, "core.log")
	if err := os.WriteFile(path, make([]byte, LogRotateSize), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := openLogFile(path, "-1")
	if err == nil {
		_ = w.Close()
	}
	// uid "-1" is invalid, so chown fails; use the current uid instead.
	w, err = openLogFile(path, strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "new\n")
	_ = w.Close()
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != LogRotateSize {
		t.Fatalf("rotated file: %v", err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "new\n" {
		t.Fatalf("new log = %q", b)
	}
}

// TestProtocolOverUnixSocket runs the real handler behind a protocol
// server on a temp unix socket and drives it with the protocol client,
// including the peer-uid authorizer.
func TestProtocolOverUnixSocket(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	sock := filepath.Join(shortTempDir(t), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &protocol.Server{Handler: NewHandler(s, "test"), Authorize: Authorizer(strconv.Itoa(os.Getuid())), Logf: t.Logf}
	go func() { _ = srv.Serve(ctx, l) }()
	c := &protocol.Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}

	resp, err := c.Call(ctx, protocol.Request{Op: protocol.OpPing})
	if err != nil || resp.Version != "test" || resp.Protocol != protocol.Version || resp.CorePath != f.core {
		t.Fatalf("ping: %+v %v", resp, err)
	}
	resp, err = c.Call(ctx, protocol.Request{Op: protocol.OpStart, Spec: &f.spec})
	if err != nil || !resp.Running || resp.PID == 0 {
		t.Fatalf("start: %+v %v", resp, err)
	}
	bad := f.spec
	bad.Bin = "/bin/sh"
	if _, err := c.Call(ctx, protocol.Request{Op: protocol.OpStart, Spec: &bad}); err == nil {
		t.Fatal("start with foreign binary accepted over the wire")
	}
	fs.procs[0].finish(errors.New("exit status 2"))
	var st protocol.Response
	for i := 0; i < 100; i++ {
		st, err = c.Call(ctx, protocol.Request{Op: protocol.OpStatus})
		if err != nil {
			t.Fatal(err)
		}
		if !st.Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.Running || !strings.Contains(st.ExitError, "exit status 2") {
		t.Fatalf("status: %+v", st)
	}
	if resp, err = c.Call(ctx, protocol.Request{Op: protocol.OpStop}); err != nil || resp.Running {
		t.Fatalf("stop: %+v %v", resp, err)
	}

	// A wrong peer uid is rejected by the authorizer.
	srv2 := &protocol.Server{Handler: NewHandler(s, "test"), Authorize: Authorizer("4294967")}
	sock2 := filepath.Join(filepath.Dir(sock), "h2.sock")
	l2, err := net.Listen("unix", sock2)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv2.Serve(ctx, l2) }()
	c2 := &protocol.Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock2)
	}}
	cctx, ccancel := context.WithTimeout(ctx, time.Second)
	defer ccancel()
	if _, err := c2.Call(cctx, protocol.Request{Op: protocol.OpPing}); err == nil {
		t.Fatal("connection from a different uid accepted")
	}
}

func TestPeerUID(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "p.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := net.Dial("unix", sock)
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			c.Close()
		}
	}()
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	uid, err := PeerUID(conn)
	if err != nil || int(uid) != os.Getuid() {
		t.Fatalf("uid=%d err=%v want %d", uid, err, os.Getuid())
	}
}

// shortTempDir returns a temp dir short enough for a unix socket path.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "lfh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}
