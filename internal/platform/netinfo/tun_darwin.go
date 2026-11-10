//go:build darwin

package netinfo

import (
	"context"
	"os/exec"
)

func otherTUN(ctx context.Context) (string, bool) {
	out, err := exec.CommandContext(ctx, "netstat", "-rn", "-f", "inet").Output()
	if err != nil {
		return "", false
	}
	return parseDarwinTUNRoute(string(out))
}
