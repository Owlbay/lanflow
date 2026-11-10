package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/platform/helper"
)

// SetSystemProxy points the system proxy at the core (starting it first if
// needed) or clears it.
func (s *Service) SetSystemProxy(on bool) (err error) {
	defer func() { s.logOp("system proxy "+onOff(on), err) }()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.setSystemProxyLocked(on)
}

// setSystemProxyLocked implements SetSystemProxy; opMu must be held.
func (s *Service) setSystemProxyLocked(on bool) error {
	if !on {
		if err := s.releaseProxy(); err != nil {
			return fmt.Errorf(i18n.T("关闭系统代理失败：%v。请在系统设置中手动关闭代理。"), err)
		}
		s.mu.Lock()
		s.state.weSetProxy = false
		s.state.st.SystemProxy = false
		s.state.st.PendingRecovery = false
		s.mu.Unlock()
		s.em.emit(TopicStatus)
		return nil
	}
	if !s.running() {
		if err := s.startLocked(); err != nil {
			return err
		}
	}
	if err := s.applySystemProxy(); err != nil {
		return err
	}
	s.mu.Lock()
	s.state.weSetProxy = true
	s.state.st.SystemProxy = true
	s.state.st.PendingRecovery = false
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	return nil
}

// applySystemProxy points the system proxy at the core the way the
// settings ask: a PAC file served by Lanflow (设置 › 高级 › PAC 模式) or a
// manual proxy at 127.0.0.1:<mixed port>. The settings in place before
// are remembered in the marker so releaseProxy can put them back.
func (s *Service) applySystemProxy() error {
	s.mu.Lock()
	port := s.state.settings.MixedPort
	bypass := s.bypassLocked()
	adv := s.state.settings.Advanced
	s.mu.Unlock()
	// Remember what was there before (another app's proxy, or off) so
	// turning ours off puts it back instead of just switching it off.
	m, owned := s.readMarker()
	saved := m.Saved
	if !owned {
		saved, _ = s.d.sys.Snapshot() // best effort: without it we fall back to Clear
	}
	if adv.PACMode {
		url, err := s.startPAC(adv.PACPort)
		if err != nil {
			return err
		}
		if err := s.d.sys.SetPAC(url); err != nil {
			return fmt.Errorf(i18n.T("开启 PAC 代理失败：%v。内核仍在运行，可以手动把系统代理指向 127.0.0.1:%d。"), err, port)
		}
		s.writeMarkerPAC(port, saved, url)
		return nil
	}
	if err := s.d.sys.Set("127.0.0.1", port, bypass); err != nil {
		return fmt.Errorf(i18n.T("开启系统代理失败：%v。内核仍在运行，可以手动把系统代理指向 127.0.0.1:%d。"), err, port)
	}
	s.writeMarker(port, saved)
	return nil
}

// checkOtherTUN refuses to turn TUN on while another client's TUN owns the
// default route: two TUNs fight over routes and DNS and break the network.
func (s *Service) checkOtherTUN() error {
	s.mu.Lock()
	ours := s.state.runningTUN
	s.mu.Unlock()
	if ours {
		return nil // the TUN we see is our own
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if name, found := s.d.otherTUN(ctx); found {
		return fmt.Errorf(i18n.T("检测到其他代理软件的 TUN（%s）正在接管网络。两个 TUN 同时开启会互相冲突，请先在那款软件中关闭 TUN 模式，再开启澜流的 TUN"), name)
	}
	return nil
}

// releaseProxy undoes Lanflow's system proxy change. While the system
// proxy still points at us it restores the settings saved before we set it
// (another app's proxy, or off; Clear when no snapshot exists). If someone
// else has changed it since, it is left alone. The marker is removed.
func (s *Service) releaseProxy() error {
	m, _ := s.readMarker()
	port := m.Port
	if port == 0 {
		s.mu.Lock()
		port = s.state.settings.MixedPort
		s.mu.Unlock()
	}
	cur, err := s.d.sys.Get()
	if err == nil && !proxyIsOurs(cur, m, port) {
		s.removeMarker() // not ours any more: leave it as it is
		return nil
	}
	if len(m.Saved) > 0 {
		if err := s.d.sys.Restore(m.Saved); err == nil {
			s.removeMarker()
			return nil
		}
	}
	if m.PAC != "" {
		if err := s.d.sys.SetPAC(""); err != nil {
			return err
		}
	} else if err := s.d.sys.Clear(); err != nil {
		return err
	}
	s.removeMarker()
	return nil
}

// ResolveRecovery answers the "recovery" notice: restore = turn the leftover
// system proxy off; otherwise keep it and start the core.
func (s *Service) ResolveRecovery(restore bool) (err error) {
	defer func() { s.logOp(fmt.Sprintf("recovery restore=%v", restore), err) }()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	pending := s.state.st.PendingRecovery
	s.state.st.PendingRecovery = false
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	if !pending {
		return nil
	}
	if restore {
		if err := s.releaseProxy(); err != nil {
			return fmt.Errorf(i18n.T("关闭系统代理失败：%v。请在系统设置中手动关闭代理。"), err)
		}
		s.mu.Lock()
		s.state.weSetProxy = false
		s.state.st.SystemProxy = false
		s.mu.Unlock()
		s.em.emit(TopicStatus)
		return nil
	}
	if err := s.startLocked(); err != nil {
		return err
	}
	// The proxy already points at us; take ownership again. In PAC mode
	// the PAC server has to be up again at the recorded URL.
	if m, ok := s.readMarker(); ok && m.PAC != "" {
		s.mu.Lock()
		pacPort := s.state.settings.Advanced.PACPort
		s.mu.Unlock()
		if url, err := s.startPAC(pacPort); err == nil && url != m.PAC {
			if err := s.d.sys.SetPAC(url); err == nil {
				s.writeMarkerPAC(m.Port, m.Saved, url)
			}
		}
	}
	s.mu.Lock()
	s.state.weSetProxy = true
	s.state.st.SystemProxy = true
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	return nil
}

// ---- TUN ----

// TUNHelperInstalled reports the last known helper state (refreshed at
// start and after install/uninstall).
func (s *Service) TUNHelperInstalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.helperOK
}

