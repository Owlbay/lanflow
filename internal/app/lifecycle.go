package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lanflow/internal/core"
	"lanflow/internal/fsx"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

const (
	startTimeout    = 40 * time.Second
	validateTimeout = 20 * time.Second
	ctrlTimeout     = 10 * time.Second
	autoRestartGap  = 60 * time.Second // a second crash within this window is not restarted
)

var errNoSubscription = i18n.Error("还没有订阅。请先在「订阅」页导入订阅，再启动内核。")

// ---- composition ----

// composeArgs selects the inputs of one composition. Zero values mean
// "use what is stored"; pointers override one layer (override preview/save).
type composeArgs struct {
	subID string // "" = current subscription
	// ov, when set, takes the place of the override-list entry with its ID
	// (appended when new) for this composition (override preview/save).
	ov *model.Override
	// ovs, when set, is the whole override list for this composition
	// (loading lanflow.yaml) instead of the stored one.
	ovs *[]model.Override
	// subOverrides, when set, is the subscription's override selection for
	// this composition instead of the stored one.
	subOverrides *[]string
	custom       []model.CustomRule
	useRules     bool            // custom is authoritative even when nil
	settings     *model.Settings // nil = current settings
	content      []byte          // nil = stored content of subID
}

// buildInput assembles profile.Input from the store and settings. It reads
// the store (disk) and must not be called with mu held.
func (s *Service) buildInput(a composeArgs) (profile.Input, model.Subscription, error) {
	var sub model.Subscription
	var ok bool
	if a.subID == "" {
		sub, ok = s.d.store.Current()
	} else {
		sub, ok = s.d.store.Get(a.subID)
	}
	if !ok {
		return profile.Input{}, sub, errNoSubscription
	}
	data := a.content
	if data == nil {
		var err error
		data, err = s.d.store.Content(sub.ID)
		if err != nil {
			return profile.Input{}, sub, fmt.Errorf(i18n.T("读取订阅「%s」的内容失败：%v"), sub.Name, err)
		}
	}
	s.mu.Lock()
	set := copySettings(s.state.settings)
	probe := s.state.probePort
	helperOK := s.state.helperOK
	co := s.state.coexist
	sysDNS := s.state.net.DNS
	s.mu.Unlock()
	if a.settings != nil {
		set = *a.settings
	}
	layerSub := sub
	if a.subOverrides != nil {
		layerSub.Overrides = *a.subOverrides
	}
	list := s.d.store.Overrides()
	if a.ovs != nil {
		list = *a.ovs
	}
	in := profile.Input{
		Subscription: data,
		Overrides:    profile.Layers(substituteOverride(list, a.ov), layerSub),
		CustomRules:  s.d.store.CustomRules(),
		DNS:          set.DNS,
		Sniffer:      set.Sniffer,
		Geo:          mirroredGeo(set), // the download mirror applies to geox-url (mirror.go)
		Managed: profile.Managed{
			MixedPort: set.MixedPort,
			AllowLAN:  set.AllowLAN,
			Mode:      set.Mode,
			LogLevel:  set.LogLevel,
			TUN:       set.TUN && helperOK,
			TUNStack:  set.TUNStack,
			ProbePort: probe,
			IPv6:      set.Advanced.IPv6,
		},
		Advanced: set.Advanced,
	}
	in.Coexist = coexistInput(co, set, sysDNS)
	if a.useRules {
		in.CustomRules = a.custom
	}
	return in, sub, nil
}

// substituteOverride returns list with o in place of the entry of the same
// ID (appended when none has it); a nil o leaves the list as is.
func substituteOverride(list []model.Override, o *model.Override) []model.Override {
	if o == nil {
		return list
	}
	out := append([]model.Override(nil), list...)
	for i := range out {
		if out[i].ID == o.ID {
			out[i] = *o
			return out
		}
	}
	return append(out, *o)
}

// prepared is a composed and validated config waiting to be committed.
type prepared struct {
	out profile.Output
	tmp string // a uniquely named temp file next to runtime.yaml
}

