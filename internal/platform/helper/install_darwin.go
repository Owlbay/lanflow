//go:build darwin

package helper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	sum, err := helperDigest(o.HelperBin)
	if err != nil {
		return err
	}
	args := []string{"install", "--core", o.CoreBin, "--data-dir", o.DataDir, "--uid", strconv.Itoa(currentUID())}
	if err := runPrivilegedShell(ctx, verifiedExec(o.HelperBin, sum, args), installPrompt); err != nil {
		return err
	}
	return waitInstalled(ctx, 10*time.Second)
}

func uninstall(ctx context.Context) error {
	bin, err := helperBinary()
	if err != nil {
		return err
	}
	shell := shellCommand([]string{bin, "uninstall"})
	if bin != installedHelperBin {
		// Not the root-owned installed copy: verify it like install does.
		sum, err := helperDigest(bin)
		if err != nil {
			return err
		}
		shell = verifiedExec(bin, sum, []string{"uninstall"})
	}
	if err := runPrivilegedShell(ctx, shell, uninstallPrompt); err != nil {
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
	if fi, err := os.Stat(installedHelperBin); err == nil && fi.Mode().IsRegular() {
		return installedHelperBin, nil
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

// installedHelperBin is the root-owned copy the LaunchDaemon runs.
const installedHelperBin = "/Library/PrivilegedHelperTools/com.lanflow.helper"

// ExpectedHelperSHA256 is the hex SHA-256 of the lanflow-helper binary
// shipped with the app, set at build time:
//
//	-ldflags "-X lanflow/internal/platform/helper.ExpectedHelperSHA256=<hex>"
//
// When set, a helper binary with another digest is never run as root.
// When empty (development builds) the digest is taken from the binary just
// before the authorization prompt, which still guarantees that root runs
// exactly the bytes the app saw.
var ExpectedHelperSHA256 string

// helperDigest returns the digest the privileged command must verify.
func helperDigest(bin string) (string, error) {
	sum, err := fileSHA256(bin)
	if err != nil {
		return "", fmt.Errorf("无法读取权限组件程序: %w", err)
	}
	if want := strings.ToLower(strings.TrimSpace(ExpectedHelperSHA256)); want != "" {
		if sum != want {
			return "", errors.New("权限组件程序校验失败，与应用构建时记录的不一致，请重新下载安装澜流")
		}
		return want, nil
	}
	return sum, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyFailed is printed by verifiedExec when the copied helper does not
// match; it becomes the error message.
const verifyFailed = "权限组件程序校验失败，已拒绝以管理员身份运行"

// verifiedExec returns a /bin/sh script, run as root, that copies src into
// a fresh root-owned directory (mktemp -d, mode 0700, in the sticky
// /private/var/tmp), checks the copy's SHA-256 against sum and only then
// runs the copy with args. The user (or anything running as the user) can
// rewrite src inside the app bundle at any time, but not the copy, so root
// executes exactly the verified bytes.
func verifiedExec(src, sum string, args []string) string {
	run := append([]string{}, args...)
	for i, a := range run {
		run[i] = shellQuote(a)
	}
	return strings.Join([]string{
		"set -e",
		"umask 077",
		`d=$(/usr/bin/mktemp -d /private/var/tmp/lanflow-helper.XXXXXX)`,
		`trap '/bin/rm -rf "$d"' EXIT`,
		`/bin/cp ` + shellQuote(src) + ` "$d/lanflow-helper"`,
		`/bin/chmod 0700 "$d/lanflow-helper"`,
		`s=$(/usr/bin/shasum -a 256 "$d/lanflow-helper" | /usr/bin/awk '{print $1}')`,
		`if [ "$s" != ` + shellQuote(sum) + ` ]; then echo ` + shellQuote(verifyFailed) + ` >&2; exit 1; fi`,
		`"$d/lanflow-helper" ` + strings.Join(run, " "),
	}, "; ")
}

// runPrivilegedShell runs a /bin/sh command line as root through
// osascript's "do shell script … with administrator privileges".
func runPrivilegedShell(ctx context.Context, shell string, prompt string) error {
	out, err := execCommand(ctx, "/usr/bin/osascript", "-e", appleScript(shell, prompt))
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
