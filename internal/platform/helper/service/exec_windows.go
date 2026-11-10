//go:build windows

package service

import (
	"context"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"

	"lanflow/internal/platform/helper/protocol"
)

type execProcess struct{ cmd *exec.Cmd }

func (p *execProcess) Pid() int    { return p.cmd.Process.Pid }
func (p *execProcess) Wait() error { return p.cmd.Wait() }

// Interrupt terminates the process: a service has no console, so no
// CTRL_BREAK can be delivered; Mihomo's Wintun adapter is released by the
// driver when the process handle closes.
func (p *execProcess) Interrupt() error { return p.cmd.Process.Kill() }
func (p *execProcess) Kill() error      { return p.cmd.Process.Kill() }

func execStart(_ context.Context, c Command) (Process, error) {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdout = c.Output
	cmd.Stderr = c.Output
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd}, nil
}

func controllerArgs(controller string) []string { return []string{"-ext-ctl-pipe", controller} }

// baseEnv restricts the controller pipe Mihomo creates to SYSTEM,
// Administrators and the installing user (Mihomo reads LISTEN_NAMEDPIPE_SDDL;
// its default grants all local users).
func baseEnv(sid string, spec protocol.Spec) []string {
	env := []string{
		"SystemRoot=" + systemRoot(),
		"TEMP=" + spec.WorkDir,
		"TMP=" + spec.WorkDir,
		"LISTEN_NAMEDPIPE_SDDL=" + PipeSDDL(sid),
	}
	return env
}

func systemRoot() string {
	if dir, err := windows.GetSystemWindowsDirectory(); err == nil {
		return dir
	}
	return `C:\Windows`
}

// PipeSDDL is the DACL for both the helper pipe and the Mihomo controller
// pipe: full access for SYSTEM and Administrators, read/write for the
// installing user, nothing for anyone else.
func PipeSDDL(sid string) string {
	return "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;" + sid + ")"
}

func defaultHooks() Hooks { return Hooks{} }
