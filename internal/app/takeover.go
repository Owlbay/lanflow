package app

import (
	"context"
	"time"

	"lanflow/internal/core"
	"lanflow/internal/model"
)

// A Lanflow that died while its core kept running (the service process
// killed or crashed; the window process restarts it at once,
// docs/service-split.md S3): the core runs on in the privileged helper
// (TUN) or as an orphaned child, the system proxy and DNS still point at
// it. The next service takes that core over instead of treating the end
// as a crash: the proxy, TUN, DNS and connections carry on and nothing is
// asked. When the core cannot be taken over it is stopped and started
// again as the previous run had it, still without asking.

// strayRun is a core of this data directory found running at start.
type strayRun struct{ tun bool }

// findStray looks for a core the previous run left; called before run.
func (s *Service) findStray() bool {
	if s.d.stray == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	found, tun := s.d.stray.Find(ctx, s.workDir)
	if !found {
		return false
	}
	s.state.stray = &strayRun{tun: tun}
	s.alog.Infof("the previous run's core is still running (tun=%v)", tun)
	return true
}

// takeOver adopts the core findStray saw, or restarts it; false when
// there was none (bootstrap goes on as usual).
func (s *Service) takeOver() bool {
	s.mu.Lock()
	stray := s.state.stray
	s.state.stray = nil
	bin := s.state.coreBin
	set := copySettings(s.state.settings)
	s.mu.Unlock()
	if stray == nil || s.rootCtx.Err() != nil {
		return false
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	logFile := s.logFile
	if stray.tun {
		logFile = s.tunLogFile()
	}
	spec := core.Spec{Bin: bin, WorkDir: s.workDir, Config: s.runtime, Controller: s.ctrlAddr, LogFile: logFile}
	ctx, cancel := s.ctrlCtx(nil, 10*time.Second)
	err := s.d.mgr.Adopt(ctx, spec, s.runnerFor(stray.tun))
	cancel()
	if err == nil {
		s.alog.Infof("took over the running core (tun=%v)", stray.tun)
		s.adoptedLocked(set, stray.tun)
		return true
	}
	s.alog.Warnf("take over the running core: %v; starting it again", err)
	sctx, scancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := s.d.stray.Stop(sctx, s.workDir, stray.tun); err != nil {
		s.alog.Errorf("stop the previous run's core: %v", err)
	}
	scancel()
	_, proxyOurs := s.leftoverProxy()
	if err := s.startLocked(); err != nil {
		// As after an ordinary crash: offer to put the proxy back.
		s.detectRecovery()
		s.releaseDNS()
		s.em.emit(TopicNotice, TopicStatus)
		return true
	}
	if proxyOurs {
		s.reclaimProxyLocked()
	}
	return true
}

// adoptedLocked records the adopted core as running and brings up what a
// start brings up; opMu must be held.
func (s *Service) adoptedLocked(set model.Settings, tun bool) {
	vctx, vcancel := s.ctrlCtx(nil, ctrlTimeout)
	version, _ := s.d.ctrl.Version(vctx)
	vcancel()
	s.mu.Lock()
	st := &s.state.st
	st.Core = model.CoreRunning
	st.CoreError = ""
	if version != "" {
		st.CoreVersion = version
	}
	st.StartedAt = s.d.now()
	st.Mode, st.MixedPort, st.AllowLAN = set.Mode, set.MixedPort, set.AllowLAN
	st.TUN = tun
	s.state.runningTUN = tun
	s.setConnsLocked(nil)
	s.state.tracker = connTracker{}
	s.mu.Unlock()
	if _, ours := s.leftoverProxy(); ours {
		s.reclaimProxyLocked()
	}
	s.em.emit(TopicStatus, TopicTraffic, TopicConns)
	s.syncDNS()
	s.startStreams()
	s.afterConfigChange()
	s.scheduleMainProbe()
	s.goBackground(s.fetchYou)
}

// stopStray stops a core of ours that runs without the manager knowing
// it (left by a previous run); true when there was one.
func (s *Service) stopStray() bool {
	if s.d.stray == nil || s.d.mgr.Running() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	found, tun := s.d.stray.Find(ctx, s.workDir)
	if !found {
		return false
	}
	if err := s.d.stray.Stop(ctx, s.workDir, tun); err != nil {
		s.alog.Errorf("stop the previous run's core: %v", err)
		return false
	}
	s.alog.Infof("stopped the previous run's core (tun=%v)", tun)
	return true
}

// coreGoneWait bounds how long Shutdown waits for the mixed port of a
// stopped core to be released.
var coreGoneWait = 3 * time.Second

// ensureCoreGone makes sure the core Shutdown just stopped is gone: when
// its mixed port is still taken, the core outlived the stop (a helper
// that did not answer, a child that ignored the signal) and is stopped
// again through its PID file or the helper (stopStray). A port that stays
// taken is logged: the next start reports it (portTaken).
func (s *Service) ensureCoreGone(port int, lan bool) {
	if port <= 0 || s.d.portInUse == nil {
		return
	}
	busy := func() bool { return s.d.portInUse(port, lan) }
	if !busy() {
		return
	}
	if s.stopStray() {
		s.alog.Warnf("quit: the core outlived its stop; stopped it again")
	}
	deadline := time.Now().Add(coreGoneWait)
	for busy() {
		if !time.Now().Before(deadline) {
			owner := ""
			if s.d.portOwner != nil {
				owner = s.d.portOwner(port)
			}
			s.alog.Warnf("quit: the mixed port %d is still taken (%s)", port, firstNonEmpty(owner, "owner unknown"))
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
