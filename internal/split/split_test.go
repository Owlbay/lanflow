package split

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/app/remote"
	"lanflow/internal/model"
	"lanflow/internal/rpc"
)

// The two processes with the fake backend: this test binary is the window
// process, and starts itself again as a headless service process
// (LANFLOW_SPLIT_TEST_SERVICE=1): the fake, the notice queue and the local
// interface with a host that quits on QuitService, as `Lanflow --service`
// does without the tray.

const serviceEnv = "LANFLOW_SPLIT_TEST_SERVICE"

func TestMain(m *testing.M) {
	if os.Getenv(serviceEnv) == "1" {
		runTestService()
		return
	}
	os.Exit(m.Run())
}

type testHost struct {
	once sync.Once
	quit chan struct{}
	bg   atomic.Bool
}

func (*testHost) Split() bool                      { return true }
func (*testHost) ShowWindow(string)                {}
func (h *testHost) SetBackground(hidden bool)      { h.bg.Store(hidden) }
func (h *testHost) Quit()                          { h.once.Do(func() { close(h.quit) }) }
func (*testHost) SetShortcut(string, string) error { return nil }

func runTestService() {
	f := fake.NewInstant()
	box := NewNotices(f)
	h := &testHost{quit: make(chan struct{})}
	srv, err := rpc.Start(os.Getenv("LANFLOW_SPLIT_TEST_DIR"), rpc.Options{API: box, Version: os.Getenv("LANFLOW_SPLIT_TEST_VERSION"), Host: h})
	if err != nil {
		os.Exit(3)
	}
	go func() { // the streams of a running core
		for {
			time.Sleep(100 * time.Millisecond)
			f.Tick()
		}
	}()
	<-h.quit
	srv.SendUI(rpc.UIQuit, "")
	time.Sleep(50 * time.Millisecond)
	_ = srv.Close()
	os.Exit(0)
}

type harness struct {
	t    *testing.T
	dir  string
	exe  string
	pids chan int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	base := ""
	if runtime.GOOS != "windows" {
		base = "/tmp" // unix socket paths are short
	}
	dir, err := os.MkdirTemp(base, "lfs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if runtime.GOOS == "windows" {
		t.Setenv("LANFLOW_SOCKET", `\\.\pipe\lanflow-split-test-`+filepath.Base(dir))
	} else {
		t.Setenv("LANFLOW_SOCKET", filepath.Join(dir, "s.sock"))
	}
	t.Setenv(serviceEnv, "1")
	t.Setenv("LANFLOW_SPLIT_TEST_DIR", dir)
	t.Setenv("LANFLOW_SPLIT_TEST_VERSION", "1.0")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, dir: dir, exe: exe, pids: make(chan int, 16)}
	t.Cleanup(h.killAll)
	return h
}

// start is ConnectOptions.Start: the service process.
func (h *harness) start() error {
	pid, err := Start(h.exe)
	if err == nil {
		h.pids <- pid
	}
	return err
}

func (h *harness) killAll() {
	for {
		select {
		case pid := <-h.pids:
			if Alive(pid) {
				_ = Terminate(pid)
			}
		default:
			return
		}
	}
}

type window struct {
	c       *remote.Client
	res     rpc.AttachResult
	cmds    chan rpc.UICommand
	ups     chan bool
	topics  chan app.Topic
	holding atomic.Bool
}

func (h *harness) connect(attach string) (*window, error) {
	w := &window{cmds: make(chan rpc.UICommand, 16), ups: make(chan bool, 16), topics: make(chan app.Topic, 1024)}
	c, res, err := Connect(context.Background(), ConnectOptions{
		DataDir: h.dir, Version: "1.0", Attach: attach, Start: h.start, Wait: 15 * time.Second,
		OnUI:    func(u rpc.UICommand) { w.cmds <- u },
		OnState: func(up bool, _ error) { w.ups <- up },
		Hold:    w.holding.Load,
	})
	if err != nil {
		return nil, err
	}
	w.c, w.res = c, res
	if c != nil {
		h.t.Cleanup(func() { c.Close() })
		c.OnChange(func(t app.Topic) {
			select {
			case w.topics <- t:
			default:
			}
		})
	}
	return w, nil
}

