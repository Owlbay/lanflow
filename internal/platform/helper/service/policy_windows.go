//go:build windows

package service

import (
	"errors"
	"fmt"
	"strings"
)

func samePath(a, b string) bool { return strings.EqualFold(a, b) }
func hasPathPrefix(p, prefix string) bool {
	return len(p) >= len(prefix) && strings.EqualFold(p[:len(prefix)], prefix)
}

// ownerMatches is not used on Windows: the controller is a named pipe.
func ownerMatches(string, string) (bool, error) { return true, nil }

// checkController validates the controller pipe name. Mihomo creates the
// pipe itself; the helper restricts it with LISTEN_NAMEDPIPE_SDDL.
func checkController(_ *Policy, controller string) (string, error) {
	if controller == "" {
		return "", errors.New("控制器地址未指定")
	}
	const prefix = `\\.\pipe\lanflow-`
	if !strings.HasPrefix(controller, prefix) || len(controller) == len(prefix) {
		return "", fmt.Errorf(`控制器管道名必须以 %s 开头: %s`, prefix, controller)
	}
	if strings.ContainsAny(controller[len(prefix):], `\/`) {
		return "", fmt.Errorf("控制器管道名无效: %s", controller)
	}
	return controller, nil
}
