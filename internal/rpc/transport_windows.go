//go:build windows

package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Endpoint is the named pipe of the current user,
// \\.\pipe\lanflow-<user SID>, or $LANFLOW_SOCKET when set. dataDir is
// unused on Windows.
func Endpoint(string) string {
	if v := os.Getenv("LANFLOW_SOCKET"); v != "" {
		return v
	}
	sid, err := currentSID()
	if err != nil {
		return `\\.\pipe\lanflow-unknown`
	}
	return `\\.\pipe\lanflow-` + sid
}

// Listen creates the pipe with a DACL for the current user only. go-winio
// opens the first instance with FILE_FLAG_FIRST_PIPE_INSTANCE, so a name
// another process holds makes Listen fail instead of sharing it. Accepted
// connections whose client process runs as another user are closed.
func Listen(dataDir string) (net.Listener, error) {
	sid, err := currentSID()
	if err != nil {
		return nil, err
	}
	l, err := winio.ListenPipe(Endpoint(dataDir), &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")",
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, ErrInUse
		}
		return nil, err
	}
	return &peerListener{Listener: l}, nil
}

func dialEndpoint(ctx context.Context, dataDir string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, Endpoint(dataDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, ErrNotRunning
		}
		return nil, err
	}
	return c, nil
}

func currentSID() (string, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}

// verifyPeer requires the process at the other end of the pipe (the
// client for the server, the server for a client) to run as the current
// user.
func verifyPeer(conn net.Conn) error {
	fc, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("无法获取管道句柄")
	}
	h := windows.Handle(fc.Fd())
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(h, &pid); err != nil {
		return fmt.Errorf("无法获取对端进程: %w", err)
	}
	if pid == uint32(os.Getpid()) {
		// We are the client: check the server instead.
		if err := windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
			return fmt.Errorf("无法获取对端进程: %w", err)
		}
	}
	want, err := currentSID()
	if err != nil {
		return err
	}
	got, err := processSID(pid)
	if err != nil {
		return fmt.Errorf("无法检查对端进程 %d: %w", pid, err)
	}
	if got != want {
		return fmt.Errorf("对端进程 %d 属于 %s，不是当前用户", pid, got)
	}
	return nil
}

func processSID(pid uint32) (string, error) {
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(proc)
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		return "", err
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}
