//go:build windows

package helper

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"lanflow/internal/platform/helper/protocol"
)

// platformDial connects to the helper pipe and verifies who serves it.
// The service creates the pipe as its first instance (go-winio opens it
// with FILE_CREATE), so a process that grabbed the name earlier makes the
// service fail rather than share the name; checking the server process
// here makes sure the app never talks to such a squatter instead.
func platformDial(ctx context.Context) (net.Conn, error) {
	conn, err := winio.DialPipeContext(ctx, protocol.PipeName)
	if err != nil {
		return nil, err
	}
	if err := verifyPipeServer(conn); err != nil {
		_ = conn.Close()
		return nil, &net.OpError{Op: "dial", Net: "pipe", Err: err}
	}
	return conn, nil
}

// verifyPipeServer requires the pipe's server process to run as
// LocalSystem from the installed helper executable.
func verifyPipeServer(conn net.Conn) error {
	fc, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("无法获取管道句柄")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(fc.Fd()), &pid); err != nil {
		return fmt.Errorf("无法获取权限组件进程: %w", err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Errorf("无法检查权限组件进程 %d: %w", pid, err)
	}
	defer windows.CloseHandle(proc)
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		return fmt.Errorf("无法检查权限组件进程 %d: %w", pid, err)
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return err
	}
	if !tu.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return fmt.Errorf("管道 %s 的服务进程 %d 不是以 SYSTEM 运行的权限组件", protocol.PipeName, pid)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(proc, 0, &buf[0], &n); err != nil {
		return err
	}
	if image := windows.UTF16ToString(buf[:n]); !strings.EqualFold(filepath.Clean(image), installedHelperPath()) {
		return fmt.Errorf("管道 %s 的服务进程不是已安装的权限组件: %s", protocol.PipeName, image)
	}
	return nil
}

// installedHelperPath is where the service executable is installed
// (service.HelperBin).
func installedHelperPath() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "Lanflow Helper", "lanflow-helper.exe")
}
