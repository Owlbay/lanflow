package app

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/model"
)

// leftoverSleepEnv makes the test binary a process left behind (TestMain):
// it runs until its stdin closes.
const leftoverSleepEnv = "LANFLOW_TEST_LEFTOVER_SLEEP"

// sleeper is a process of the test binary that stands for a decryption
// process left by a previous run.
type sleeper struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{}
}

func startSleeper(t *testing.T, args ...string) *sleeper {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^$"}, args...)...)
	cmd.Env = append(os.Environ(), leftoverSleepEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sl := &sleeper{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(sl.done) }() // reaped: procAlive turns false
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-sl.done
	})
	return sl
}

func (sl *sleeper) pid() int { return sl.cmd.Process.Pid }

func (sl *sleeper) exited(d time.Duration) bool {
	select {
	case <-sl.done:
		return true
	case <-time.After(d):
		return false
	}
}

// fakeAgents makes agentProc.is accept only the given PIDs as decryption
// processes (the real check reads the command line).
func fakeAgents(t *testing.T, pids ...int) {
	t.Helper()
	old := agentProc
	agentProc.is = func(pid int, _ time.Time) bool {
		for _, p := range pids {
			if p == pid {
				return true
			}
		}
		return false
	}
	agentProc.wait, agentProc.poll = 2*time.Second, 10*time.Millisecond
	t.Cleanup(func() { agentProc = old })
}

