//go:build !windows

package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// killOrphan must never kill a process that is not mihomo (PIDs get reused).
func TestKillOrphanSparesOtherProcesses(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer cmd.Process.Kill()
	dir := t.TempDir()
	writePID(dir, cmd.Process.Pid)
	killOrphan(dir)
	if processName(cmd.Process.Pid) == "" {
		t.Fatal("killOrphan killed a non-mihomo process")
	}
	if _, err := os.Stat(filepath.Join(dir, pidFileName)); !os.IsNotExist(err) {
		t.Fatal("pid file not removed")
	}
}

func TestKillOrphanStopsMihomo(t *testing.T) {
	// Copy `sleep` under the name mihomo so it looks like an orphaned core.
	src, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Skip(err)
	}
	bin := filepath.Join(t.TempDir(), "mihomo")
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "30")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	go cmd.Wait()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, pidFileName), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	killOrphan(dir)
	if processName(cmd.Process.Pid) != "" {
		cmd.Process.Kill()
		t.Fatal("orphaned mihomo still running")
	}
}
