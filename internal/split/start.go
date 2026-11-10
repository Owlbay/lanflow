package split

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"lanflow/internal/app/remote"
	"lanflow/internal/i18n"
	"lanflow/internal/rpc"
)

// Start runs exe with args as a process of its own: it outlives the
// caller (a new session on Unix, a detached process on Windows), with the
// caller's environment (LANFLOW_FAKE, LANFLOW_SOCKET) and no terminal.
func Start(exe string, args ...string) (int, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = os.Environ()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }() // reap it should it end while we run
	return pid, nil
}

// StartService starts the service process of exe.
func StartService(exe string, login bool) error {
	args := []string{ServiceFlag}
	if login {
		args = append(args, LoginFlag)
	}
	_, err := Start(exe, args...)
	return err
}

// StartWindow starts a window process of exe that opens open ("" =
// nothing in particular).
func StartWindow(exe, open string) error {
	var args []string
	if open != "" {
		args = append(args, OpenFlag+open)
	}
	_, err := Start(exe, args...)
	return err
}

// ConnectOptions describe the window's connection to the service.
type ConnectOptions struct {
	DataDir string
	Version string
	// Attach is passed to AttachUI: what an existing window should open
	// when this process turns out to be the second one.
	Attach string
	// Start starts the service process when it is not running.
	Start func() error
	// Wait is how long to wait for a service started here (default 20 s).
	Wait time.Duration
	// OnUI, OnState and Hold go to the client (remote.Options.OnUI,
	// remote.Reconnect).
	OnUI    func(rpc.UICommand)
	OnState func(up bool, err error)
	Hold    func() bool
}

// ErrNotSplit: the serving process is a single-process Lanflow (an older
// version, or LANFLOW_SINGLE) and showed its own window.
var ErrNotSplit = errors.New("澜流以单进程方式运行，已显示它自己的窗口")

// Connect connects the window process to the service, starting the
// service first when it is not running, and attaches as the window. The
// result says Forwarded when another window process exists and was asked
// to show instead: the caller should then exit.
func Connect(ctx context.Context, o ConnectOptions) (*remote.Client, rpc.AttachResult, error) {
	if o.Wait == 0 {
		o.Wait = 20 * time.Second
	}
	ro := remote.Options{DataDir: o.DataDir, Client: rpc.KindUI, Version: o.Version, Cache: true, Incremental: true, OnUI: o.OnUI,
		Reconnect: &remote.Reconnect{
			Start:   o.Start,
			OnState: o.OnState,
			Hold:    o.Hold,
			// After a reconnect the window attaches again; a window that
			// another process took over meanwhile is not expected (the
			// service only forwards while this one is attached).
			Ready: func(c *remote.Client, conn *rpc.Conn) error {
				_, err := c.AttachUI(conn, "")
				return err
			},
		}}
	c, err := remote.Dial(ctx, ro)
	if errors.Is(err, rpc.ErrNotRunning) && o.Start != nil {
		if err := o.Start(); err != nil {
			return nil, rpc.AttachResult{}, fmt.Errorf("无法启动澜流后台服务: %w", err)
		}
		deadline := time.Now().Add(o.Wait)
		for {
			c, err = remote.Dial(ctx, ro)
			if err == nil || !errors.Is(err, rpc.ErrNotRunning) || time.Now().After(deadline) {
				break
			}
			select {
			case <-ctx.Done():
				return nil, rpc.AttachResult{}, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	if pid, ok := protocolMismatch(err); ok && o.Start != nil {
		// A service of another protocol version (an update replaced the
		// app while it ran): end it and start ours.
		if err := replace(pid, o.Wait); err != nil {
			return nil, rpc.AttachResult{}, err
		}
		return Connect(ctx, o)
	}
	if err != nil {
		return nil, rpc.AttachResult{}, err
	}
	res, err := c.AttachUI(nil, o.Attach)
	if err != nil {
		c.Close()
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == rpc.CodeNoMethod {
			return nil, rpc.AttachResult{}, ErrOldService
		}
		return nil, rpc.AttachResult{}, err
	}
	if res.Forwarded {
		c.Close()
		if !res.Split {
			return nil, res, ErrNotSplit
		}
	}
	return c, res, nil
}

// ErrOldService: a Lanflow from before the split serves the local
// interface (it has the window ops missing); it must be quit first.
var ErrOldService = errors.New("另一个旧版本的澜流正在运行：请先退出它，再打开澜流")

// OldServiceText is ErrOldService for the user, translated.
func OldServiceText() string {
	return i18n.T("另一个旧版本的澜流正在运行：请先退出它，再打开澜流")
}

// protocolMismatch reports a handshake refused for the protocol version,
// with the serving process.
func protocolMismatch(err error) (int, bool) {
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == rpc.CodeHandshake && re.Data != nil && re.Data.Kind == "protocol" && re.Data.PID > 0 {
		return re.Data.PID, true
	}
	return 0, false
}

// replace ends the service pid and waits for it to go.
func replace(pid int, wait time.Duration) error {
	if err := Terminate(pid); err != nil && Alive(pid) {
		return fmt.Errorf("无法结束旧的澜流后台服务: %w", err)
	}
	deadline := time.Now().Add(wait)
	for Alive(pid) {
		if time.Now().After(deadline) {
			return errors.New("旧的澜流后台服务没有退出")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// NoServiceText says, translated, that the window cannot reach a service
// that runs.
func NoServiceText(err error) string {
	return i18n.Tf("澜流后台服务正在运行，但无法连接（%s）。请在活动监视器或任务管理器中结束 Lanflow 后再打开。", err.Error())
}
