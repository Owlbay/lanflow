package core

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lanflow/internal/fsx"
)

// pidFileName records the PID of the Mihomo this runner started, so a core
// orphaned by a crashed Lanflow can be stopped before the next start
// (otherwise it keeps the mixed port and the next start fails).
const pidFileName = "mihomo.pid"

func writePID(workDir string, pid int) {
	_ = fsx.WriteFile(filepath.Join(workDir, pidFileName), []byte(strconv.Itoa(pid)), 0o600)
}

func removePID(workDir string) { _ = os.Remove(filepath.Join(workDir, pidFileName)) }

// killOrphan stops a Mihomo left running by a previous Lanflow, identified
// by the PID file in workDir. It only kills the process if it is still a
// mihomo executable (PIDs get reused).
func killOrphan(workDir string) {
	path := filepath.Join(workDir, pidFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	defer os.Remove(path)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return
	}
	name := processName(pid)
	if name == "" || !strings.Contains(strings.ToLower(filepath.Base(name)), "mihomo") {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = interrupt(p)
	for i := 0; i < 25; i++ { // up to 5 s
		time.Sleep(200 * time.Millisecond)
		if processName(pid) == "" {
			return
		}
	}
	_ = p.Kill()
	time.Sleep(200 * time.Millisecond)
}
