package split

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProc is a service process for StopService: which steps end it.
type fakeProc struct {
	mu    sync.Mutex
	alive bool
	// onQuit, onTerm, onKill: whether that step ends the process.
	onQuit, onTerm, onKill bool
	calls                  []string
}

func (p *fakeProc) step(name string, ends bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, name)
	if ends {
		p.alive = false
	}
}

func (p *fakeProc) steps() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

const fakePID = 4242

func useFakeProc(t *testing.T, p *fakeProc) {
	t.Helper()
	old := procOps
	procOps.alive = func(pid int) bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return pid == fakePID && p.alive
	}
	procOps.terminate = func(int) error { p.step("term", p.onTerm); return nil }
	procOps.kill = func(int) error { p.step("kill", p.onKill); return nil }
	procOps.poll = 5 * time.Millisecond
	t.Cleanup(func() { procOps = old })
}

func fastStop(p *fakeProc) StopOptions {
	return StopOptions{
		Quit:     func() error { p.step("quit", p.onQuit); return nil },
		Graceful: 50 * time.Millisecond, Term: 50 * time.Millisecond, Kill: 50 * time.Millisecond,
		Logf: func(string, ...any) {},
	}
}

func TestStopServiceSteps(t *testing.T) {
	cases := []struct {
		name string
		p    *fakeProc
		want []string
		err  bool
	}{
		{"quits when asked", &fakeProc{alive: true, onQuit: true, onTerm: true, onKill: true}, []string{"quit"}, false},
		{"ignores the quit", &fakeProc{alive: true, onTerm: true, onKill: true}, []string{"quit", "term"}, false},
		{"ignores the signal", &fakeProc{alive: true, onKill: true}, []string{"quit", "term", "kill"}, false},
		{"never ends", &fakeProc{alive: true}, []string{"quit", "term", "kill"}, true},
		{"already gone", &fakeProc{alive: false}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			useFakeProc(t, p)
			start := time.Now()
			err := StopService(fakePID, fastStop(p))
			if took := time.Since(start); took > 2*time.Second {
				t.Fatalf("took %s: the waits are not bounded", took)
			}
			if got := p.steps(); !slices.Equal(got, tc.want) {
				t.Fatalf("steps %v, want %v", got, tc.want)
			}
			if tc.err != (err != nil) {
				t.Fatalf("err %v, want error %v", err, tc.err)
			}
			if tc.err && !errors.Is(err, ErrServiceStuck) {
				t.Fatalf("err %v, want ErrServiceStuck", err)
			}
		})
	}
}

// A failed quit request (the service no longer answers) goes on to the
// signal at once, without waiting the graceful time.
func TestStopServiceQuitFails(t *testing.T) {
	p := &fakeProc{alive: true, onTerm: true}
	useFakeProc(t, p)
	o := fastStop(p)
	o.Graceful = time.Hour
	o.Quit = func() error { p.step("quit", false); return errors.New("connection closed") }
	if err := StopService(fakePID, o); err != nil {
		t.Fatal(err)
	}
	if got := p.steps(); !slices.Equal(got, []string{"quit", "term"}) {
		t.Fatalf("steps %v", got)
	}
}

// The update restart: the old service is ended, then the leftovers
// cleared, then the new version started, in that order; a service that
// will not end still lets the new version start.
func TestRestartForUpdateOrder(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(fmt.Sprintf("stuck=%v", stuck), func(t *testing.T) {
			p := &fakeProc{alive: true, onQuit: !stuck}
			useFakeProc(t, p)
			var order []string
			RestartForUpdate(UpdateRestart{
				PID:  fakePID,
				Stop: fastStop(p),
				Cleanup: func() {
					if procOps.alive(fakePID) != stuck {
						t.Errorf("cleanup ran while the service ran: %v", procOps.alive(fakePID))
					}
					order = append(order, "cleanup")
				},
				Relaunch: func() { order = append(order, "relaunch") },
			})
			if !slices.Equal(order, []string{"cleanup", "relaunch"}) {
				t.Fatalf("order %v", order)
			}
			if got := p.steps(); got[0] != "quit" {
				t.Fatalf("steps %v: the service was not asked to quit first", got)
			}
		})
	}
}

// With no service running (pid 0) the restart only cleans up and starts.
func TestRestartForUpdateNoService(t *testing.T) {
	p := &fakeProc{}
	useFakeProc(t, p)
	n := 0
	RestartForUpdate(UpdateRestart{Stop: fastStop(p), Cleanup: func() { n++ }, Relaunch: func() { n++ }})
	if n != 2 || len(p.steps()) != 0 {
		t.Fatalf("n=%d steps=%v", n, p.steps())
	}
}

// A newer window asks the older service to quit (as 退出澜流 does, so
// it restores the system proxy and stops its core) rather than only
// signalling it, and clears the leftovers before the new service starts.
func TestNewerWindowQuitsOlderServiceGracefully(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	h := newHarness(t)
	t.Setenv("LANFLOW_SPLIT_TEST_VERSION", "1.0.0")
	first, _, err := Connect(context.Background(), ConnectOptions{DataDir: h.dir, Version: "1.0.0", Start: h.start, Wait: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	old := first.Conn().Welcome().PID
	first.Close()

	t.Setenv("LANFLOW_SPLIT_TEST_VERSION", "1.1.0")
	var cleanups atomic.Int32
	c, _, err := Connect(context.Background(), ConnectOptions{DataDir: h.dir, Version: "1.1.0", Start: h.start, Wait: 15 * time.Second,
		Cleanup: func() {
			if Alive(old) {
				t.Errorf("cleanup ran while the old service %d ran", old)
			}
			cleanups.Add(1)
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if Alive(old) || c.Conn().Welcome().Version != "1.1.0" {
		t.Fatalf("the old service %d still runs, or serves: %+v", old, c.Conn().Welcome())
	}
	if n := cleanups.Load(); n != 1 {
		t.Fatalf("cleanup ran %d times, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(h.dir, fmt.Sprintf("quit-%d", old))); err != nil {
		t.Fatalf("the old service was not asked to quit: %v", err)
	}
}

// An older service that ignores the quit request and the signal is
// killed, and the new one still starts, within the bounds.
func TestNewerWindowKillsStuckOlderService(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	h := newHarness(t)
	t.Setenv("LANFLOW_SPLIT_TEST_VERSION", "1.0.0")
	t.Setenv(stubbornEnv, "1")
	first, _, err := Connect(context.Background(), ConnectOptions{DataDir: h.dir, Version: "1.0.0", Start: h.start, Wait: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	old := first.Conn().Welcome().PID
	first.Close()

	t.Setenv("LANFLOW_SPLIT_TEST_VERSION", "1.1.0")
	t.Setenv(stubbornEnv, "")
	start := time.Now()
	c, _, err := Connect(context.Background(), ConnectOptions{DataDir: h.dir, Version: "1.1.0", Start: h.start, Wait: 15 * time.Second,
		Stop: StopOptions{Graceful: 300 * time.Millisecond, Term: 300 * time.Millisecond, Kill: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if Alive(old) {
		t.Fatalf("the stuck service %d still runs", old)
	}
	if w := c.Conn().Welcome(); w.Version != "1.1.0" {
		t.Fatalf("serves %+v", w)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("took %s", took)
	}
}
