//go:build darwin

package app

import (
	"context"
	"os/exec"
	"strings"
)

const helperLogDir = "/Library/Logs/Lanflow Helper"

func osVersion() string {
	out, _ := exec.Command("/usr/bin/sw_vers", "-productVersion").Output()
	return strings.TrimSpace(string(out))
}

// systemDiagnostics runs read-only commands describing the network.
func systemDiagnostics(ctx context.Context) map[string]string {
	run := func(name string, args ...string) string {
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			return string(out) + "\n(" + err.Error() + ")"
		}
		return string(out)
	}
	return map[string]string{
		"routes.txt":   run("/usr/sbin/netstat", "-rn", "-f", "inet"),
		"dns.txt":      run("/usr/sbin/scutil", "--dns"),
		"proxy.txt":    run("/usr/sbin/scutil", "--proxy"),
		"services.txt": run("/usr/sbin/networksetup", "-listnetworkserviceorder"),
	}
}
