package helper

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"lanflow/internal/core"
	"lanflow/internal/platform/helper/protocol"
)

// dialHelper connects to the helper endpoint; tests replace it.
var dialHelper protocol.Dialer = platformDial

func newClient() *protocol.Client {
	return &protocol.Client{Dial: func(ctx context.Context) (net.Conn, error) { return dialHelper(ctx) }}
}

// pingTimeout bounds Installed.
const pingTimeout = time.Second

func installed(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	resp, err := newClient().Call(ctx, protocol.Request{Op: protocol.OpPing})
	return err == nil && resp.OK && resp.Protocol == protocol.Version
}

// waitInstalled polls ping until it answers or d elapses.
func waitInstalled(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		if installed(ctx) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("权限组件已安装但没有响应，请稍后重试或重新安装")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// waitUninstalled polls until ping stops answering.
func waitUninstalled(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	for installed(ctx) {
		if time.Now().After(deadline) {
			return errors.New("权限组件仍在运行，移除可能未完成")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return nil
}

// runner is the core.Runner that drives Mihomo through the helper.
type runner struct {
	c            *protocol.Client
	pollInterval time.Duration
	callTimeout  time.Duration

	mu      sync.Mutex
	running bool
	exited  chan error
	cancel  context.CancelFunc
}

func newRunner() core.Runner {
	return &runner{c: newClient(), pollInterval: time.Second, callTimeout: 15 * time.Second, exited: make(chan error, 1)}
}

func toWire(s core.Spec) *protocol.Spec {
	return &protocol.Spec{Bin: s.Bin, WorkDir: s.WorkDir, Config: s.Config, Controller: s.Controller, LogFile: s.LogFile, Env: s.Env}
}

func (r *runner) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, r.callTimeout)
}

// Start asks the helper to spawn Mihomo and begins polling its status.
func (r *runner) Start(ctx context.Context, spec core.Spec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopPollingLocked()
	cctx, cancel := r.withTimeout(ctx)
	defer cancel()
	if _, err := r.c.Call(cctx, protocol.Request{Op: protocol.OpStart, Spec: toWire(spec)}); err != nil {
		return wrapTransport(err)
	}
	r.running = true
	r.exited = make(chan error, 1)
	pctx, pcancel := context.WithCancel(context.Background())
	r.cancel = pcancel
	go r.poll(pctx, r.exited)
	return nil
}

// Stop stops polling (so no spurious Exited is delivered) and asks the
// helper to stop Mihomo.
func (r *runner) Stop(ctx context.Context) error {
	r.mu.Lock()
	r.stopPollingLocked()
	wasRunning := r.running
	r.running = false
	r.mu.Unlock()
	cctx, cancel := r.withTimeout(ctx)
	defer cancel()
	if _, err := r.c.Call(cctx, protocol.Request{Op: protocol.OpStop}); err != nil {
		if !wasRunning && isTransport(err) {
			// Nothing was started through this runner and the helper is
			// not reachable: there is nothing to stop.
			return nil
		}
		return wrapTransport(err)
	}
	return nil
}

func (r *runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

func (r *runner) Exited() <-chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exited
}

func (r *runner) stopPollingLocked() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

// poll asks the helper for status every pollInterval and reports an exit
// (or an unreachable helper, after three failures) once on ch.
func (r *runner) poll(ctx context.Context, ch chan error) {
	t := time.NewTicker(r.pollInterval)
	defer t.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		resp, err := r.c.Call(cctx, protocol.Request{Op: protocol.OpStatus})
		cancel()
		if ctx.Err() != nil {
			return
		}
		var exit error
		switch {
		case err != nil:
			failures++
			if failures < 3 {
				continue
			}
			exit = fmt.Errorf("权限组件无响应，Mihomo 状态未知: %w", err)
		case resp.Running:
			failures = 0
			continue
		case resp.ExitError != "":
			exit = errors.New(resp.ExitError)
		default:
			exit = errors.New("Mihomo 已退出")
		}
		r.mu.Lock()
		if ctx.Err() == nil {
			r.running = false
			r.cancel = nil
		}
		r.mu.Unlock()
		if ctx.Err() == nil {
			select {
			case ch <- exit:
			default:
			}
		}
		return
	}
}

// wrapTransport turns a connection failure into a user-facing message;
// application errors from the helper are already in Chinese.
func wrapTransport(err error) error {
	if isTransport(err) {
		return fmt.Errorf("无法连接权限组件，请重新安装权限组件后再启用 TUN 模式（%v）", err)
	}
	return err
}

func isTransport(err error) bool {
	var ne net.Error
	var opErr *net.OpError
	return errors.As(err, &opErr) || errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded)
}
