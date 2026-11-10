package helper

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
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

// ExpectedVersion is the helper version this app requires. It can be set
// at build time (-ldflags "-X lanflow/internal/platform/helper.ExpectedVersion=1.2.3");
// otherwise it is learned from the bundled helper binary (SetBundledHelper,
// or Install's HelperBin). While unknown, or for "dev" builds, only the
// protocol version is compared.
var ExpectedVersion string

var (
	learnedMu      sync.Mutex
	learnedVersion string
)

// Errors returned by Check and Install.
var (
	// ErrNotRunning: nothing answers on the helper endpoint.
	ErrNotRunning = errors.New("权限组件未安装或未运行")
	// ErrProtocolMismatch: the helper speaks another protocol version.
	ErrProtocolMismatch = errors.New("权限组件协议版本与应用不一致，请重新安装权限组件")
	// ErrVersionMismatch: the helper is another release than the app ships.
	ErrVersionMismatch = errors.New("权限组件版本与应用不一致，请重新安装权限组件")
	// ErrDowngrade: Install would replace a newer helper with an older one.
	ErrDowngrade = errors.New("已安装的权限组件比应用自带的版本更新，拒绝降级")
)

// VersionError carries both versions; it matches ErrVersionMismatch or
// ErrDowngrade with errors.Is.
type VersionError struct {
	Kind      error
	Installed string
	Bundled   string
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("%v（已安装 %s，应用自带 %s）", e.Kind, e.Installed, e.Bundled)
}

func (e *VersionError) Unwrap() error { return e.Kind }

func expectedVersion() string {
	if ExpectedVersion != "" {
		return ExpectedVersion
	}
	learnedMu.Lock()
	defer learnedMu.Unlock()
	return learnedVersion
}

// versionTimeout bounds `lanflow-helper version`.
const versionTimeout = 5 * time.Second

// helperVersionOf runs `bin version` (unprivileged) and parses the
// output; tests replace it.
var helperVersionOf = func(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, err := versionCommand(ctx, bin).Output()
	if err != nil {
		return "", err
	}
	v, ok := protocol.ParseVersionLine(string(out))
	if !ok {
		return "", fmt.Errorf("无法识别权限组件版本: %q", strings.TrimSpace(string(out)))
	}
	return v, nil
}

// learnBundledVersion reads the version of the helper shipped with the
// app and remembers it as the required version.
func learnBundledVersion(ctx context.Context, bin string) (string, error) {
	v, err := helperVersionOf(ctx, bin)
	if err != nil {
		return "", err
	}
	learnedMu.Lock()
	learnedVersion = v
	learnedMu.Unlock()
	return v, nil
}

// ping asks the helper for its identity.
func ping(ctx context.Context) (protocol.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	resp, err := newClient().Call(ctx, protocol.Request{Op: protocol.OpPing})
	if err != nil {
		return resp, err
	}
	if !resp.OK {
		return resp, resp.Err()
	}
	return resp, nil
}

// check reports whether the helper answers with this app's protocol and
// (when known and comparable) its version.
func check(ctx context.Context) error {
	resp, err := ping(ctx)
	if err != nil {
		if isTransport(err) {
			return fmt.Errorf("%w: %v", ErrNotRunning, err)
		}
		// An old helper refuses our request version with an error
		// response.
		return fmt.Errorf("%w: %v", ErrProtocolMismatch, err)
	}
	if resp.Protocol != protocol.Version {
		return fmt.Errorf("%w（权限组件协议 %d，应用协议 %d）", ErrProtocolMismatch, resp.Protocol, protocol.Version)
	}
	want := expectedVersion()
	if c, ok := protocol.CompareVersions(resp.Version, want); ok && c != 0 {
		return &VersionError{Kind: ErrVersionMismatch, Installed: resp.Version, Bundled: want}
	}
	return nil
}

func installed(ctx context.Context) bool { return check(ctx) == nil }

// reachable reports whether anything answers a ping, whatever its version.
func reachable(ctx context.Context) bool {
	_, err := ping(ctx)
	return err == nil || !isTransport(err)
}

// checkDowngrade learns the bundled helper's version and refuses to
// replace a running helper that is newer.
func checkDowngrade(ctx context.Context, helperBin string) error {
	bundled, err := learnBundledVersion(ctx, helperBin)
	if err != nil {
		// Unknown bundled version: nothing to compare; the root
		// installer still checks the recorded version.
		return nil
	}
	resp, err := ping(ctx)
	if err != nil {
		return nil
	}
	if c, ok := protocol.CompareVersions(resp.Version, bundled); ok && c > 0 {
		return &VersionError{Kind: ErrDowngrade, Installed: resp.Version, Bundled: bundled}
	}
	return nil
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
	for reachable(ctx) {
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
