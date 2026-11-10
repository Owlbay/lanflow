//go:build windows

package helper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// errCanceled is returned when the user dismisses the UAC prompt.
var errCanceled = errors.New("已取消授权")

func install(ctx context.Context, o InstallOptions) error {
	for _, p := range []struct{ name, path string }{{"权限组件", o.HelperBin}, {"Mihomo 内核", o.CoreBin}, {"数据目录", o.DataDir}} {
		if p.path == "" || !filepath.IsAbs(p.path) {
			return fmt.Errorf("%s路径必须是绝对路径: %q", p.name, p.path)
		}
	}
	if fi, err := os.Stat(o.HelperBin); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("找不到权限组件程序: %s", o.HelperBin)
	}
	if err := checkDowngrade(ctx, o.HelperBin); err != nil {
		return err
	}
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	if err := runElevated(ctx, o.HelperBin, []string{"install", "--core", o.CoreBin, "--data-dir", o.DataDir, "--uid", sid}); err != nil {
		return err
	}
	return waitInstalled(ctx, 10*time.Second)
}

func uninstall(ctx context.Context) error {
	bin, err := helperBinary()
	if err != nil {
		return err
	}
	if err := runElevated(ctx, bin, []string{"uninstall"}); err != nil {
		return err
	}
	return waitUninstalled(ctx, 5*time.Second)
}

// helperBinary prefers the installed copy, else the one next to the app.
func helperBinary() (string, error) {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	installedBin := filepath.Join(pf, "Lanflow Helper", "lanflow-helper.exe")
	if fi, err := os.Stat(installedBin); err == nil && fi.Mode().IsRegular() {
		return installedBin, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	c := filepath.Join(filepath.Dir(exe), "lanflow-helper.exe")
	if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() {
		return c, nil
	}
	return "", errors.New("找不到权限组件程序，无法移除")
}

// currentUserSID returns the SID of the user running the app, which the
// elevated installer records (the UAC prompt may run as another account).
func currentUserSID() (string, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("无法获取当前用户标识: %w", err)
	}
	return tu.User.Sid.String(), nil
}

// shellExecuteInfo mirrors SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           windows.Handle
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       windows.Handle
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      windows.Handle
	dwHotKey       uint32
	hIconOrMonitor windows.Handle
	hProcess       windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
)

var (
	modshell32          = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = modshell32.NewProc("ShellExecuteExW")
)

// runElevated runs exe with args through the "runas" verb (UAC prompt)
// and waits for it to exit; a non-zero exit code is an error.
func runElevated(ctx context.Context, exe string, args []string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return err
	}
	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        windows.SW_HIDE,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errors.Is(e, windows.ERROR_CANCELLED) {
			return errCanceled
		}
		return fmt.Errorf("无法以管理员身份运行权限组件: %v", e)
	}
	if info.hProcess == 0 {
		return errors.New("无法获取权限组件进程")
	}
	defer windows.CloseHandle(info.hProcess)
	for {
		ev, err := windows.WaitForSingleObject(info.hProcess, 200)
		if err != nil {
			return err
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("权限组件操作失败（退出码 %d），详情见 %s", code, filepath.Join(programData(), "Lanflow Helper", "install.log"))
	}
	return nil
}

func programData() string {
	if v := os.Getenv("ProgramData"); v != "" {
		return v
	}
	return `C:\ProgramData`
}
