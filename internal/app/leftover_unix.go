//go:build !windows

package app

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func procAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func procTerminate(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
func procKill(pid int) error      { return syscall.Kill(pid, syscall.SIGKILL) }

// isCaptureAgent: pid runs `… capture-agent` and has run since before
// recorded (with a little slack for the file written just after start).
func isCaptureAgent(pid int, recorded time.Time) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "etime=", "-o", "args=").Output()
	if err != nil {
		return false
	}
	line := strings.TrimSpace(string(out))
	etime, args, ok := strings.Cut(line, " ")
	if !ok {
		return false
	}
	fields := strings.Fields(args)
	if len(fields) < 2 || fields[len(fields)-1] != "capture-agent" {
		return false
	}
	el, ok := parseEtime(etime)
	if !ok {
		return false
	}
	started := time.Now().Add(-el)
	return !started.After(recorded.Add(2 * time.Second))
}

// parseEtime reads ps's elapsed time, [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, bool) {
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var total int
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		total = total*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(total)*time.Second, true
}

// portOwner names the program listening on TCP port ("" = unknown).
func portOwner(port int) string {
	out, err := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN", "-Fpc").Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	return parseLsof(out)
}

// parseLsof reads the first process of `lsof -Fpc`.
func parseLsof(out []byte) string {
	var pid, name string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "p") && pid == "":
			pid = l[1:]
		case strings.HasPrefix(l, "c") && name == "":
			name = l[1:]
		}
	}
	if pid == "" {
		return ""
	}
	if name == "" {
		return "PID " + pid
	}
	return name + " (PID " + pid + ")"
}