// discard removes a prepared file that will not be committed.
func (p prepared) discard() {
	if p.tmp != "" {
		os.Remove(p.tmp)
	}
}

// prepare composes and validates (mihomo -t) the config, writing it to a
// uniquely named temp file next to the runtime file, so concurrent prepares
// (an import's check, a reload) never share or delete each other's file.
// The current runtime file is untouched; commit or discard the result.
func (s *Service) prepare(a composeArgs) (prepared, error) {
	in, _, err := s.buildInput(a)
	if err != nil {
		return prepared{}, err
	}
	out, err := s.d.prof.Compose(in)
	if err != nil {
		return prepared{}, err
	}
	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()
	if bin == "" {
		return prepared{}, errors.New(coreMissingText(s.Settings().CorePath, core.ErrNotFound))
	}
	if err := os.MkdirAll(s.workDir, 0o700); err != nil {
		return prepared{}, fmt.Errorf(i18n.T("无法创建内核工作目录：%v"), err)
	}
	f, err := fsx.CreateTemp(s.runtime, 0o600)
	if err != nil {
		return prepared{}, fmt.Errorf(i18n.T("写入运行时配置失败：%v"), err)
	}
	p := prepared{out: out, tmp: f.Name()}
	_, werr := f.Write(out.YAML)
	if werr == nil {
		werr = f.Sync() // commit is then a bare rename
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		p.discard()
		return prepared{}, fmt.Errorf(i18n.T("写入运行时配置失败：%v"), werr)
	}
	ctx, cancel := s.ctrlCtx(nil, validateTimeout)
	defer cancel()
	if err := s.d.core.Validate(ctx, bin, s.workDir, p.tmp); err != nil {
		p.discard()
		return prepared{}, fmt.Errorf(i18n.T("配置未通过内核校验：%v"), err)
	}
	return p, nil
}

