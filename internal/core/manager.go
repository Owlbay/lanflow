package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	defaultReadyTimeout = 15 * time.Second
	readyPollInterval   = 200 * time.Millisecond
	readyProbeTimeout   = 2 * time.Second
	setRunnerStopGrace  = stopGrace + 5*time.Second
)

// manager implements Manager: one start/stop at a time, readiness polling,
// crash forwarding.
type manager struct {
	op sync.Mutex // serializes start / stop / setRunner

	mu        sync.Mutex // guards the fields below
	runner    Runner
	isRunning bool
	stopWatch chan struct{} // closed to end the crash watcher of the current run

	readyTimeout time.Duration
	ready        func(ctx context.Context) error
	crashCh      chan error
}

func newManager(o Options) *manager {
	m := &manager{
		runner:       o.Runner,
		readyTimeout: o.ReadyTimeout,
		ready:        o.Ready,
		crashCh:      make(chan error, 8),
	}
	if m.runner == nil {
		m.runner = newExecRunner()
	}
	if m.readyTimeout <= 0 {
		m.readyTimeout = defaultReadyTimeout
	}
	return m
}

func (m *manager) start(ctx context.Context, spec Spec) error {
	m.op.Lock()
	defer m.op.Unlock()

	if err := m.stopLocked(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	runner := m.runner
	m.mu.Unlock()

	if err := runner.Start(ctx, spec); err != nil {
		return err
	}
	exited := runner.Exited()

	if err := m.waitReady(ctx, spec, exited); err != nil {
		// Process still alive but not usable (timeout / ctx cancelled): tear
		// it down so the caller sees a clean "stopped" state.
		stopCtx, cancel := context.WithTimeout(context.Background(), setRunnerStopGrace)
		_ = runner.Stop(stopCtx)
		cancel()
		return err
	}

	stopWatch := make(chan struct{})
	m.mu.Lock()
	m.isRunning = true
	m.stopWatch = stopWatch
	m.mu.Unlock()
	go m.watch(exited, stopWatch)
	return nil
}

// waitReady polls the ready probe until it succeeds, the process exits, the
// timeout elapses or ctx is cancelled. A process exit returns the runner's
// error (with log tail) directly.
func (m *manager) waitReady(ctx context.Context, spec Spec, exited <-chan error) error {
	probe := m.ready
	if probe == nil {
		probe = func(ctx context.Context) error { return probeController(ctx, spec.Controller) }
	}
	deadline := time.NewTimer(m.readyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(readyPollInterval)
	defer tick.Stop()

	// Probe right away so a fast core does not pay a full poll interval;
	// afterwards every readyPollInterval.
	first := make(chan struct{}, 1)
	first <- struct{}{}

	var lastErr error
	for {
		select {
		case <-first:
			pctx, cancel := context.WithTimeout(ctx, readyProbeTimeout)
			err := probe(pctx)
			cancel()
			if err == nil {
				return nil
			}
			lastErr = err
		case <-ctx.Done():
			return fmt.Errorf("启动内核被取消：%w", ctx.Err())
		case err := <-exited:
			if err == nil {
				err = errors.New("Mihomo 内核在就绪前退出")
			}
			return err
		case <-deadline.C:
			msg := fmt.Sprintf("Mihomo 内核在 %s 内未就绪", m.readyTimeout.Round(time.Second))
			if lastErr != nil {
				msg += fmt.Sprintf("（最后一次探测：%v）", lastErr)
			}
			if tail := tailLines(spec.LogFile, logTailLines); tail != "" {
				msg += "，最近日志：\n" + tail
			}
			return errors.New(msg)
		case <-tick.C:
			pctx, cancel := context.WithTimeout(ctx, readyProbeTimeout)
			err := probe(pctx)
			cancel()
			if err == nil {
				return nil
			}
			lastErr = err
		}
	}
}

// watch forwards an unexpected exit of the current run to Crashes and marks
// the manager stopped. It ends silently when stop closes stopWatch.
func (m *manager) watch(exited <-chan error, stopWatch <-chan struct{}) {
	select {
	case <-stopWatch:
	case err, ok := <-exited:
		if !ok {
			return
		}
		m.mu.Lock()
		if m.stopWatch != nil {
			select {
			case <-m.stopWatch:
				// stop raced with the exit; it owns the state now
				m.mu.Unlock()
				return
			default:
			}
		}
		m.isRunning = false
		m.stopWatch = nil
		m.mu.Unlock()
		if err == nil {
			err = errors.New("Mihomo 内核意外退出")
		}
		select {
		case m.crashCh <- err:
		default: // nobody is draining; drop rather than block the watcher
		}
	}
}

func (m *manager) stop(ctx context.Context) error {
	m.op.Lock()
	defer m.op.Unlock()
	return m.stopLocked(ctx)
}

// stopLocked stops the current run; caller holds op.
func (m *manager) stopLocked(ctx context.Context) error {
	m.mu.Lock()
	runner := m.runner
	stopWatch := m.stopWatch
	m.isRunning = false
	m.stopWatch = nil
	m.mu.Unlock()
	if stopWatch != nil {
		close(stopWatch)
	}
	if !runner.Running() && stopWatch == nil {
		return nil
	}
	if err := runner.Stop(ctx); err != nil {
		return fmt.Errorf("停止 Mihomo 内核失败：%w", err)
	}
	return nil
}

func (m *manager) setRunner(r Runner) {
	if r == nil {
		return
	}
	m.op.Lock()
	defer m.op.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), setRunnerStopGrace)
	defer cancel()
	_ = m.stopLocked(ctx)
	m.mu.Lock()
	m.runner = r
	m.mu.Unlock()
}

func (m *manager) running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isRunning && m.runner.Running()
}

func (m *manager) crashes() <-chan error { return m.crashCh }

// probeController performs GET /version over the controller endpoint and
// succeeds on HTTP 200. It is the default readiness check.
func probeController(ctx context.Context, address string) error {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(ctx, address)
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://mihomo/version", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("控制器返回 HTTP %d", resp.StatusCode)
	}
	return nil
}
