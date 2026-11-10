//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"lanflow/internal/platform/helper/protocol"
)

// Install locations on macOS.
const (
	Label      = "com.lanflow.helper"
	HelperDir  = "/Library/PrivilegedHelperTools"
	HelperBin  = HelperDir + "/com.lanflow.helper"
	CoreBin    = HelperDir + "/com.lanflow.mihomo"
	ConfigDir  = "/Library/Application Support/Lanflow Helper"
	ConfigPath = ConfigDir + "/config.json"
	PlistPath  = "/Library/LaunchDaemons/" + Label + ".plist"
	LogDir     = "/Library/Logs/Lanflow Helper"
	LogPath    = LogDir + "/helper.log"
)

// Install copies the helper (the running executable) and the core into
// HelperDir, records the config and (re)loads the LaunchDaemon. Must run
// as root.
func Install(ctx context.Context, o InstallOptions, logf Logf) error {
	if os.Geteuid() != 0 {
		return errors.New("安装权限组件需要管理员权限")
	}
	if err := o.check(); err != nil {
		return err
	}
	if _, err := parseUID(o.UID); err != nil {
		return err
	}
	dataDir, err := resolveDataDir(o.DataDir, o.UID)
	if err != nil {
		return err
	}
	if err := checkDowngrade(ConfigPath, o); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.EvalSymlinks(self)

	// Stop a previous instance first so the files are not in use.
	_ = launchctl(ctx, "bootout", "system/"+Label)

	if err := os.MkdirAll(HelperDir, 0o755); err != nil {
		return err
	}
	if self != HelperBin {
		logf("copy %s -> %s", self, HelperBin)
		if err := copyFile(self, HelperBin, 0o755); err != nil {
			return fmt.Errorf("复制权限组件失败: %w", err)
		}
	}
	src, _ := filepath.EvalSymlinks(o.CoreBin)
	if src == "" {
		src = o.CoreBin
	}
	logf("copy %s -> %s", src, CoreBin)
	if err := copyFile(src, CoreBin, 0o755); err != nil {
		return fmt.Errorf("复制 Mihomo 内核失败: %w", err)
	}
	for _, p := range []string{HelperDir, HelperBin, CoreBin} {
		if err := os.Chown(p, 0, 0); err != nil {
			return err
		}
	}
	sum, err := FileSHA256(CoreBin)
	if err != nil {
		return err
	}
	cfg := Config{Version: ConfigVersion, Core: CoreBin, CoreSource: src, SHA256: sum, DataDir: dataDir, UID: o.UID, HelperVersion: o.Version}
	if err := os.MkdirAll(ConfigDir, 0o755); err != nil {
		return err
	}
	if err := SaveConfig(ConfigPath, cfg, 0o644); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	_ = os.Chown(ConfigDir, 0, 0)
	_ = os.Chown(ConfigPath, 0, 0)
	if err := os.MkdirAll(LogDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(PlistPath, []byte(plist()), 0o644); err != nil {
		return fmt.Errorf("写入 LaunchDaemon 失败: %w", err)
	}
	_ = os.Chown(PlistPath, 0, 0)
	if err := launchctl(ctx, "bootstrap", "system", PlistPath); err != nil {
		return fmt.Errorf("加载 LaunchDaemon 失败: %w", err)
	}
	logf("installed; waiting for socket")
	return waitForSocket(ctx, 10*time.Second)
}

// Uninstall unloads the LaunchDaemon (which stops Mihomo via the helper's
// shutdown) and removes every installed file. Must run as root.
func Uninstall(ctx context.Context, logf Logf) error {
	if os.Geteuid() != 0 {
		return errors.New("移除权限组件需要管理员权限")
	}
	if err := launchctl(ctx, "bootout", "system/"+Label); err != nil {
		logf("bootout: %v", err)
	}
	var firstErr error
	for _, p := range []string{PlistPath, HelperBin, CoreBin, ConfigPath, protocol.SocketPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	_ = os.RemoveAll(ConfigDir)
	_ = os.RemoveAll(LogDir)
	return firstErr
}

func plist() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + HelperBin + `</string>
		<string>run</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + LogPath + `</string>
	<key>StandardErrorPath</key>
	<string>` + LogPath + `</string>
</dict>
</plist>
`
}

func launchctl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "/bin/launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func waitForSocket(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		if fi, err := os.Lstat(protocol.SocketPath); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("权限组件已安装但未在 10 秒内启动，请查看 " + LogPath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Listen creates the helper socket: root-owned directory, chmod 0600 then
// chown to the installing user so only that user (and root) can connect.
func Listen(uid string) (net.Listener, error) {
	n, err := parseUID(uid)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(protocol.SocketPath)
	old := syscall.Umask(0o177)
	l, err := net.Listen("unix", protocol.SocketPath)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(protocol.SocketPath, 0o600); err != nil {
		_ = l.Close()
		return nil, err
	}
	if err := os.Chown(protocol.SocketPath, n, -1); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// Authorizer returns the per-connection check: the peer's effective uid
// (LOCAL_PEERCRED) must be the installing user or root.
func Authorizer(uid string) func(net.Conn) error {
	return func(conn net.Conn) error {
		peer, err := PeerUID(conn)
		if err != nil {
			return err
		}
		if fmt.Sprint(peer) != uid && peer != 0 {
			return fmt.Errorf("peer uid %d is not the installing user %s", peer, uid)
		}
		return nil
	}
}

// PeerUID returns the effective uid of the process at the other end of a
// unix socket connection.
func PeerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Xucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if cerr != nil {
		return 0, cerr
	}
	return cred.Uid, nil
}

// Run is the LaunchDaemon entry: load the config, listen, serve until
// SIGTERM/SIGINT, then stop Mihomo and remove the socket.
func Run(ctx context.Context, version string, logf Logf) error {
	cfg, err := LoadConfig(ConfigPath)
	if err != nil {
		return err
	}
	sup := NewSupervisor(NewPolicy(cfg))
	sup.Logf = logf
	l, err := Listen(cfg.UID)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", protocol.SocketPath, err)
	}
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	srv := &protocol.Server{Handler: NewHandler(sup, version), Authorize: Authorizer(cfg.UID), Logf: logf}
	logf("lanflow-helper %s listening on %s for uid %s", version, protocol.SocketPath, cfg.UID)
	err = srv.Serve(ctx, l)
	sup.Shutdown()
	_ = os.Remove(protocol.SocketPath)
	return err
}