// commit moves the prepared file over runtime.yaml in one rename, keeping a
// copy of the previous one as runtime.yaml.bak, and records the composed
// lines. runtime.yaml exists at every moment.
func (s *Service) commit(p prepared) error {
	bak := s.runtime + ".bak"
	if old, err := os.ReadFile(s.runtime); err == nil {
		if err := fsx.WriteFile(bak, old, 0o600); err != nil {
			p.discard()
			return fmt.Errorf(i18n.T("备份运行时配置失败：%v"), err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		p.discard()
		return fmt.Errorf(i18n.T("备份运行时配置失败：%v"), err)
	} else {
		os.Remove(bak) // no previous config: nothing to roll back to
	}
	if err := fsx.Rename(p.tmp, s.runtime); err != nil {
		p.discard()
		return fmt.Errorf(i18n.T("写入运行时配置失败：%v"), err)
	}
	icons := groupIconsOf(p.out.YAML)
	rd := parseRuntimeDNS(p.out.YAML)
	s.mu.Lock()
	s.state.runtimeYAML = p.out.YAML
	s.state.runtimeDNS = rd
	s.state.runtimeLines = nil // annotated on demand (RuntimeConfig): one entry per line adds up
	s.state.groupIcons = icons
	s.mu.Unlock()
	return nil
}

// rollback restores runtime.yaml.bak over runtime.yaml (one rename) after a
// failed reload; without a backup there is nothing to restore.
func (s *Service) rollback() error {
	bak := s.runtime + ".bak"
	if _, err := os.Stat(bak); err != nil {
		return nil
	}
	if err := fsx.Rename(bak, s.runtime); err != nil {
		return fmt.Errorf("restore runtime config: %w", err) // logged only
	}
	return nil
}

// ---- core lifecycle ----

// StartCore composes, validates and starts the core.
func (s *Service) StartCore() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.startLocked()
}

// startLocked starts the core; opMu must be held. A running core is restarted.
func (s *Service) startLocked() (err error) {
	defer func() { s.logOp("start core", err) }()
	if s.rootCtx.Err() != nil {
		return errors.New(i18n.T("澜流正在退出，内核不再启动。"))
	}
	s.mu.Lock()
	s.resolveBinaryLocked()
	bin := s.state.coreBin
	set := copySettings(s.state.settings)
	helperOK := s.state.helperOK
	prev := s.state.st.Core
	if bin != "" {
		s.state.st.Core = model.CoreStarting
		s.state.st.CoreError = ""
	}
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	if bin == "" {
		return errors.New(coreMissingText(set.CorePath, core.ErrNotFound))
	}
	fail := s.failStart
	if prev != model.CoreRunning && s.d.portInUse(set.MixedPort, set.AllowLAN) {
		return fail(portTakenError(set.MixedPort), model.CoreError)
	}
	s.detectCoexist() // compose with aTrust's routes when it is already running
	p, err := s.prepare(composeArgs{})
	if err != nil {
		if errors.Is(err, errNoSubscription) {
			return fail(err, model.CoreStopped)
		}
		return fail(err, model.CoreError)
	}
	if err := s.commit(p); err != nil {
		return fail(err, model.CoreError)
	}
	return s.launchLocked(bin, set, helperOK, prev)
}

// failStart records a failed start in Status and returns err.
func (s *Service) failStart(err error, st model.CoreState) error {
	s.mu.Lock()
	s.state.st.Core = st
	s.state.st.CoreError = err.Error()
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	return err
}

// launchLocked starts bin on the committed runtime config and brings the
// streams up; opMu must be held. startLocked calls it after composing;
// the core switch (cores_switch.go) calls it directly so the new binary
// starts on the very same config without a recompose.
func (s *Service) launchLocked(bin string, set model.Settings, helperOK bool, prev model.CoreState) error {
	fail := s.failStart
	if prev == model.CoreRunning {
		s.stopStreams()
	}
	useTUN := set.TUN && helperOK
	if useTUN && prev != model.CoreRunning {
		// e.g. at launch with TUN saved on while another client's TUN runs:
		// start as a plain proxy and say why, rather than break the network.
		if err := s.checkOtherTUN(); err != nil {
			useTUN = false
			s.notify(Notice{Kind: "error", Title: i18n.T("TUN 模式未开启"), Detail: err.Error()})
		}
	}
	s.d.mgr.SetRunner(s.runnerFor(useTUN))
	logFile := s.logFile
	if useTUN {
		logFile = s.tunLogFile()
	}
	spec := core.Spec{Bin: bin, WorkDir: s.workDir, Config: s.runtime, Controller: s.ctrlAddr, LogFile: logFile}
	ctx, cancel := s.ctrlCtx(nil, startTimeout)
	err := s.d.mgr.Start(ctx, spec)
	cancel()
	if err != nil {
		return fail(fmt.Errorf(i18n.T("内核启动失败：%v。可在「日志」中查看详情，或重新启动。"), err), model.CoreError)
	}
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
	st.TUN = useTUN
	s.state.runningTUN = useTUN
	st.Up, st.Down, st.TotalUp, st.TotalDown, st.Memory, st.Connections = 0, 0, 0, 0, 0, 0
	s.state.up, s.state.down = nil, nil
	s.state.conns = nil
	s.state.tracker = connTracker{}
	s.state.youDone = false
	s.mu.Unlock()
	s.em.emit(TopicStatus, TopicTraffic, TopicConns)

	s.syncDNS()
	s.startStreams()
	s.afterConfigChange()
	s.scheduleMainProbe()
	s.goBackground(s.fetchYou)
	return nil
}

// tunLogFile is where the helper-run (root) core logs. The helper only lets
// root write inside the data directory, so the log lives next to the
// runtime config; a link in the log directory keeps it one click away.
func (s *Service) tunLogFile() string {
	p := filepath.Join(s.workDir, "mihomo-tun.log")
	link := filepath.Join(s.opt.LogDir, "mihomo-tun.log")
	if _, err := os.Lstat(link); errors.Is(err, os.ErrNotExist) {
		_ = os.Symlink(p, link) // best effort (may need privileges on Windows)
	}
	return p
}

// runnerFor picks the exec or helper runner and remembers which one is active.
func (s *Service) runnerFor(tun bool) core.Runner {
	s.mu.Lock()
	s.state.tunRunner = tun
	s.mu.Unlock()
	if tun {
		return s.d.core.HelperRunner()
	}
	return s.d.core.ExecRunner()
}

// StopCore stops the core and turns the system proxy off if Lanflow set it.
func (s *Service) StopCore() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.stopLocked()
}

