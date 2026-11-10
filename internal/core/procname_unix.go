//go:build !windows

package core

import (
	"os/exec"
	"strconv"
	"strings"
)

// processName returns the executable name of pid, "" if it is not running.
func processName(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
