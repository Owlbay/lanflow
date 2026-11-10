package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"lanflow/internal/platform/helper/protocol"
)

// Process is a running Mihomo as seen by the supervisor.
type Process interface {
	Pid() int
	// Wait blocks until the process exits and returns its exit error.
	Wait() error
	// Interrupt asks the process to exit gracefully (SIGINT on unix).
	Interrupt() error
	Kill() error
}

// Command describes the process to start.
type Command struct {
	Path string
	Args []string
	Dir  string
	Env  []string
	// Output receives stdout and stderr.
	Output io.Writer
}

// StartFunc spawns a process. The default is exec-based; tests inject a
// fake.
type StartFunc func(ctx context.Context, cmd Command) (Process, error)

// Hooks are platform actions around a run. Nil fields are skipped.
type Hooks struct {
	// OpenLog opens the Mihomo log for appending (rotating first). The
	// default rotates at 5 MB keeping one old file and hands ownership of
	// the file to the user on unix.
	OpenLog func(path string, uid string) (io.WriteCloser, error)
	// AfterStart runs in the background once the process is spawned, e.g.
	// to hand the controller socket to the user when it appears.
	AfterStart func(ctx context.Context, spec protocol.Spec, uid string)
	// AfterExit runs once the process has exited.
	AfterExit func(spec protocol.Spec, uid string)
}

// StopGrace is how long Stop waits after Interrupt before killing.
const StopGrace = 5 * time.Second

// LogRotateSize is the size at which the Mihomo log is rotated.
const LogRotateSize = 5 << 20

// Supervisor runs at most one Mihomo process.
type Supervisor struct {
	Policy *Policy
	Start  StartFunc
	Hooks  Hooks
	Logf   func(format string, args ...any)

	mu       sync.Mutex
	proc     Process
	spec     protocol.Spec
	log      io.WriteCloser
	done     chan struct{} // closed when the current process has exited
	stopping bool
	exitErr  string
	cancel   context.CancelFunc
}

// NewSupervisor returns a supervisor with platform defaults.
func NewSupervisor(p *Policy) *Supervisor {
	return &Supervisor{Policy: p, Start: execStart, Hooks: defaultHooks(), Logf: func(string, ...any) {}}
}

// Running reports whether a process is alive.
func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proc != nil
}

// Status returns the current state for the status op.
func (s *Supervisor) Status() protocol.Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := protocol.Response{OK: true, Running: s.proc != nil, ExitError: s.exitErr}
	if s.proc != nil {
		r.PID = s.proc.Pid()
	}
	return r
}

// StartCore validates spec and spawns Mihomo. It fails when a process is
// already running.
func (s *Supervisor) StartCore(ctx context.Context, spec protocol.Spec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc != nil {
		return errors.New("Mihomo 内核已在权限组件中运行，请先停止")
	}
	resolved, err := s.Policy.Validate(spec)
	if err != nil {
		return err
	}
	uid := s.Policy.Config.UID
	openLog := s.Hooks.OpenLog
	if openLog == nil {
		openLog = openLogFile
	}
	logw, err := openLog(resolved.LogFile, uid)
	if err != nil {
		return fmt.Errorf("无法打开日志文件: %w", err)
	}
	cmd := Command{
		Path:   resolved.Bin,
		Args:   append([]string{"-d", resolved.WorkDir, "-f", resolved.Config}, controllerArgs(resolved.Controller)...),
		Dir:    resolved.WorkDir,
		Env:    append(baseEnv(uid, resolved), resolved.Env...),
		Output: logw,
	}
	runCtx, cancel := context.WithCancel(context.Background())
	proc, err := s.Start(runCtx, cmd)
	if err != nil {
		cancel()
		_ = logw.Close()
		return fmt.Errorf("启动 Mihomo 失败: %w", err)
	}
	s.proc, s.spec, s.log, s.cancel = proc, resolved, logw, cancel
	s.exitErr = ""
	s.stopping = false
	s.done = make(chan struct{})
	s.Logf("started mihomo pid=%d", proc.Pid())
	if s.Hooks.AfterStart != nil {
		go s.Hooks.AfterStart(runCtx, resolved, uid)
	}
	go s.wait(proc, s.done)
	return nil
}

func (s *Supervisor) wait(proc Process, done chan struct{}) {
	err := proc.Wait()
	s.mu.Lock()
	if s.proc == proc {
		if !s.stopping {
			if err != nil {
				s.exitErr = "Mihomo 意外退出: " + err.Error()
			} else {
				s.exitErr = "Mihomo 意外退出（退出码 0）"
			}
			s.Logf("mihomo exited unexpectedly: %v", err)
		} else {
			s.Logf("mihomo stopped")
		}
		spec := s.spec
		if s.log != nil {
			_ = s.log.Close()
		}
		if s.cancel != nil {
			s.cancel()
		}
		s.proc, s.log, s.cancel = nil, nil, nil
		s.mu.Unlock()
		if s.Hooks.AfterExit != nil {
			s.Hooks.AfterExit(spec, s.Policy.Config.UID)
		}
	} else {
		s.mu.Unlock()
	}
	close(done)
}

// StopCore stops the process gracefully, killing it after StopGrace.
// It is a no-op when nothing runs.
func (s *Supervisor) StopCore(ctx context.Context) error {
	s.mu.Lock()
	proc, done := s.proc, s.done
	if proc == nil {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	s.mu.Unlock()

	if err := proc.Interrupt(); err != nil {
		s.Logf("interrupt: %v", err)
	}
	timer := time.NewTimer(StopGrace)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	s.Logf("mihomo did not exit after %s, killing", StopGrace)
	if err := proc.Kill(); err != nil {
		s.Logf("kill: %v", err)
	}
	select {
	case <-done:
		return nil
	case <-time.After(StopGrace):
		return errors.New("无法结束 Mihomo 进程")
	}
}

// Shutdown stops a running process; called when the helper exits.
func (s *Supervisor) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*StopGrace)
	defer cancel()
	if err := s.StopCore(ctx); err != nil {
		s.Logf("shutdown: %v", err)
	}
}

// NewHandler maps protocol requests onto the supervisor.
func NewHandler(s *Supervisor, version string) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		switch req.Op {
		case protocol.OpPing:
			return protocol.Response{OK: true, Version: version, Protocol: protocol.Version, CorePath: s.Policy.Config.Core, Running: s.Running()}
		case protocol.OpStart:
			if req.Spec == nil {
				return protocol.Response{Error: "start 请求缺少 spec"}
			}
			if err := s.StartCore(ctx, *req.Spec); err != nil {
				s.Logf("start refused: %v", err)
				return protocol.Response{Error: err.Error()}
			}
			return s.Status()
		case protocol.OpStop:
			if err := s.StopCore(ctx); err != nil {
				return protocol.Response{Error: err.Error()}
			}
			return s.Status()
		case protocol.OpStatus:
			return s.Status()
		default:
			return protocol.Response{Error: fmt.Sprintf("未知操作: %q", req.Op)}
		}
	}
}