// stopLocked stops the core; opMu must be held.
func (s *Service) stopLocked() (err error) {
	defer func() { s.logOp("stop core", err) }()
	s.stopStreams()
	s.mu.Lock()
	ours := s.state.weSetProxy
	s.mu.Unlock()
	var proxyErr error
	if ours {
		if proxyErr = s.releaseProxy(); proxyErr == nil {
			s.mu.Lock()
			s.state.weSetProxy = false
			s.state.st.SystemProxy = false
			s.mu.Unlock()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = s.d.mgr.Stop(ctx)
	cancel()
	s.mu.Lock()
	st := &s.state.st
	st.Core = model.CoreStopped
	st.CoreError = ""
	st.TUN = false
	s.state.runningTUN = false
	st.Up, st.Down, st.Memory, st.Connections = 0, 0, 0, 0
	s.state.conns = nil
	s.state.routes = nil
	s.state.tracker = connTracker{}
	if s.state.coreBin == "" {
		st.Core = model.CoreMissing
		st.CoreError = coreMissingText(s.state.settings.CorePath, core.ErrNotFound)
	}
	s.mu.Unlock()
	s.em.emit(TopicStatus, TopicConns, TopicExits, TopicProxies)
	s.releaseDNS()
	if err != nil {
		return fmt.Errorf(i18n.T("停止内核失败：%v"), err)
	}
	if proxyErr != nil {
		return fmt.Errorf(i18n.T("内核已停止，但关闭系统代理失败：%v。请在系统设置中手动关闭代理。"), proxyErr)
	}
	return nil
}

// RestartCore restarts the core, keeping the system proxy as is.
func (s *Service) RestartCore() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.restartLocked()
}

// portTakenError explains a mixed port held by another program.
func portTakenError(port int) error {
	return fmt.Errorf(i18n.T("代理端口 %d 已被其他程序占用（可能是另一款代理软件）。请在「设置 › 代理」中换一个端口，或先退出占用它的程序"), port)
}

// ensureStarted starts the core unless it is already running (or starting
// through another operation, which holds opMu). Unlike StartCore it never
// restarts a running core, so the launch-time auto start cannot race with
// e.g. the first subscription import and bounce the core.
func (s *Service) ensureStarted() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.running() {
		return nil
	}
	return s.startLocked()
}

// restartLocked stops (without touching the system proxy) and starts again.
func (s *Service) restartLocked() (err error) {
	defer func() { s.logOp("restart core", err) }()
	s.stopStreams()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	_ = s.d.mgr.Stop(ctx)
	cancel()
	s.mu.Lock()
	s.state.st.Core = model.CoreStopped
	s.mu.Unlock()
	return s.startLocked()
}

// reloadLocked recomposes, validates and hot-reloads the running core.
// When the core is not running nothing happens: the next start composes
// afresh. On failure the previous runtime file stays in place.
func (s *Service) reloadLocked() error {
	if !s.running() {
		return nil
	}
	p, err := s.prepare(composeArgs{})
	if err != nil {
		s.logOp("reload config", err)
		return err
	}
	return s.reloadPrepared(p)
}

