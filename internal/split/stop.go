package split

import (
	"errors"
	"fmt"
	"log"
	"time"
)

// Ending the service process before another version starts: an update
// that restarts the app (RestartForUpdate), or a window that finds an
// older service still running (Connect). The old service must be gone,
// with its core, its decryption process and its ports, before the new
// one starts: otherwise the new service finds the single-instance lock or
// the mixed port taken, or adopts the old core.

// StopOptions bound each step of StopService. Zero durations take the
// defaults.
type StopOptions struct {
	// Quit asks the service to quit as 退出澜流 does (QuitService over the
	// local interface): it restores the system proxy and DNS, stops the
	// decryption process and the core. nil skips this step.
	Quit func() error
	// Graceful is how long to wait for the service to exit after Quit
	// (default 25 s: Shutdown gives the core 10 s, the decryption process
	// 5 s).
	Graceful time.Duration
	// Term is how long to wait after Terminate (default 22 s: a signalled
	// service waits 5 s for its quit, then up to 15 s for the cleanup).
	Term time.Duration
	// Kill is how long to wait after Kill (default 5 s).
	Kill time.Duration
	// Logf logs each step (default log.Printf).
	Logf func(format string, args ...any)
}

func (o *StopOptions) defaults() {
	if o.Graceful == 0 {
		o.Graceful = 25 * time.Second
	}
	if o.Term == 0 {
		o.Term = 22 * time.Second
	}
	if o.Kill == 0 {
		o.Kill = 5 * time.Second
	}
	if o.Logf == nil {
		o.Logf = log.Printf
	}
}

// procOps are the process calls of StopService; tests replace them.
var procOps = struct {
	alive     func(pid int) bool
	terminate func(pid int) error
	kill      func(pid int) error
	poll      time.Duration
}{Alive, Terminate, Kill, 100 * time.Millisecond}

// ErrServiceStuck: the service process outlived every step of StopService.
var ErrServiceStuck = errors.New("旧的澜流后台服务没有退出")

// StopService ends the service process pid and waits until it is gone:
// first politely (o.Quit), then Terminate (SIGTERM: the service still
// restores the system proxy on the way out; on Windows this already ends
// it), then Kill. Every wait is bounded, so it never hangs; the error
// says the process still runs after all of them.
func StopService(pid int, o StopOptions) error {
	o.defaults()
	if pid <= 0 || !procOps.alive(pid) {
		return nil
	}
	if o.Quit != nil {
		if err := o.Quit(); err != nil {
			o.Logf("lanflow: ask the service %d to quit: %v", pid, err)
		} else if waitGone(pid, o.Graceful) {
			o.Logf("lanflow: the service %d quit", pid)
			return nil
		} else {
			o.Logf("lanflow: the service %d did not quit in %s; terminating it", pid, o.Graceful)
		}
	}
	if err := procOps.terminate(pid); err != nil && procOps.alive(pid) {
		o.Logf("lanflow: terminate the service %d: %v", pid, err)
	} else if waitGone(pid, o.Term) {
		o.Logf("lanflow: the service %d ended", pid)
		return nil
	} else {
		o.Logf("lanflow: the service %d did not end in %s; killing it", pid, o.Term)
	}
	if err := procOps.kill(pid); err != nil && procOps.alive(pid) {
		return fmt.Errorf("%w: %v", ErrServiceStuck, err)
	}
	if !waitGone(pid, o.Kill) {
		return ErrServiceStuck
	}
	o.Logf("lanflow: the service %d was killed", pid)
	return nil
}

// waitGone waits up to d for pid to exit, reporting whether it did.
func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for procOps.alive(pid) {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(procOps.poll)
	}
	return true
}

// UpdateRestart is what RestartForUpdate does, step by step.
type UpdateRestart struct {
	// PID is the service process (0 = none runs).
	PID  int
	Stop StopOptions
	// Cleanup ends what the old service left behind once it is gone: a
	// core it could not stop (the privileged helper's too), decryption
	// processes, and waits for the mixed port (app.StopLeftovers). It
	// must be bounded.
	Cleanup func()
	// Relaunch quits this window process and starts the new version
	// (mygo.App.Relaunch).
	Relaunch func()
}

// RestartForUpdate restarts Lanflow into the version an update just
// installed: the old service is ended (StopService) and what it left
// cleaned up before this window quits and the new version starts. A step
// that fails is logged and the restart goes on: the new version must come
// up, and it checks for leftovers again at start.
func RestartForUpdate(u UpdateRestart) {
	u.Stop.defaults()
	if err := StopService(u.PID, u.Stop); err != nil {
		u.Stop.Logf("lanflow: update restart: %v; restarting anyway", err)
	}
	if u.Cleanup != nil {
		u.Cleanup()
	}
	if u.Relaunch != nil {
		u.Relaunch()
	}
}
