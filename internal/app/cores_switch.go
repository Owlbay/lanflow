package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"lanflow/internal/core"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/platform/helper"
)

// Hot switch between installed cores (API.UseCore). Mihomo cannot replace
// its own binary, so the switch is a fast stop/start on the same runtime
// config, controller socket and ports: everything that can fail is done
// first (the new binary validates the current runtime.yaml with -t, and
// is registered with the TUN helper when one is installed), then the old
// process is stopped and the new one started at once. The system proxy
// keeps pointing at the same port throughout and is never touched, nor is
// the DNS; applications only see a sub-second blip. Any failure puts the
// previous core back and starts it again.

// UseCore implements API.UseCore.
func (s *Service) UseCore(version string) (err error) {
	version = normalizeVersion(version)
	var target, desiredPath string
	if version == "" {
		bin, _ := s.bundledCore()
		if bin == "" {
			return errors.New(coreMissingText("", core.ErrNotFound))
		}
		target, desiredPath = bin, ""
	} else {
		if err := checkVersionName(version); err != nil {
			return err
		}
		if !s.isInstalled(version) {
			return fmt.Errorf(i18n.T("内核 %s 未安装。"), version)
		}
		target = s.coreVersionPath(version)
		desiredPath = target
	}

	s.mu.Lock()
	dl := s.coreDLLocked()
	if dl.Active {
		s.mu.Unlock()
		return errors.New(i18n.T("内核正在下载中，请稍候。"))
	}
	prev := *dl
	label := version
	if label == "" {
		label = i18n.T("内置")
	}
	*dl = model.CoreDownload{Active: true, Phase: phaseVerify, Version: label}
	if prev.Version == version {
		dl.Bytes, dl.Total = prev.Bytes, prev.Total
	}
	s.mu.Unlock()
	s.em.emit(TopicCoreDownload)

	defer func() { s.logOp("use core "+label, err) }()
	err = s.switchCore(target, desiredPath, version)
	s.updateCoreDL(func(dl *model.CoreDownload) {
		if err != nil {
			*dl = model.CoreDownload{Version: label, Error: err.Error()}
		} else {
			*dl = model.CoreDownload{Version: label, Done: label, Bytes: dl.Bytes, Total: dl.Total}
		}
	})
	s.em.emit(TopicCores)
	return err
}

// switchCore performs the switch; the progress record is already active.
// desiredPath is the CorePath to store ("" for the bundled core).
func (s *Service) switchCore(target, desiredPath, version string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	prevState := s.state.st.Core
	prevPath := s.state.settings.CorePath
	prevVersion := s.state.settings.CoreVersion
	prevBin := s.state.coreBin
	helperOK := s.state.helperOK
	tunRunner := s.state.tunRunner
	s.mu.Unlock()
	wasRunning := prevState == model.CoreRunning || prevState == model.CoreStarting
	// A core that could not start (missing file, failed start) is given
	// another go with the new binary once there is a subscription.
	_, hasSub := s.d.store.Current()
	retryStart := !wasRunning && hasSub && (prevState == model.CoreMissing || prevState == model.CoreError)

	if prevPath == desiredPath && prevVersion == version && prevBin == target {
		return nil
	}
	if prevBin == target {
		// Same file, different bookkeeping (a custom path that is one of ours).
		s.setActiveCore(desiredPath, version)
		return nil
	}

	// 1. Everything that can fail without touching the running core.
	ctx, cancel := s.ctrlCtx(nil, 2*time.Minute)
	defer cancel()
	reported, err := s.d.core.Version(ctx, target)
	if err != nil {
		return fmt.Errorf(i18n.T("内核无法运行：%v"), err)
	}
	if _, err := os.Stat(s.runtime); err == nil {
		if err := s.d.core.Validate(ctx, target, s.workDir, s.runtime); err != nil {
			return fmt.Errorf(i18n.T("新内核无法运行当前配置，已保留现有内核：%v"), err)
		}
	}
	// The helper only runs the binary recorded at install time (path +
	// SHA-256), so the new core is registered first. Registering restarts
	// the helper, which takes a helper-run (TUN) core down with it, so in
	// that case it waits until the old core is stopped anyway.
	registered := false
	if helperOK && !(wasRunning && tunRunner) {
		if err := s.registerHelper(target); err != nil {
			_ = s.registerHelper(prevBin)
			return fmt.Errorf(i18n.T("内核已下载，但权限组件未能登记新内核（%v），已恢复原内核。可稍后重试，或先在设置中卸载权限组件再更新。"), err)
		}
		registered = true
	}

	// 2. Stop the old core. The system proxy and DNS are left as they are.
	s.updateCoreDL(func(dl *model.CoreDownload) { dl.Phase = phaseInstall })
	if wasRunning {
		s.stopStreams()
		sctx, scancel := context.WithTimeout(ctx, 15*time.Second)
		_ = s.d.mgr.Stop(sctx)
		scancel()
		s.mu.Lock()
		s.state.st.Core = model.CoreStopped
		s.mu.Unlock()
		s.em.emit(TopicStatus)
	}
	if helperOK && !registered {
		if err := s.registerHelper(target); err != nil {
			_ = s.registerHelper(prevBin)
			return s.afterSwapFailure(wasRunning, fmt.Errorf(i18n.T("内核已下载，但权限组件未能登记新内核（%v），已恢复原内核。可稍后重试，或先在设置中卸载权限组件再更新。"), err))
		}
	}

	// 3. Point the settings at the new core.
	rollback := func() {
		s.setActiveCore(prevPath, prevVersion)
		if helperOK && prevBin != "" {
			_ = s.registerHelper(prevBin)
		}
	}
	s.setActiveCore(desiredPath, version)
	s.mu.Lock()
	resolved := s.state.coreBin
	s.mu.Unlock()
	if resolved != target {
		rollback()
		return s.afterSwapFailure(wasRunning, errors.New(i18n.T("下载的内核文件无法使用，已恢复原内核。")))
	}

	// 4. Start the new core on the same config, or just record its version.
	from := describeCore(prevVersion, prevPath)
	to := describeCore(version, desiredPath)
	if wasRunning {
		s.updateCoreDL(func(dl *model.CoreDownload) { dl.Phase = phaseStart })
		if err := s.relaunchLocked(); err != nil {
			rollback()
			s.alog.Errorf("core switch %s → %s failed, rolled back: %v", from, to, err)
			if rerr := s.relaunchLocked(); rerr != nil {
				return fmt.Errorf(i18n.T("新内核启动失败：%v。已恢复原内核，但原内核也未能启动：%v"), err, rerr)
			}
			return fmt.Errorf(i18n.T("新内核启动失败：%v。已恢复并重新启动原内核。"), err)
		}
	} else if retryStart {
		s.updateCoreDL(func(dl *model.CoreDownload) { dl.Phase = phaseStart })
		if err := s.startLocked(); err != nil {
			rollback()
			s.alog.Errorf("core switch %s → %s: start failed, rolled back: %v", from, to, err)
			return fmt.Errorf(i18n.T("新内核启动失败：%v。已恢复原内核。"), err)
		}
	} else {
		s.mu.Lock()
		s.state.st.CoreVersion = normalizeVersion(reported)
		s.mu.Unlock()
		s.em.emit(TopicStatus)
	}
	s.alog.Infof("core switched: %s → %s (%s)", from, to, normalizeVersion(reported))
	return nil
}