// reloadPrepared commits an already validated config and hot-reloads the
// running core with it, so a caller that validated a change before storing
// it (subscription switch, update, edit) does not validate it twice; opMu
// must be held and p must compose what the store now says. A stopped core
// only drops p: the next start composes afresh.
func (s *Service) reloadPrepared(p prepared) (err error) {
	if !s.running() {
		p.discard()
		return nil
	}
	defer func() { s.logOp("reload config", err) }()
	s.mu.Lock()
	prevYAML := s.state.runtimeYAML
	s.mu.Unlock()
	if err := s.commit(p); err != nil {
		return err
	}
	ctx, cancel := s.ctrlCtx(nil, ctrlTimeout*2)
	err = s.d.ctrl.ReloadConfig(ctx, s.runtime)
	cancel()
	if err != nil {
		if rerr := s.rollback(); rerr != nil {
			s.alog.Errorf("reload config: %v", rerr)
		}
		icons, rd := groupIconsOf(prevYAML), parseRuntimeDNS(prevYAML)
		s.mu.Lock()
		s.state.runtimeYAML, s.state.runtimeLines = prevYAML, nil
		s.state.runtimeDNS, s.state.groupIcons = rd, icons
		s.mu.Unlock()
		return fmt.Errorf(i18n.T("内核重新加载配置失败：%v。仍在使用之前的配置。"), err)
	}
	s.mu.Lock()
	set := s.state.settings
	st := &s.state.st
	st.Mode, st.MixedPort, st.AllowLAN = set.Mode, set.MixedPort, set.AllowLAN
	st.TUN = set.TUN && s.state.helperOK && s.state.tunRunner
	s.state.runningTUN = st.TUN
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	s.syncDNS()
	s.afterConfigChange()
	return nil
}

// afterConfigChange refreshes everything the controller reports after a
// start or reload.
func (s *Service) afterConfigChange() {
	s.refreshProxies()
	s.restoreSelections()
	s.pruneExits()
	s.refreshRules()
	s.refreshProviders()
	s.rebuildRoutes()
}

func (s *Service) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.st.Core == model.CoreRunning
}

// ---- crash handling ----

// watchCrashes handles unexpected exits: the system proxy is turned off at
// once (so the user is not left without network), one automatic restart is
// attempted, and the UI is told what happened.
func (s *Service) watchCrashes() {
	ch := s.d.mgr.Crashes()
	if ch == nil {
		return
	}
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case err, ok := <-ch:
			if !ok {
				return
			}
			s.handleCrash(err)
		}
	}
}

func (s *Service) handleCrash(cause error) {
	msg := i18n.T("内核意外退出")
	if cause != nil {
		msg = fmt.Sprintf(i18n.T("内核意外退出：%v"), cause)
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.stopStreams()
	s.mu.Lock()
	if s.state.st.Core != model.CoreRunning && s.state.st.Core != model.CoreStarting {
		s.mu.Unlock()
		return // a stop we requested raced with the exit report
	}
	ours := s.state.weSetProxy
	last := s.state.lastAutoStart
	now := s.d.now()
	s.state.st.Core = model.CoreError
	s.state.st.CoreError = msg
	s.state.st.Up, s.state.st.Down, s.state.st.Connections = 0, 0, 0
	s.state.conns = nil
	s.state.routes = nil
	s.mu.Unlock()
	s.em.emit(TopicStatus, TopicConns, TopicExits)
	s.recordCoreExit(cause)

	if ours {
		if err := s.releaseProxy(); err == nil {
			s.mu.Lock()
			s.state.weSetProxy = false
			s.state.st.SystemProxy = false
			s.mu.Unlock()
			s.em.emit(TopicStatus)
		}
	}
	s.mu.Lock()
	s.state.runningTUN = false
	s.mu.Unlock()
	s.releaseDNS() // the restart below sets it again when TUN comes back

	if s.rootCtx.Err() == nil && (last.IsZero() || now.Sub(last) > autoRestartGap) {
		s.mu.Lock()
		s.state.lastAutoStart = now
		s.mu.Unlock()
		if err := s.startLocked(); err == nil {
			detail := i18n.T("系统代理已关闭，请在需要时重新打开。")
			if ours {
				if err := s.setSystemProxyLocked(true); err == nil {
					detail = i18n.T("系统代理已恢复。")
				}
			}
			s.notify(Notice{Kind: "toast", Title: i18n.T("内核意外退出，已自动重新启动"), Detail: detail, System: true}) // docs/design.md §8.4: system notification
			return
		}
	}
	s.mu.Lock()
	s.state.st.Core = model.CoreError
	s.state.st.CoreError = msg
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	s.notify(Notice{Kind: "core-crash", Title: i18n.T("Mihomo 内核意外退出"), Detail: i18n.Tf("%s。系统代理已关闭。可在「日志」中查看原因，或重新启动内核。", msg), System: true})
}