func agentRecords(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(agentsDir(dataDir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A decryption process of a previous run is ended; a recorded PID that
// now runs something else is left alone; both records are forgotten.
func TestStopLeftoversEndsCaptureAgents(t *testing.T) {
	dir := t.TempDir()
	agentProcLeft, other := startSleeper(t), startSleeper(t)
	fakeAgents(t, agentProcLeft.pid())
	recordAgent(dir, agentProcLeft.pid())
	recordAgent(dir, other.pid()) // a PID reused by another program
	recordAgent(dir, 999999999)   // a process long gone

	r := stopLeftovers(LeftoverOptions{DataDir: dir, Logf: t.Logf}, leftoverDeps{poll: time.Millisecond})
	if r.Agents != 1 {
		t.Fatalf("ended %d agents, want 1: %+v", r.Agents, r)
	}
	if !agentProcLeft.exited(3 * time.Second) {
		t.Fatal("the leftover decryption process still runs")
	}
	if other.exited(100 * time.Millisecond) {
		t.Fatal("a process that is not a decryption process was ended")
	}
	if got := agentRecords(t, dir); len(got) != 0 {
		t.Fatalf("records left: %v", got)
	}
}

// The core the previous run left (in the helper, for TUN) is stopped.
func TestStopLeftoversStopsCore(t *testing.T) {
	st := &fakeStray{found: true, tun: true}
	r := stopLeftovers(LeftoverOptions{DataDir: t.TempDir(), Logf: t.Logf}, leftoverDeps{stray: st, poll: time.Millisecond})
	if !r.Core || !r.CoreTUN || r.CoreErr != nil || st.stops != 1 {
		t.Fatalf("report %+v, stops %d", r, st.stops)
	}
	// Nothing left: nothing stopped.
	r = stopLeftovers(LeftoverOptions{DataDir: t.TempDir(), Logf: t.Logf}, leftoverDeps{stray: st, poll: time.Millisecond})
	if r.Core || st.stops != 1 {
		t.Fatalf("report %+v, stops %d", r, st.stops)
	}
}

// The mixed port is waited for while it is being released; one another
// program holds is reported with that program, within the bound.
func TestStopLeftoversPort(t *testing.T) {
	var checks atomic.Int32
	releasing := func(int, bool) bool { return checks.Add(1) < 4 }
	r := stopLeftovers(LeftoverOptions{DataDir: t.TempDir(), MixedPort: 7890, PortWait: 2 * time.Second, Logf: t.Logf},
		leftoverDeps{portInUse: releasing, portOwner: func(int) string { return "never asked" }, poll: time.Millisecond})
	if r.PortBusy || r.PortOwner != "" {
		t.Fatalf("a released port reported busy: %+v", r)
	}

	start := time.Now()
	var asked int
	r = stopLeftovers(LeftoverOptions{DataDir: t.TempDir(), MixedPort: 7890, PortWait: 100 * time.Millisecond, Logf: t.Logf},
		leftoverDeps{portInUse: func(int, bool) bool { return true }, portOwner: func(p int) string { asked = p; return "ClashX (PID 77)" }, poll: time.Millisecond})
	if took := time.Since(start); took > time.Second {
		t.Fatalf("waited %s for a port that stays taken", took)
	}
	if !r.PortBusy || r.PortOwner != "ClashX (PID 77)" || asked != 7890 {
		t.Fatalf("report %+v (asked %d)", r, asked)
	}
}

// A service that starts ends the decryption processes of a run that did
// not exit cleanly (nothing adopts them).
func TestServiceStartEndsCaptureAgentLeftovers(t *testing.T) {
	sl := startSleeper(t)
	fakeAgents(t, sl.pid())
	h := newHarness(t, func(h *harness) { recordAgent(h.dir, sl.pid()) })
	if !sl.exited(3 * time.Second) {
		t.Fatal("the leftover decryption process still runs after the service started")
	}
	if got := agentRecords(t, h.dir); len(got) != 0 {
		t.Fatalf("records left: %v", got)
	}
}

// Quitting stops the decryption process before the core, and puts the
// system proxy back first.
func TestShutdownOrder(t *testing.T) {
	h := newHarness(t, nil)
	s := h.s
	if err := s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	sl := startSleeper(t)
	run := &captureRun{cmd: sl.cmd, stdin: sl.stdin, done: sl.done}
	s.capst.mu.Lock()
	s.capst.run, s.capst.enabled = run, true
	s.capst.mu.Unlock()

	var mu sync.Mutex
	var agentGone, proxyOff bool
	h.mgr.mu.Lock()
	h.mgr.onStop = func() {
		mu.Lock()
		defer mu.Unlock()
		select {
		case <-sl.done:
			agentGone = true
		default:
		}
		h.sys.mu.Lock()
		proxyOff = !h.sys.state.Enabled
		h.sys.mu.Unlock()
	}
	h.mgr.mu.Unlock()
	s.Shutdown()
	mu.Lock()
	defer mu.Unlock()
	if !agentGone {
		t.Fatal("the core was stopped while the decryption process still ran")
	}
	if !proxyOff {
		t.Fatal("the core was stopped while the system proxy still pointed at it")
	}
}

// A core that outlives its stop (its port still taken) is stopped again
// through the PID file or the helper; a port another program keeps does
// not hold up the quit.
func TestShutdownStopsCoreThatOutlivedStop(t *testing.T) {
	old := coreGoneWait
	coreGoneWait = 200 * time.Millisecond
	defer func() { coreGoneWait = old }()

	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	// The stop "succeeds" but the core still runs and holds its port.
	h.stray.mu.Lock()
	h.stray.found = true
	h.stray.mu.Unlock()
	h.s.d.portInUse = func(int, bool) bool {
		h.stray.mu.Lock()
		defer h.stray.mu.Unlock()
		return h.stray.found
	}
	h.s.Shutdown()
	if h.stray.stops != 1 {
		t.Fatalf("the core that outlived its stop was stopped %d times, want 1", h.stray.stops)
	}

	h = newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.s.d.portInUse = func(int, bool) bool { return true } // someone else's now
	start := time.Now()
	h.s.Shutdown()
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("quit took %s with the port taken", took)
	}
}

// A mixed port another program holds is reported with that program.
func TestStartCorePortTakenNamesOwner(t *testing.T) {
	h := newHarness(t, nil)
	port := h.s.Settings().MixedPort
	h.taken[port] = true
	h.s.d.portOwner = func(p int) string {
		if p == port {
			return "ClashX (PID 77)"
		}
		return ""
	}
	err := h.s.StartCore()
	if err == nil || !strings.Contains(err.Error(), "ClashX (PID 77)") {
		t.Fatalf("err %v", err)
	}
	if st := h.s.Status(); st.Core != model.CoreError || !strings.Contains(st.CoreError, "ClashX") {
		t.Fatalf("status %+v", st)
	}
	// Unknown owner: the general explanation.
	h.s.d.portOwner = func(int) string { return "" }
	if err := h.s.StartCore(); err == nil || !strings.Contains(err.Error(), "已被其他程序占用") {
		t.Fatalf("err %v", err)
	}
}

func TestParseNetstat(t *testing.T) {
	out := `
Active Connections

  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1100
  TCP    127.0.0.1:17890        0.0.0.0:0              LISTENING       5
  TCP    127.0.0.1:7890         127.0.0.1:50000        ESTABLISHED     9
  TCP    127.0.0.1:7890         0.0.0.0:0              侦听            4321
  TCP    [::]:7890              [::]:0                 LISTENING       4321
`
	if got := parseNetstat(out, 7890); got != 4321 {
		t.Fatalf("pid %d, want 4321", got)
	}
	if got := parseNetstat(out, 9090); got != 0 {
		t.Fatalf("pid %d, want 0", got)
	}
}

func TestAgentRecordForgotten(t *testing.T) {
	dir := t.TempDir()
	forget := recordAgent(dir, 1234)
	if _, err := os.Stat(filepath.Join(agentsDir(dir), "1234")); err != nil {
		t.Fatal(err)
	}
	forget()
	if got := agentRecords(t, dir); len(got) != 0 {
		t.Fatalf("records left: %v", got)
	}
}