// SetTUN turns TUN mode on or off. On requires the helper; the core is
// restarted through the helper (on) or as the current user (off).
func (s *Service) SetTUN(on bool) (err error) {
	defer func() { s.logOp("TUN "+onOff(on), err) }()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if on && !s.refreshHelper() {
		return ErrHelperRequired
	}
	if on {
		if err := s.checkOtherTUN(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.state.settings.TUN = on
	set := copySettings(s.state.settings)
	wasRunning := s.state.st.Core == model.CoreRunning
	s.mu.Unlock()
	s.em.emit(TopicSettings)
	if err := saveSettings(s.opt.DataDir, set); err != nil {
		return err
	}
	if !wasRunning {
		return nil
	}
	if err := s.restartLocked(); err != nil {
		return fmt.Errorf(i18n.T("切换 TUN 模式后重新启动内核失败：%v"), err)
	}
	return nil
}

// InstallTUNHelper installs the privileged helper (administrator prompt).
func (s *Service) InstallTUNHelper() (err error) {
	defer func() { s.logOp("install helper", err) }()
	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()
	if bin == "" {
		return errors.New(i18n.T("未找到 Mihomo 内核，无法安装权限组件。请先在设置 › 内核中选择内核文件。"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err = s.d.helper.Install(ctx, helper.InstallOptions{HelperBin: s.opt.helperBinary(), CoreBin: bin, DataDir: s.opt.DataDir})
	s.refreshHelper()
	if err != nil {
		return fmt.Errorf(i18n.T("安装权限组件失败：%v"), err)
	}
	return nil
}

// UninstallTUNHelper removes the helper; TUN is turned off first when active.
func (s *Service) UninstallTUNHelper() (err error) {
	defer func() { s.logOp("uninstall helper", err) }()
	s.mu.Lock()
	tunOn := s.state.settings.TUN
	s.mu.Unlock()
	if tunOn {
		if err := s.SetTUN(false); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err = s.d.helper.Uninstall(ctx)
	s.refreshHelper()
	if err != nil {
		return fmt.Errorf(i18n.T("卸载权限组件失败：%v"), err)
	}
	return nil
}

// ---- mode ----

// SetMode switches the routing mode (hot) and persists it.
func (s *Service) SetMode(m model.Mode) (err error) {
	defer func() { s.logOp("mode "+string(m), err) }()
	switch m {
	case model.ModeRule, model.ModeGlobal, model.ModeDirect:
	default:
		return fmt.Errorf(i18n.T("未知的模式「%s」"), m)
	}
	if s.running() {
		mode := string(m)
		ctx, cancel := context.WithTimeout(context.Background(), ctrlTimeout)
		err := s.d.ctrl.PatchConfigs(ctx, controller.ConfigPatch{Mode: &mode})
		cancel()
		if err != nil {
			return fmt.Errorf(i18n.T("切换模式失败：%v"), err)
		}
		if m == model.ModeGlobal {
			s.ensureGlobalExit()
		}
	}
	s.mu.Lock()
	s.state.settings.Mode = m
	s.state.st.Mode = m
	set := copySettings(s.state.settings)
	s.mu.Unlock()
	s.em.emit(TopicStatus, TopicSettings)
	s.rebuildRoutes()
	return saveSettings(s.opt.DataDir, set)
}

// ensureGlobalExit points Mihomo's GLOBAL group at the main group when it
// is still on its default: the group starts on DIRECT, so global mode
// would send everything direct until the user picks a node in it (seen
// in the VM e2e run: every packet left en0 unproxied in global mode).
func (s *Service) ensureGlobalExit() {
	s.refreshProxies()
	px := s.Proxies()
	var g *model.ProxyGroup
	for i := range px.Groups {
		if px.Groups[i].Name == "GLOBAL" {
			g = &px.Groups[i]
		}
	}
	if g == nil || (g.Now != "" && g.Now != "DIRECT" && g.Now != "REJECT") {
		return
	}
	target := ""
	if slices.Contains(g.All, px.MainGroup) {
		target = px.MainGroup
	} else {
		for _, n := range g.All {
			if n != "DIRECT" && n != "REJECT" && n != "REJECT-DROP" && n != "PASS" && n != "COMPATIBLE" {
				target = n
				break
			}
		}
	}
	if target == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctrlTimeout)
	err := s.d.ctrl.SelectProxy(ctx, "GLOBAL", target)
	cancel()
	if err != nil {
		s.alog.Warnf("global mode: GLOBAL left on %s (select %s: %v)", g.Now, target, err)
		return
	}
	s.alog.Infof("global mode: GLOBAL -> %s", target)
	s.refreshProxies()
}
