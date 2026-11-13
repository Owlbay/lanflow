//go:build windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func procAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// procTerminate ends pid: Windows has no polite signal for a process
// without windows.
func procTerminate(pid int) error { return procKill(pid) }

func procKill(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// procImage is the executable path of pid ("" = unknown).
func procImage(pid int) (string, time.Time) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", time.Time{}
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", time.Time{}
	}
	var created, exit, kernel, user windows.Filetime
	var start time.Time
	if windows.GetProcessTimes(h, &created, &exit, &kernel, &user) == nil {
		start = time.Unix(0, created.Nanoseconds())
	}
	return windows.UTF16ToString(buf[:n]), start
}

// isCaptureAgent: pid runs this program's executable (the agent is
// `Lanflow.exe capture-agent`) and started no later than recorded. The
// command line of another process is out of reach without reading its
// memory; the start time rules out a PID reused since.
func isCaptureAgent(pid int, recorded time.Time) bool {
	img, start := procImage(pid)
	if img == "" || start.IsZero() {
		return false
	}
	exe, err := os.Executable()
	if err != nil || !strings.EqualFold(filepath.Base(img), filepath.Base(exe)) {
		return false
	}
	return !start.After(recorded.Add(2 * time.Second))
}

// portOwner names the program listening on TCP port ("" = unknown), from
// netstat.
func portOwner(port int) string {
	cmd := exec.Command("netstat", "-ano", "-p", "TCP")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	pid := parseNetstat(string(out), port)
	if pid <= 0 {
		return ""
	}
	if img, _ := procImage(pid); img != "" {
		return filepath.Base(img) + " (PID " + strconv.Itoa(pid) + ")"
	}
	return "PID " + strconv.Itoa(pid)
}

func hideWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
