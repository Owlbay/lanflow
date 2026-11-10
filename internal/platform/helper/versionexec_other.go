//go:build !windows

package helper

import (
	"context"
	"os/exec"
)

// versionCommand runs `bin version`.
func versionCommand(ctx context.Context, bin string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, "version")
}
