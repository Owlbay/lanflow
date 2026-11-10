//go:build windows

package helper

import (
	"context"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// versionCommand runs `bin version` without flashing a console window.
func versionCommand(ctx context.Context, bin string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}
