//go:build windows

package netinfo

import (
	"context"
	"os/exec"
	"syscall"
)

func otherTUN(ctx context.Context) (string, bool) {
	cmd := exec.CommandContext(ctx, "route", "print", "-4")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	if addr, ok := parseWindowsTUNRoute(string(out)); ok {
		return "TUN（" + addr + "）", true
	}
	return "", false
}
