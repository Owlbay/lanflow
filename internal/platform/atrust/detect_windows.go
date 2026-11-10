//go:build windows

package atrust

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func detect(ctx context.Context, _ func(string) bool) Info {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tasklist", "/fo", "csv", "/nh")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return Info{}
	}
	return Info{Processes: matchProcesses(tasklistNames(string(out), os.Getpid()))}
}
