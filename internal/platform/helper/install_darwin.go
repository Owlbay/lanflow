//go:build darwin

package helper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// execCommand runs a command and returns its combined output; tests
// replace it.
var execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// currentUID returns the uid passed to the helper; tests replace it.
var currentUID = os.Getuid

const (
	installPrompt   = "澜流需要安装权限组件以启用 TUN 模式"
	uninstallPrompt = "澜流需要管理员权限以移除权限组件"
)

// errCanceled is returned when the user dismisses the authorization dialog.
var errCanceled = errors.New("已取消授权")

func install(ctx context.Context, o InstallOptions) error {
	if err := checkOptions(o); err != nil {
		return err
	}
	if err := checkDowngrade(ctx, o.HelperBin); err != nil {
		return err
	}
	args := []string{o.HelperBin, "install", "--core", o.CoreBin, "--data-dir", o.DataDir, "--uid", strconv.Itoa(currentUID())}
	if err := runPrivileged(ctx, args, installPrompt); err != nil {
		return err
	}
	return waitInstalled(ctx, 10*time.Second)
}

func uninstall(ctx context.Context) error {
	bin, err := helperBinary()
	if err != nil {
		return err
	}
	if err := runPrivileged(ctx, []string{bin, "uninstall"}, uninstallPrompt); err != nil {
		return err
	}
	return waitUninstalled(ctx, 5*time.Second)
}

func checkOptions(o InstallOptions) error {
	for _, p := range []struct{ name, path string }{{"权限组件", o.HelperBin}, {"Mihomo 内核", o.CoreBin}, {"数据目录", o.DataDir}} {
		if p.path == "" || !filepath.IsAbs(p.path) {
			return fmt.Errorf("%s路径必须是绝对路径: %q", p.name, p.path)
		}
	}
	if fi, err := os.Stat(o.HelperBin); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("找不到权限组件程序: %s", o.HelperBin)
	}
	if fi, err := os.Stat(o.CoreBin); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("找不到 Mihomo 内核: %s", o.CoreBin)
	}
	if fi, err := os.Stat(o.DataDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("数据目录不存在: %s", o.DataDir)
	}
	return nil
}

// helperBinary locates a helper executable for uninstall: the installed
// copy if present, else the one shipped next to the app executable.
func helperBinary() (string, error) {
	const installedBin = "/Library/PrivilegedHelperTools/com.lanflow.helper"
	if fi, err := os.Stat(installedBin); err == nil && fi.Mode().IsRegular() {
		return installedBin, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	for _, c := range []string{
		filepath.Join(filepath.Dir(exe), "lanflow-helper"),
		filepath.Join(filepath.Dir(exe), "..", "Resources", "lanflow-helper"),
		filepath.Join(filepath.Dir(exe), "..", "Helpers", "lanflow-helper"),
	} {
		if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() {
			return filepath.Clean(c), nil
		}
	}
	return "", errors.New("找不到权限组件程序，无法移除")
}

// runPrivileged runs argv as root through osascript's
// "do shell script … with administrator privileges".
func runPrivileged(ctx context.Context, argv []string, prompt string) error {
	out, err := execCommand(ctx, "/usr/bin/osascript", "-e", appleScript(shellCommand(argv), prompt))
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if isUserCanceled(msg) {
			return errCanceled
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("权限组件操作失败: %s", lastLine(msg))
	}
	return nil
}

// shellCommand joins argv into a /bin/sh command line, single-quoting
// every argument so no character is interpreted by the shell.
func shellCommand(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// appleScript wraps a shell command in a `do shell script` statement.
// Inside an AppleScript string literal only backslash and double quote
// are special.
func appleScript(shell, prompt string) string {
	return `do shell script "` + appleQuote(shell) + `" with administrator privileges with prompt "` + appleQuote(prompt) + `"`
}

func appleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// isUserCanceled recognizes osascript's cancellation error (-128).
func isUserCanceled(out string) bool {
	return strings.Contains(out, "(-128)") || strings.Contains(out, "User canceled") || strings.Contains(out, "用户已取消")
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
