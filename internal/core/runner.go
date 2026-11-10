package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// stopGrace is how long Stop waits after the graceful signal before killing.
const stopGrace = 5 * time.Second

// unixSocketPathLimit is the usable length of sun_path on macOS (104 bytes
// including the NUL) and the lower bound we accept everywhere. Declared here
// (not in the unix file) so tests compile on every platform.
const unixSocketPathLimit = 100

// execRunner starts Mihomo as a plain child process of the current user.
// It only ever manages the process it spawned itself.
type execRunner struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	spec     Spec
	logf     *rotatingFile
	done     chan struct{} // closed when Wait returns
	exited   chan error    // replaced on every Start
	stopping bool          // Stop was called; the exit is expected
}

func newExecRunner() Runner {
	return &execRunner{exited: make(chan error, 1)}
}

// buildArgs returns the Mihomo command line for spec (without argv[0]).
func buildArgs(spec Spec) []string {
	args := []string{"-d", spec.WorkDir, "-f", spec.Config}
	return append(args, controllerArgs(spec)...)
}

func (r *execRunner) Start(ctx context.Context, spec Spec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runningLocked() {
		return errors.New("Mihomo 内核已在运行，请先停止")
	}
	if err := checkSpec(spec); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(spec.WorkDir, 0o755); err != nil {
		return fmt.Errorf("无法创建内核工作目录 %s：%w", spec.WorkDir, err)
	}
	killOrphan(spec.WorkDir)
	if err := prepareController(spec); err != nil {
		return err
	}
	logf, err := openRotatingFile(spec.LogFile, logMaxSize)
	if err != nil {
		return fmt.Errorf("无法打开内核日志 %s：%w", spec.LogFile, err)
	}

	cmd := exec.Command(spec.Bin, buildArgs(spec)...)
	cmd.Dir = spec.WorkDir
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = sysProcAttr()
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return fmt.Errorf("无法启动 Mihomo 内核 %s：%w", spec.Bin, err)
	}

	writePID(spec.WorkDir, cmd.Process.Pid)
	r.cmd = cmd
	r.spec = spec
	r.logf = logf
	r.stopping = false
	r.done = make(chan struct{})
	r.exited = make(chan error, 1)
	go r.wait(cmd, logf, spec, r.done, r.exited)
	return nil
}

// wait reaps the child and reports unexpected exits.
func (r *execRunner) wait(cmd *exec.Cmd, logf *rotatingFile, spec Spec, done chan struct{}, exited chan error) {
	err := cmd.Wait()
	_ = logf.Close()
	cleanupController(spec)
	removePID(spec.WorkDir)

	r.mu.Lock()
	stopping := r.stopping
	close(done)
	r.mu.Unlock()

	if !stopping {
		exited <- exitError(err, spec.LogFile)
	}
}

// exitError shapes a Wait error into a user-facing message with log tail.
func exitError(waitErr error, logFile string) error {
	reason := "退出码 0"
	if waitErr != nil {
		reason = waitErr.Error()
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			reason = ee.ProcessState.String()
		}
	}
	msg := fmt.Sprintf("Mihomo 内核意外退出（%s）", reason)
	if tail := tailLines(logFile, logTailLines); tail != "" {
		msg += "，最近日志：\n" + tail
	}
	return errors.New(msg)
}

func (r *execRunner) Stop(ctx context.Context) error {
	r.mu.Lock()
	cmd, done := r.cmd, r.done
	if cmd == nil {
		r.mu.Unlock()
		return nil
	}
	r.stopping = true
	r.mu.Unlock()

	select {
	case <-done:
	default:
		if err := interrupt(cmd.Process); err != nil {
			_ = cmd.Process.Kill()
		}
	}

	timer := time.NewTimer(stopGrace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		_ = cmd.Process.Kill()
		<-done
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
	}

	r.mu.Lock()
	if r.cmd == cmd {
		r.cmd = nil
		r.logf = nil
	}
	r.mu.Unlock()
	return nil
}

func (r *execRunner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runningLocked()
}

func (r *execRunner) runningLocked() bool {
	if r.cmd == nil {
		return false
	}
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

func (r *execRunner) Exited() <-chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exited
}

// checkSpec validates the parts of a Spec every runner needs.
func checkSpec(spec Spec) error {
	switch {
	case strings.TrimSpace(spec.Bin) == "":
		return fmt.Errorf("%w：未指定内核路径", ErrNotFound)
	case spec.WorkDir == "" || !filepath.IsAbs(spec.WorkDir):
		return fmt.Errorf("内核工作目录必须是绝对路径：%q", spec.WorkDir)
	case spec.Config == "" || !filepath.IsAbs(spec.Config):
		return fmt.Errorf("内核配置文件必须是绝对路径：%q", spec.Config)
	case spec.Controller == "":
		return errors.New("未指定内核控制器地址")
	case spec.LogFile == "" || !filepath.IsAbs(spec.LogFile):
		return fmt.Errorf("内核日志文件必须是绝对路径：%q", spec.LogFile)
	}
	if _, err := os.Stat(spec.Config); err != nil {
		return fmt.Errorf("内核配置文件不可用 %s：%w", spec.Config, err)
	}
	return nil
}
