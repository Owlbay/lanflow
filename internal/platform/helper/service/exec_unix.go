//go:build !windows

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"lanflow/internal/platform/helper/protocol"
)

type execProcess struct{ cmd *exec.Cmd }

func (p *execProcess) Pid() int         { return p.cmd.Process.Pid }
func (p *execProcess) Wait() error      { return p.cmd.Wait() }
func (p *execProcess) Interrupt() error { return p.cmd.Process.Signal(syscall.SIGINT) }
func (p *execProcess) Kill() error      { return p.cmd.Process.Kill() }

// execStart spawns the command in its own process group so a signal to
// the helper does not reach Mihomo before the helper has decided to stop it.
func execStart(_ context.Context, c Command) (Process, error) {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdout = c.Output
	cmd.Stderr = c.Output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd}, nil
}

func controllerArgs(controller string) []string { return []string{"-ext-ctl-unix", controller} }

// baseEnv is the minimal environment for a root-run Mihomo.
func baseEnv(_ string, spec protocol.Spec) []string {
	return []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + spec.WorkDir,
		"TMPDIR=/tmp",
	}
}

func parseUID(uid string) (int, error) {
	n, err := strconv.ParseUint(uid, 10, 32)
	if err != nil {
		return 0, errors.New("用户标识无效: " + uid)
	}
	return int(n), nil
}

// chownToUser gives path to uid (group unchanged).
func chownToUser(path string, uid string) error {
	n, err := parseUID(uid)
	if err != nil {
		return err
	}
	if err := os.Lchown(path, n, -1); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SocketWait is how long AfterStart waits for the controller socket.
const SocketWait = 5 * time.Second

func defaultHooks() Hooks {
	return Hooks{
		AfterStart: handOverSocket,
		AfterExit:  handOverWorkDir,
	}
}

// handOverSocket polls for the controller socket Mihomo creates and makes
// it owned by the user, mode 0600. Mihomo chmods it 0666 right after
// listening; the second pass a moment later wins that race.
func handOverSocket(ctx context.Context, spec protocol.Spec, uid string) {
	deadline := time.Now().Add(SocketWait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if fi, err := os.Lstat(spec.Controller); err == nil && fi.Mode()&os.ModeSocket != 0 {
			fix := func() {
				_ = chownToUser(spec.Controller, uid)
				_ = os.Chmod(spec.Controller, 0o600)
			}
			fix()
			select {
			case <-time.After(500 * time.Millisecond):
				fix()
			case <-ctx.Done():
			}
			return
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}
}

// workDirFromUID is the owner whose files handOverWorkDir gives away:
// root, i.e. what the root-run Mihomo wrote. Tests change it.
var workDirFromUID = 0

// handOverWorkDir gives the top-level entries of the work dir (cache.db,
// downloaded geodata, provider files written by root) back to the user so
// the app can manage them when running Mihomo unprivileged.
//
// The work dir belongs to the user, so nothing in it is trusted: the
// directory is reached without following symlinks, and only regular files
// with a single link and directories that are still owned by root are
// opened (O_NOFOLLOW), re-checked with fstat and fchown'ed through their
// descriptor. A symlink, hard link, FIFO or socket planted by the user is
// skipped.
func handOverWorkDir(spec protocol.Spec, uid string) {
	n, err := parseUID(uid)
	if err != nil {
		return
	}
	dirfd, err := openDirNoFollow(spec.WorkDir)
	if err != nil {
		return
	}
	defer unix.Close(dirfd)
	names, err := readDirNames(dirfd)
	if err != nil {
		return
	}
	for _, name := range names {
		_ = handOverEntry(dirfd, name, n)
	}
}

// readDirNames lists the directory open at dirfd.
func readDirNames(dirfd int) ([]string, error) {
	dup, err := unix.Dup(dirfd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "workdir")
	defer f.Close()
	return f.Readdirnames(-1)
}

// handOverEntry gives name (in dirfd) to uid if it is a root-owned regular
// file with one link or a root-owned directory.
func handOverEntry(dirfd int, name string, uid int) error {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if err := checkHandOver(&st, uid); err != nil {
		return err
	}
	if int(st.Uid) != workDirFromUID {
		return nil
	}
	return handOverFd(fd, uid)
}
