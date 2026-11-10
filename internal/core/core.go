// Package core runs the Mihomo process.
//
// Exported names and signatures are a stable contract; the bodies live in
// this and other files of this package.
//
// Design:
//   - Mihomo is started with `-d <workDir> -f <config>` plus
//     `-ext-ctl-unix <sock>` (macOS/Linux) or `-ext-ctl-pipe <name>`
//     (Windows); no TCP controller port, no secret.
//   - Who launches the process is abstracted by Runner: ExecRunner starts it
//     as the current user (system proxy mode); the platform helper provides a
//     privileged Runner (TUN mode) via package platform/helper.
//   - Manager owns lifecycle: one start/stop at a time, readiness (GET
//     /version over the controller succeeds), unexpected-exit detection.
package core

import (
	"context"
	"errors"
	"net"
	"time"
)

// ErrNotFound means no usable Mihomo binary was found.
var ErrNotFound = errors.New("未找到 Mihomo 内核")

// Spec describes one Mihomo run.
type Spec struct {
	Bin        string   // absolute path of the mihomo binary
	WorkDir    string   // -d; holds geodata, cache.db, providers
	Config     string   // -f; absolute path of the runtime config
	Controller string   // unix socket path (darwin/linux) or pipe name like `\\.\pipe\lanflow-<user>` (windows)
	LogFile    string   // stdout+stderr appended here (rotated by the runner at 5 MB, keep 1 old file)
	Env        []string // extra environment, KEY=VALUE
}

// Runner starts and stops one Mihomo process at a time.
type Runner interface {
	Start(ctx context.Context, spec Spec) error // returns once the process is spawned
	Stop(ctx context.Context) error             // graceful (SIGINT / CTRL_BREAK), then kill after 5 s
	Running() bool
	// Exited delivers an error when the process exits without Stop being
	// called. The channel is replaced on every Start; read it after Start.
	Exited() <-chan error
}

// NewExecRunner returns a Runner that starts Mihomo as the current user.
func NewExecRunner() Runner { return newExecRunner() }

// FindBinary returns the Mihomo binary to use: custom if non-empty (must be
// an executable file), else the bundled one (inside the app bundle's
// Resources / next to the executable / ./bin during development), else ErrNotFound.
func FindBinary(custom string) (string, error) { return findBinary(custom) }

// Version runs `mihomo -v` and returns the version, e.g. "v1.19.33".
func Version(ctx context.Context, bin string) (string, error) { return version(ctx, bin) }

// Validate runs `mihomo -t -d workDir -f config` and returns nil if the
// config is valid, else an error whose message is the core's explanation
// (trimmed to the relevant lines).
func Validate(ctx context.Context, bin, workDir, config string) error {
	return validate(ctx, bin, workDir, config)
}

// ControllerAddress returns the controller endpoint to use for this user:
// a short unix socket path under os.TempDir (macOS paths must stay < 104
// bytes) or a per-user pipe name on Windows.
func ControllerAddress() string { return controllerAddress() }

// Dial connects to a controller endpoint returned by ControllerAddress
// (unix socket or named pipe). Used by package controller.
func Dial(ctx context.Context, address string) (net.Conn, error) { return dial(ctx, address) }

// Manager owns the core lifecycle.
type Manager struct{ m *manager }

// Options configure a Manager.
type Options struct {
	Runner       Runner
	ReadyTimeout time.Duration // default 15 s
	// Ready is called with the controller address and must return nil once
	// the controller answers (the app passes a controller.Client.Version call).
	Ready func(ctx context.Context) error
}

// NewManager returns a Manager.
func NewManager(o Options) *Manager { return &Manager{newManager(o)} }

// Start starts the core with spec and waits until Ready succeeds. Starting
// while running restarts. Concurrent calls are serialized.
func (m *Manager) Start(ctx context.Context, spec Spec) error { return m.m.start(ctx, spec) }

// Stop stops the core; no-op when stopped.
func (m *Manager) Stop(ctx context.Context) error { return m.m.stop(ctx) }

// SetRunner switches runner (exec vs privileged helper) for the next Start.
func (m *Manager) SetRunner(r Runner) { m.m.setRunner(r) }

// Running reports whether the core is running.
func (m *Manager) Running() bool { return m.m.running() }

// Crashes delivers an error each time the running core exits unexpectedly.
func (m *Manager) Crashes() <-chan error { return m.m.crashes() }
