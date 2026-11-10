//go:build windows

package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

var helperLogDir = filepath.Join(programDataDir(), "Lanflow Helper")

func programDataDir() string {
	if p := os.Getenv("ProgramData"); p != "" {
		return p
	}
	return `C:\ProgramData`
}

func osVersion() string {
	cmd := exec.Command("cmd", "/c", "ver")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// systemDiagnostics runs read-only commands describing the network.
func systemDiagnostics(ctx context.Context) map[string]string {
	run := func(name string, args ...string) string {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out) + "\n(" + err.Error() + ")"
		}
		return string(out)
	}
	return map[string]string{
		"routes.txt":     run("route", "print", "-4"),
		"interfaces.txt": run("ipconfig", "/all"),
		"proxy.txt":      run("reg", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`),
		"winhttp.txt":    run("netsh", "winhttp", "show", "proxy"),
	}
}
