//go:build !windows

package core

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"syscall"
)

// controllerArgs tells Mihomo to serve its controller on a unix socket.
func controllerArgs(spec Spec) []string {
	return []string{"-ext-ctl-unix", spec.Controller}
}

// prepareController removes a stale socket file left by a previous run so
// the listener can bind. A real listener is unlinked too; Mihomo would do
// that itself, and we only ever manage the process we start.
func prepareController(spec Spec) error {
	if err := os.MkdirAll(filepath.Dir(spec.Controller), 0o755); err != nil {
		return fmt.Errorf("无法创建控制器目录：%w", err)
	}
	if err := os.Remove(spec.Controller); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("无法清理旧的控制器套接字 %s：%w", spec.Controller, err)
	}
	return nil
}

// cleanupController removes the socket file after the process has exited.
func cleanupController(spec Spec) {
	_ = os.Remove(spec.Controller)
}

func sysProcAttr() *syscall.SysProcAttr { return nil }

// interrupt asks the process to shut down gracefully (Mihomo handles SIGINT).
func interrupt(p *os.Process) error { return p.Signal(syscall.SIGINT) }

// controllerAddress returns the per-user socket path, short enough for
// sun_path. os.TempDir on macOS is a long per-user directory, so we fall
// back to ~/.lanflow when the result would not fit (or TMPDIR is a shared
// directory such as /tmp), and to /tmp only as a last resort.
func controllerAddress() string {
	name := "lanflow-" + currentUID() + ".sock"
	tmp := filepath.Clean(os.TempDir())
	// A shared, world-writable temp dir (TMPDIR unset → /tmp) is not used:
	// another user could plant the socket, and the TUN helper refuses it.
	if tmp != "/tmp" && tmp != "/private/tmp" && tmp != "/var/tmp" {
		if p := filepath.Join(tmp, name); len(p) < unixSocketPathLimit {
			return p
		}
	}
	// The user's own short directory: the TUN helper refuses /tmp
	// (world-writable), so prefer ~/.lanflow when TMPDIR is too long.
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".lanflow")
		if p := filepath.Join(dir, name); len(p) < unixSocketPathLimit && os.MkdirAll(dir, 0o700) == nil {
			return p
		}
	}
	return filepath.Join("/tmp", name)
}

func currentUID() string {
	if uid := os.Getuid(); uid >= 0 {
		return fmt.Sprint(uid)
	}
	if u, err := user.Current(); err == nil && u.Uid != "" {
		return u.Uid
	}
	return "default"
}

// dial connects to the controller unix socket.
func dial(ctx context.Context, address string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", address)
	if err != nil {
		return nil, fmt.Errorf("无法连接内核控制器：%w", err)
	}
	return conn, nil
}

// isExecutable reports whether the file mode has any execute bit set.
func isExecutable(info os.FileMode) bool { return info&0o111 != 0 }

// binaryName is the bundled core file name on this platform.
const binaryName = "mihomo"