func waitFor[T any](t *testing.T, ch <-chan T, what string, ok func(T) bool) T {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case v := <-ch:
			if ok == nil || ok(v) {
				return v
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestTwoProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	h := newHarness(t)

	// The window finds no service and starts one.
	w, err := h.connect("")
	if err != nil {
		t.Fatal(err)
	}
	if w.res.Forwarded || !w.res.Split || w.res.PID == 0 || !Alive(w.res.PID) {
		t.Fatalf("attach: %+v", w.res)
	}
	if st := w.c.Status(); st.Core != model.CoreRunning {
		t.Fatalf("status over the service: %+v", st)
	}
	waitFor(t, w.topics, "a conns event", func(tp app.Topic) bool { return tp == app.TopicConns })
	if len(w.c.Connections()) == 0 || len(w.c.Logs()) == 0 {
		t.Fatal("lists empty over the service")
	}

	// A second window process is sent to the first one.
	w2, err := h.connect("lanflow://import?url=x")
	if err != nil {
		t.Fatal(err)
	}
	if !w2.res.Forwarded || w2.c != nil && w2.res.PID != w.res.PID {
		t.Fatalf("second window: %+v", w2.res)
	}
	cmd := waitFor(t, w.cmds, "the show command", nil)
	if cmd.Cmd != rpc.UIShow || cmd.Arg != "lanflow://import?url=x" {
		t.Fatalf("command %+v", cmd)
	}

	// The service crashes: the window starts a new one, reconnects and
	// gets every topic again.
	if err := w.c.SetMode(model.ModeGlobal); err != nil {
		t.Fatal(err)
	}
	old := w.res.PID
	if err := Kill(old); err != nil {
		t.Fatal(err)
	}
	waitFor(t, w.ups, "the connection to drop", func(up bool) bool { return !up })
	waitFor(t, w.ups, "the reconnection", func(up bool) bool { return up })
	waitFor(t, w.topics, "a settings refresh", func(tp app.Topic) bool { return tp == app.TopicSettings })
	if st := w.c.Status(); st.Core != model.CoreRunning || st.Mode != model.ModeRule {
		t.Fatalf("after the restart the window shows the new service's state: %+v", st)
	}

	// Closing the window leaves the service, the core and its
	// connections alone.
	conns := len(w.c.Connections())
	if err := w.c.DetachUI(); err != nil {
		t.Fatal(err)
	}
	w.c.Close()
	w3, err := h.connect("")
	if err != nil {
		t.Fatal(err)
	}
	if w3.res.Forwarded {
		t.Fatal("the closed window still counts as the window")
	}
	if st := w3.c.Status(); st.Core != model.CoreRunning || len(w3.c.Connections()) != conns {
		t.Fatalf("after closing the window: %+v, %d connections (want %d)", st, len(w3.c.Connections()), conns)
	}

	// 退出澜流: the service quits, tells the window, and stays down.
	pid := w3.c.Conn().Welcome().PID
	w3.holding.Store(true) // what the window does on UIQuit
	if err := w3.c.QuitService(); err != nil {
		t.Fatal(err)
	}
	cmd = waitFor(t, w3.cmds, "the quit command", nil)
	if cmd.Cmd != rpc.UIQuit {
		t.Fatalf("command %+v", cmd)
	}
	deadline := time.Now().Add(10 * time.Second)
	for Alive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if Alive(pid) {
		t.Fatal("the service did not quit")
	}
	time.Sleep(500 * time.Millisecond)
	if c, err := remote.Dial(context.Background(), remote.Options{DataDir: h.dir, Client: rpc.KindCtl}); err == nil {
		c.Close()
		t.Fatal("a service was started again after 退出澜流")
	}
}

func TestNotices(t *testing.T) {
	f := fake.NewInstant()
	n := NewNotices(f)
	now := time.Unix(1000, 0)
	n.now = func() time.Time { return now }
	var shown []app.Notice
	n.OnNew = func(x app.Notice) { shown = append(shown, x) }
	f.PushNotice(app.Notice{Kind: "core-crash", Title: "crash", System: true})
	f.PushNotice(app.Notice{Kind: "toast", Title: "hi"})
	n.Pull()
	if len(shown) != 2 {
		t.Fatalf("OnNew got %v", shown)
	}
	now = now.Add(5 * time.Minute) // the window opens later
	got := n.TakeNotices()
	if len(got) != 1 || got[0].Kind != "core-crash" {
		t.Fatalf("kept for the window: %v", got)
	}
	if len(n.TakeNotices()) != 0 || len(shown) != 2 {
		t.Fatal("notices handed out twice")
	}
	f.PushNotice(app.Notice{Kind: "toast", Title: "fresh"})
	if got := n.TakeNotices(); len(got) != 1 || len(shown) != 3 {
		t.Fatalf("a notice the window asks for first still reaches OnNew: %v %v", got, shown)
	}
}

func TestParseArgs(t *testing.T) {
	a := ParseArgs([]string{"--service", "--login", "--open=feedback", "clash://install-config?url=x"})
	if !a.Service || !a.Login || a.Open != "feedback" || a.Link != "clash://install-config?url=x" {
		t.Fatalf("%+v", a)
	}
	t.Setenv("LANFLOW_SINGLE", "")
	dir := t.TempDir()
	if SingleProcess(dir) {
		t.Fatal("single without marker")
	}
	_ = os.WriteFile(filepath.Join(dir, SingleFile), nil, 0o600)
	if !SingleProcess(dir) {
		t.Fatal("marker ignored")
	}
	t.Setenv("LANFLOW_SINGLE", "0")
	if SingleProcess(dir) {
		t.Fatal("LANFLOW_SINGLE=0 should win")
	}
}
