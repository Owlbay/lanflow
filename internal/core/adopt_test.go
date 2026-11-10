//go:build !windows

package core

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// orphanMihomo starts a process that looks like a core a crashed Lanflow
// left (this test binary as the fake core, copied as mihomo) and writes
// its PID file in a work dir.
func orphanMihomo(t *testing.T) (*exec.Cmd, Spec) {
	t.Helper()
	src, err := os.Executable()
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
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), fakeCoreEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	go cmd.Wait()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	waitFor(t, func() bool { return filepath.Base(processName(cmd.Process.Pid)) == "mihomo" }) // exec done
	dir := t.TempDir()
	writePID(dir, cmd.Process.Pid)
	spec := Spec{Bin: bin, WorkDir: dir, Config: filepath.Join(dir, "c.yaml"), Controller: filepath.Join(dir, "c.sock"), LogFile: filepath.Join(dir, "log")}
	return cmd, spec
}

func TestAdoptOrphan(t *testing.T) {
	old := adoptPoll
	adoptPoll = 20 * time.Millisecond
	t.Cleanup(func() { adoptPoll = old })
	cmd, spec := orphanMihomo(t)
	if !OrphanRunning(spec.WorkDir) {
		t.Fatal("orphan not seen")
	}
	ready := func(context.Context) error { return nil }
	m := NewManager(Options{Ready: ready})
	if err := m.Adopt(context.Background(), spec, newExecRunner()); err != nil {
		t.Fatal(err)
	}
	if !m.Running() {
		t.Fatal("adopted core not running")
	}
	// It ends on its own: a crash is reported.
	_ = cmd.Process.Kill()
	select {
	case <-m.Crashes():
	case <-time.After(5 * time.Second):
		t.Fatal("exit of the adopted core not reported")
	}
	if m.Running() {
		t.Fatal("still running after the exit")
	}

	// Stopping an adopted core ends it without a crash report.
	cmd2, spec2 := orphanMihomo(t)
	m2 := NewManager(Options{Ready: ready})
	if err := m2.Adopt(context.Background(), spec2, newExecRunner()); err != nil {
		t.Fatal(err)
	}
	if err := m2.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if processName(cmd2.Process.Pid) != "" {
		t.Fatal("adopted core still runs after Stop")
	}
	select {
	case err := <-m2.Crashes():
		t.Fatalf("crash after Stop: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

// A process that does not answer on our controller is not taken over and
// is left running for the caller to stop.
func TestAdoptRefusesSilentCore(t *testing.T) {
	cmd, spec := orphanMihomo(t)
	m := NewManager(Options{Ready: func(context.Context) error { return errors.New("no controller") }})
	if err := m.Adopt(context.Background(), spec, newExecRunner()); err == nil {
		t.Fatal("adopted a core without a controller")
	}
	if m.Running() || processName(cmd.Process.Pid) == "" {
		t.Fatal("refused core was stopped or counted as running")
	}
	StopOrphan(spec.WorkDir)
	if processName(cmd.Process.Pid) != "" {
		t.Fatal("StopOrphan left it running")
	}
	if err := m.Adopt(context.Background(), spec, newExecRunner()); !errors.Is(err, ErrNothingToAdopt) {
		t.Fatalf("nothing left: %v", err)
	}
}