// describeCore names a core for the log.
func describeCore(version, path string) string {
	switch {
	case version != "":
		return version
	case path != "":
		return path
	}
	return "bundled"
}

// registerHelper records bin as the core the TUN helper may run; this asks
// for administrator rights, like installing the helper.
func (s *Service) registerHelper(bin string) error {
	if bin == "" {
		return nil
	}
	hctx, cancel := s.ctrlCtx(nil, 2*time.Minute)
	defer cancel()
	err := s.d.helper.Install(hctx, helper.InstallOptions{HelperBin: s.opt.helperBinary(), CoreBin: bin, DataDir: s.opt.DataDir})
	s.refreshHelper()
	return err
}

// afterSwapFailure starts the previous core again when the switch failed
// after it was stopped, and returns err.
func (s *Service) afterSwapFailure(wasRunning bool, err error) error {
	if wasRunning {
		if rerr := s.relaunchLocked(); rerr != nil {
			return fmt.Errorf(i18n.T("%v 原内核重新启动也失败了：%v"), err, rerr)
		}
	}
	return err
}

// setActiveCore persists CorePath / CoreVersion and re-resolves the
// binary; opMu must be held.
func (s *Service) setActiveCore(path, version string) {
	_ = s.updateSettings(func(set *model.Settings) {
		set.CorePath = path
		set.CoreVersion = version
		s.resolveBinaryLocked()
	})
	s.em.emit(TopicSettings, TopicStatus)
}

// relaunchLocked starts the resolved core on the runtime config that is
// already on disk, skipping the recompose startLocked does; opMu must be
// held. Without a runtime config yet it falls back to a full start.
func (s *Service) relaunchLocked() error {
	if s.rootCtx.Err() != nil {
		return errors.New(i18n.T("澜流正在退出，内核不再启动。"))
	}
	if _, err := os.Stat(s.runtime); err != nil {
		return s.startLocked()
	}
	s.mu.Lock()
	s.resolveBinaryLocked()
	bin := s.state.coreBin
	set := copySettings(s.state.settings)
	helperOK := s.state.helperOK
	if bin != "" {
		s.state.st.Core = model.CoreStarting
		s.state.st.CoreError = ""
	}
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	if bin == "" {
		return errors.New(coreMissingText(set.CorePath, core.ErrNotFound))
	}
	return s.launchLocked(bin, set, helperOK, model.CoreStopped)
}
