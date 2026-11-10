//go:build !windows

package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

// Endpoint is the socket path for the data directory: <dataDir>/run/
// lanflow.sock, or $LANFLOW_SOCKET when set (tests, development).
func Endpoint(dataDir string) string {
	if v := os.Getenv("LANFLOW_SOCKET"); v != "" {
		return v
	}
	return filepath.Join(dataDir, "run", "lanflow.sock")
}

// Listen creates the socket: its directory 0700 and owned by the current
// user, the socket 0600. A socket another process still serves is left
// alone (ErrInUse); a stale one is replaced. Accepted connections from
// another user are closed at once (LOCAL_PEERCRED / SO_PEERCRED).
func Listen(dataDir string) (net.Listener, error) {
	path := Endpoint(dataDir)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if os.Getenv("LANFLOW_SOCKET") == "" {
		if err := checkRunDir(dir); err != nil {
			return nil, err
		}
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			_ = c.Close()
			return nil, ErrInUse
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return &peerListener{Listener: l}, nil
}

// checkRunDir requires the socket's directory to be a real directory of
// the current user, and makes it private. ($LANFLOW_SOCKET, for tests and
// development, may point anywhere, e.g. /tmp.)
func checkRunDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("本机接口目录不是目录: %s", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("本机接口目录不属于当前用户: %s", dir)
	}
	return os.Chmod(dir, 0o700)
}

func dialEndpoint(ctx context.Context, dataDir string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", Endpoint(dataDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return nil, ErrNotRunning
		}
		return nil, err
	}
	return c, nil
}

// peerUID returns the effective uid of the other end of a unix socket.
func peerUID(conn net.Conn) (int, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("不是 unix 套接字连接")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid int
	var uerr error
	if err := raw.Control(func(fd uintptr) { uid, uerr = sockPeerUID(int(fd)) }); err != nil {
		return 0, err
	}
	return uid, uerr
}

// uidOverride replaces the uid peers must run as, for tests; -1 = none.
var uidOverride = func() *atomic.Int64 { var v atomic.Int64; v.Store(-1); return &v }()

// currentUID is the uid peers must run as.
func currentUID() int {
	if v := uidOverride.Load(); v >= 0 {
		return int(v)
	}
	return os.Getuid()
}

// verifyPeer requires the other end to run as the current user.
func verifyPeer(conn net.Conn) error {
	uid, err := peerUID(conn)
	if err != nil {
		return fmt.Errorf("无法确认对端身份: %w", err)
	}
	if uid != currentUID() {
		return fmt.Errorf("对端进程属于用户 %d，不是当前用户 %d", uid, currentUID())
	}
	return nil
}
