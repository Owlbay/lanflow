package sysdns

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// commandTimeout bounds every external command (networksetup).
const commandTimeout = 5 * time.Second

// runCommand is the executor used by the darwin implementation.
var runCommand executor = execCommand

// execCommand runs name with args under a commandTimeout deadline.
func execCommand(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out.String(), fmt.Errorf("命令 %s 超过 %d 秒未返回", name, int(commandTimeout.Seconds()))
	}
	if err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(msg)
	}
	return out.String(), nil
}
