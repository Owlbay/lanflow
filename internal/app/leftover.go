package app

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lanflow/internal/fsx"
)

// What a Lanflow that did not exit cleanly can leave behind, and how the
// next one clears it before it starts (an update restart, a window that
// replaced an older service, a service that starts after a crash):
//
//   - a core: an orphaned child named by core/mihomo.pid, or the core the
//     privileged helper runs for TUN (strayCores, takeover.go);
//   - decryption processes (`lanflow capture-agent`), each named by a file
//     capture/agents/<pid> while it runs;
//   - the mixed port, held by either of those, or by another program.
//
// Nothing is ended unless it is ours: a core only through its PID file
// (still a mihomo executable) or the helper, an agent only while its PID
// still runs `capture-agent` (agentProc.is).

// captureAgentsDir holds one empty file per running agent, named by its
// PID; the file's time is when the agent started.
const captureAgentsDir = "agents"

func agentsDir(dataDir string) string { return filepath.Join(dataDir, captureDir, captureAgentsDir) }

// recordAgent notes a started agent; the returned func forgets it once it
// exited.
func recordAgent(dataDir string, pid int) func() {
	dir := agentsDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}
	}
	path := filepath.Join(dir, strconv.Itoa(pid))
	_ = fsx.WriteFile(path, nil, 0o600)
	return func() { _ = os.Remove(path) }
}

// agentProc are the process calls for leftover agents; tests replace them.
var agentProc = struct {
	// is reports whether pid still runs a decryption process that started
	// no later than recorded (a PID reused since is someone else's).
	is    func(pid int, recorded time.Time) bool
	alive func(pid int) bool
	term  func(pid int) error
	kill  func(pid int) error
	wait  time.Duration // after term, before kill
	poll  time.Duration
}{isCaptureAgent, procAlive, procTerminate, procKill, 3 * time.Second, 100 * time.Millisecond}

// stopAgentLeftovers ends the decryption processes a previous run left
// and forgets their records; it returns how many it ended.
func stopAgentLeftovers(dataDir string, logf func(string, ...any)) int {
	dir := agentsDir(dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		pid, err := strconv.Atoi(e.Name())
		info, ierr := e.Info()
		if err != nil || pid <= 0 || pid == os.Getpid() || ierr != nil {
			_ = os.Remove(path)
			continue
		}
		if agentProc.alive(pid) && agentProc.is(pid, info.ModTime()) {
			logf("a decryption process of the previous run is still running (pid %d); ending it", pid)
			_ = agentProc.term(pid)
			if !waitProcGone(pid, agentProc.wait) {
				_ = agentProc.kill(pid)
				if !waitProcGone(pid, agentProc.wait) {
					logf("the decryption process %d did not end", pid)
				}
			}
			n++
		}
		_ = os.Remove(path)
	}
	return n
}

func waitProcGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for agentProc.alive(pid) {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(agentProc.poll)
	}
	return true
}

// LeftoverOptions describe StopLeftovers.
type LeftoverOptions struct {
	DataDir string
	// MixedPort (with AllowLAN) is checked once the rest is cleared; 0
	// skips the check.
	MixedPort int
	AllowLAN  bool
	// PortWait is how long the mixed port may take to be released
	// (default 5 s).
	PortWait time.Duration
	Logf     func(format string, args ...any) // default log.Printf
}

// LeftoverReport says what StopLeftovers found.
type LeftoverReport struct {
	Core    bool // a core of the previous run was stopped
	CoreTUN bool // ... in the privileged helper
	CoreErr error
	Agents  int // decryption processes ended
	// PortBusy: the mixed port is still taken afterwards, by PortOwner
	// when known ("" = unknown).
	PortBusy  bool
	PortOwner string
}

// leftoverDeps are what StopLeftovers uses; tests replace them.
type leftoverDeps struct {
	stray     strayCores
	portInUse func(port int, lan bool) bool
	portOwner func(port int) string
	poll      time.Duration
}

// StopLeftovers clears what a Lanflow that is gone left behind: its core
// (an orphaned child, or the helper's TUN core), its decryption processes,
// and waits for the mixed port. The window process calls it after it
// ended the old service (an update restart, split.Connect replacing an
// older service), before the new service starts. Every step is bounded;
// a port still taken is logged with its owner when known (the new service
// then says so too, portTaken).
func StopLeftovers(o LeftoverOptions) LeftoverReport {
	return stopLeftovers(o, leftoverDeps{stray: realStray{}, portInUse: tcpPortInUse, portOwner: portOwner, poll: 100 * time.Millisecond})
}

func stopLeftovers(o LeftoverOptions, d leftoverDeps) LeftoverReport {
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	if o.PortWait == 0 {
		o.PortWait = 5 * time.Second
	}
	logf := func(f string, a ...any) { o.Logf("lanflow: leftovers: "+f, a...) }
	var r LeftoverReport
	workDir := filepath.Join(o.DataDir, "core")
	if d.stray != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if found, tun := d.stray.Find(ctx, workDir); found {
			r.Core, r.CoreTUN = true, tun
			logf("the previous run's core is still running (tun=%v); stopping it", tun)
			if err := d.stray.Stop(ctx, workDir, tun); err != nil {
				r.CoreErr = err
				logf("stop the previous run's core: %v", err)
			}
		}
		cancel()
	}
	r.Agents = stopAgentLeftovers(o.DataDir, logf)
	if o.MixedPort > 0 && d.portInUse != nil {
		deadline := time.Now().Add(o.PortWait)
		for d.portInUse(o.MixedPort, o.AllowLAN) {
			if !time.Now().Before(deadline) {
				r.PortBusy = true
				break
			}
			time.Sleep(d.poll)
		}
		if r.PortBusy {
			if d.portOwner != nil {
				r.PortOwner = d.portOwner(o.MixedPort)
			}
			logf("the mixed port %d is still taken (%s)", o.MixedPort, firstNonEmpty(r.PortOwner, "owner unknown"))
		}
	}
	return r
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// portOwnerText names the program that holds a port for the user.
func portOwnerText(owner string) string { return strings.TrimSpace(owner) }

// parseNetstat finds the PID listening on TCP port in `netstat -ano`
// (Windows): "  TCP    127.0.0.1:7890    0.0.0.0:0    LISTENING    1234".
// The state is localized on some systems, so a line counts when its
// foreign address is a wildcard; 0 = none.
func parseNetstat(out string, port int) int {
	suffix := ":" + strconv.Itoa(port)
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 5 || !strings.EqualFold(f[0], "TCP") || !strings.HasSuffix(f[1], suffix) {
			continue
		}
		if !strings.HasSuffix(f[2], ":0") && !strings.HasSuffix(f[2], ":*") {
			continue
		}
		if pid, err := strconv.Atoi(f[len(f)-1]); err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}
