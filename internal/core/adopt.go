package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Taking over a core that outlived the Lanflow that started it: the
// service process crashed or was killed while Mihomo kept running, as a
// child (the PID file names it) or in the privileged helper. The next
// service adopts it, so the proxy, TUN and the connections carry on
// (docs/service-split.md S3).

// Adopter is a Runner that can take over a process already running for
// spec instead of starting one.
type Adopter interface {
	Adopt(ctx context.Context, spec Spec) error
	// Release forgets an adopted process without stopping it.
	Release()
}

// ErrNothingToAdopt: no process of spec runs.
var ErrNothingToAdopt = errors.New("没有可接管的 Mihomo 内核")

// OrphanRunning reports whether the PID file in workDir names a running
// Mihomo (left by a previous Lanflow).
func OrphanRunning(workDir string) bool { return orphanPID(workDir) > 0 }

// StopOrphan stops the Mihomo the PID file in workDir names, if any.
func StopOrphan(workDir string) { killOrphan(workDir) }

// orphanPID is the PID of the running Mihomo the PID file names; 0 when none.
func orphanPID(workDir string) int {
	data, err := os.ReadFile(filepath.Join(workDir, pidFileName))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return 0
	}
	name := processName(pid)
	if name == "" || !strings.Contains(strings.ToLower(filepath.Base(name)), "mihomo") {
		return 0
	}
	return pid
}

// adoptPoll is how often an adopted process (not a child: no Wait) is
// checked.
var adoptPoll = 500 * time.Millisecond

// Adopt takes over the Mihomo the PID file in spec.WorkDir names.
func (r *execRunner) Adopt(ctx context.Context, spec Spec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runningLocked() {
		return errors.New("Mihomo 内核已在运行，请先停止")
	}
	pid := orphanPID(spec.WorkDir)
	if pid == 0 {
		return ErrNothingToAdopt
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	trackServer(pid)
	r.cmd = nil
	r.adopted = p
	r.spec = spec
	r.stopping = false
	r.done = make(chan struct{})
	r.exited = make(chan error, 1)
	go r.watchAdopted(pid, spec, adoptPoll, r.done, r.exited)
	return nil
}

// watchAdopted reports the end of an adopted process.
func (r *execRunner) watchAdopted(pid int, spec Spec, every time.Duration, done chan struct{}, exited chan error) {
	for processName(pid) != "" {
		time.Sleep(every)
	}
	untrackServer(pid)
	cleanupController(spec)
	removePID(spec.WorkDir)
	r.mu.Lock()
	stopping := r.stopping
	close(done)
	r.mu.Unlock()
	if !stopping {
		exited <- exitError(errors.New("进程已结束"), spec.LogFile)
	}
}

// stopAdopted stops an adopted process: the graceful signal, then kill.
func (r *execRunner) stopAdopted(ctx context.Context, p *os.Process, done chan struct{}) error {
	select {
	case <-done:
	default:
		if err := interrupt(p); err != nil {
			_ = p.Kill()
		}
	}
	timer := time.NewTimer(stopGrace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		_ = p.Kill()
		<-done
	case <-ctx.Done():
		_ = p.Kill()
		<-done
	}
	r.mu.Lock()
	if r.adopted == p {
		r.adopted = nil
	}
	r.mu.Unlock()
	return nil
}

func (m *manager) adopt(ctx context.Context, spec Spec, r Runner) error {
	a, ok := r.(Adopter)
	if !ok {
		return fmt.Errorf("%T 不能接管运行中的内核", r)
	}
	m.op.Lock()
	defer m.op.Unlock()
	if err := m.stopLocked(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	m.runner = r
	m.mu.Unlock()
	if err := a.Adopt(ctx, spec); err != nil {
		return err
	}
	exited := r.Exited()
	// It must answer on our controller: that, more than the PID, says it
	// is the core of this data directory.
	probe := m.ready
	if probe == nil {
		probe = func(ctx context.Context) error { return probeController(ctx, spec.Controller) }
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err := probe(pctx)
	cancel()
	if err != nil {
		// Not ours to keep: let go without stopping it (the caller decides).
		a.Release()
		return fmt.Errorf("运行中的内核没有应答控制接口：%w", err)
	}
	stopWatch := make(chan struct{})
	m.mu.Lock()
	m.isRunning = true
	m.stopWatch = stopWatch
	m.mu.Unlock()
	go m.watch(exited, stopWatch)
	return nil
}

// Release forgets an adopted process without stopping it.
func (r *execRunner) Release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopping = true
	r.adopted = nil
}
