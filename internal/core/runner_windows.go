//go:build windows

package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"lanflow/internal/platform/helper/service"
)

// controllerArgs tells Mihomo to serve its controller on a named pipe.
func controllerArgs(spec Spec) []string {
	return []string{"-ext-ctl-pipe", spec.Controller}
}

// prepareController: named pipes vanish with their server, nothing to clean.
func prepareController(Spec) error { return nil }

func cleanupController(Spec) {}

// sysProcAttr hides the console window and puts the child in its own
// process group so CTRL_BREAK can be delivered to it alone.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole     = kernel32.NewProc("AttachConsole")
	procFreeConsole       = kernel32.NewProc("FreeConsole")
	procSetConsoleCtrlHdl = kernel32.NewProc("SetConsoleCtrlHandler")
)

// interrupt sends CTRL_BREAK_EVENT to the child's process group. The Go
// runtime inside Mihomo turns it into os.Interrupt, which Mihomo handles as
// a graceful shutdown. The event can only be generated from a process that
// shares the child's console, and a GUI app has none, so we temporarily
// attach to the child's hidden console, ignore the event ourselves, fire it,
// and detach again. Any failure is reported so the caller falls back to Kill.
func interrupt(p *os.Process) error {
	pid := uint32(p.Pid)
	if r, _, err := procAttachConsole.Call(uintptr(pid)); r == 0 {
		return fmt.Errorf("AttachConsole: %w", err)
	}
	defer procFreeConsole.Call()
	// Ignore the event in our own process (NULL handler + add=TRUE).
	if r, _, err := procSetConsoleCtrlHdl.Call(0, 1); r == 0 {
		return fmt.Errorf("SetConsoleCtrlHandler: %w", err)
	}
	defer procSetConsoleCtrlHdl.Call(0, 0)
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, pid); err != nil {
		return fmt.Errorf("GenerateConsoleCtrlEvent: %w", err)
	}
	return nil
}

// controllerAddress returns the per-user pipe name, e.g. \\.\pipe\lanflow-alice.
func controllerAddress() string {
	return `\\.\pipe\lanflow-` + currentUserName()
}

func currentUserName() string {
	name := os.Getenv("USERNAME")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
			if i := strings.LastIndexAny(name, `\/`); i >= 0 {
				name = name[i+1:]
			}
		}
	}
	if name == "" {
		name = "default"
	}
	// Pipe names may not contain backslashes; keep the rest conservative too.
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, name)
}

// dial connects to the controller named pipe and checks who serves it.
// Pipe names are global: a process of another user (or any process that
// got there first) could create \\.\pipe\lanflow-<user> and answer the
// controller calls in place of Mihomo. Like the helper client
// (internal/platform/helper/dial_windows.go), the server process is looked
// up with GetNamedPipeServerProcessId and must be the core this app
// started itself, or the core the helper runs as LocalSystem from its
// install directory.
func dial(ctx context.Context, address string) (net.Conn, error) {
	conn, err := winio.DialPipeContext(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("无法连接内核控制器：%w", err)
	}
	if err := verifyControllerServer(conn, address); err != nil {
		_ = conn.Close()
		return nil, &net.OpError{Op: "dial", Net: "pipe", Err: err}
	}
	return conn, nil
}

// verifyControllerServer accepts the pipe's server process when this app
// started it (execRunner), or when it runs as LocalSystem from the
// helper's core path (service.CoreBin).
func verifyControllerServer(conn net.Conn, address string) error {
	fc, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("无法获取内核控制器管道句柄")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(fc.Fd()), &pid); err != nil {
		return fmt.Errorf("无法获取内核控制器进程：%w", err)
	}
	if ownServer(int(pid)) {
		return nil
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Errorf("无法检查内核控制器进程 %d：%w", pid, err)
	}
	defer windows.CloseHandle(proc)
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		return fmt.Errorf("无法检查内核控制器进程 %d：%w", pid, err)
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return err
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(proc, 0, &buf[0], &n); err != nil {
		return err
	}
	image := windows.UTF16ToString(buf[:n])
	if tu.User.Sid.IsWellKnown(windows.WinLocalSystemSid) && strings.EqualFold(filepath.Clean(image), filepath.Clean(service.CoreBin)) {
		return nil
	}
	return fmt.Errorf("管道 %s 的服务进程 %d（%s）不是澜流启动的 Mihomo 内核", address, pid, image)
}

// isExecutable: Windows has no execute bit; the extension decides.
func isExecutable(os.FileMode) bool { return true }

// binaryName is the bundled core file name on this platform.
const binaryName = "mihomo.exe"
