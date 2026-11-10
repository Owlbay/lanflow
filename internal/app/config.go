package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// SaveSettings persists s and applies what changed: hot-patchable fields go
// through PATCH /configs, anything else in the composed config triggers a
// recompose + reload (or a restart when the binary or TUN changed).
func (s *Service) SaveSettings(in model.Settings) error {
	in = normalizeSettings(in)
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	old := copySettings(s.state.settings)
	running := s.state.st.Core == model.CoreRunning
	ours := s.state.weSetProxy
	s.mu.Unlock()

	if in.TUN != old.TUN && in.TUN && !s.refreshHelper() {
		return ErrHelperRequired
	}
	if in.TUN != old.TUN && in.TUN {
		if err := s.checkOtherTUN(); err != nil {
			return err
		}
	}

	// Mihomo would keep running without its mixed port (it only logs), so
	// refuse a port another program already listens on.
	if in.MixedPort != old.MixedPort && s.d.portInUse(in.MixedPort, in.AllowLAN) {
		return fmt.Errorf(i18n.T("端口 %d 已被其他程序占用（可能是另一款代理软件），已保留原端口 %d。"), in.MixedPort, old.MixedPort)
	}
	for _, p := range []struct{ was, now int }{{old.Advanced.HTTPPort, in.Advanced.HTTPPort}, {old.Advanced.SocksPort, in.Advanced.SocksPort}} {
		if p.now != 0 && p.now != p.was && s.d.portInUse(p.now, in.AllowLAN) {
			return fmt.Errorf(i18n.T("端口 %d 已被其他程序占用，请换一个端口。"), p.now)
		}
	}

	// persist first so a failed apply still keeps the user's choice
	s.mu.Lock()
	s.state.settings = copySettings(in)
	i18n.SetLang(in.Language)
	if !running {
		s.state.st.Mode, s.state.st.MixedPort, s.state.st.AllowLAN = in.Mode, in.MixedPort, in.AllowLAN
	}
	s.state.proxies = buildSnapshot(s.state.rawProxy, s.state.testing, in.MainGroup)
	s.mu.Unlock()
	s.em.emit(TopicSettings, TopicProxies)
	if err := s.persistSettings(); err != nil {
		s.alog.Errorf("save settings: %v", err)
		return err
	}
	if ch := changedSettings(old, in); len(ch) > 0 {
		s.alog.Infof("settings changed: %s", strings.Join(ch, ", "))
	}

	if in.CorePath != old.CorePath {
		s.mu.Lock()
		s.resolveBinaryLocked()
		s.mu.Unlock()
		s.em.emit(TopicStatus)
	}
	if in.LogLevel != old.LogLevel && running {
		s.restartLogStream(in.LogLevel)
	}
	coexistChanged := (in.ATrustCompat != old.ATrustCompat && s.detectCoexist()) || s.coexistSettingsChanged(old, in)
	if !running {
		return nil
	}
	if (in.TUNSetDNS != old.TUNSetDNS || coexistChanged) && in.TUN == old.TUN {
		s.syncDNS()
	}

	// revert restores the previous settings when applying them failed, so
	// the UI shows what the core actually runs.
	revert := func(err error) error {
		s.mu.Lock()
		s.state.settings = copySettings(old)
		s.state.proxies = buildSnapshot(s.state.rawProxy, s.state.testing, old.MainGroup)
		s.mu.Unlock()
		i18n.SetLang(old.Language)
		_ = s.persistSettings()
		s.em.emit(TopicSettings, TopicProxies)
		return err
	}

	// A stopped core picks the new settings up at its next start.
	needRestart := (in.CorePath != old.CorePath || in.TUN != old.TUN) && s.running()
	needReload := needsReload(old, in) || coexistChanged || advancedNeedsReload(old.Advanced, in.Advanced)
	switch {
	case needRestart:
		if err := s.restartLocked(); err != nil {
			return revert(err)
		}
	case needReload:
		if err := s.reloadLocked(); err != nil {
			return revert(err)
		}
	default:
		patch := controller.ConfigPatch{}
		changed := false
		if in.Mode != old.Mode {
			m := string(in.Mode)
			patch.Mode, changed = &m, true
		}
		if in.LogLevel != old.LogLevel {
			l := in.LogLevel
			patch.LogLevel, changed = &l, true
		}
		if in.AllowLAN != old.AllowLAN {
			a := in.AllowLAN
			patch.AllowLan, changed = &a, true
		}
		if in.MixedPort != old.MixedPort {
			p := in.MixedPort
			patch.MixedPort, changed = &p, true
		}
		if in.Sniffer.Enable != old.Sniffer.Enable {
			e := in.Sniffer.Enable
			patch.Sniffing, changed = &e, true
		}
		if ap, ok := advancedPatch(old.Advanced, in.Advanced); ok {
			patch.Port, patch.SocksPort, patch.TCPConcurrent, patch.InterfaceName = ap.Port, ap.SocksPort, ap.TCPConcurrent, ap.InterfaceName
			changed = true
		}
		if changed {
			ctx, cancel := context.WithTimeout(context.Background(), ctrlTimeout)
			err := s.d.ctrl.PatchConfigs(ctx, patch)
			cancel()
			if err != nil {
				if patch.MixedPort != nil {
					// most likely the port is taken
					return revert(fmt.Errorf(i18n.T("端口 %d 无法使用：%v。已保留原端口 %d。"), in.MixedPort, err, old.MixedPort))
				}
				return revert(fmt.Errorf(i18n.T("应用设置失败：%v。已恢复原设置。"), err))
			}
		}
		s.mu.Lock()
		s.state.st.Mode, s.state.st.MixedPort, s.state.st.AllowLAN = in.Mode, in.MixedPort, in.AllowLAN
		s.mu.Unlock()
		s.em.emit(TopicStatus)
	}

	// the system proxy follows the port, the bypass list and the PAC mode
	switch {
	case ours && pacChanged(old.Advanced, in.Advanced):
		// Global proxy ↔ PAC: let go of the old form first so the saved
		// state is restored, then set the new one.
		if err := s.releaseProxy(); err != nil {
			return fmt.Errorf(i18n.T("设置已保存，但更新系统代理失败：%v"), err)
		}
		if err := s.applySystemProxy(); err != nil {
			return fmt.Errorf(i18n.T("设置已保存，但更新系统代理失败：%v"), err)
		}
	case ours && (in.MixedPort != old.MixedPort || !reflect.DeepEqual(in.Bypass, old.Bypass) || coexistChanged || in.Advanced.PACDirectCN != old.Advanced.PACDirectCN):
		if err := s.applySystemProxy(); err != nil {
			return fmt.Errorf(i18n.T("设置已保存，但更新系统代理失败：%v"), err)
		}
	}
	if in.Mode != old.Mode {
		s.rebuildRoutes()
	}
	return nil
}

// needsReload reports whether a settings change touches the composed config
// in a way PATCH /configs cannot apply.
func needsReload(old, in model.Settings) bool {
	oldS, inS := old.Sniffer, in.Sniffer
	oldS.Enable, inS.Enable = false, false
	return !reflect.DeepEqual(old.DNS, in.DNS) ||
		!reflect.DeepEqual(oldS, inS) ||
		!reflect.DeepEqual(old.Geo.GeoIP, in.Geo.GeoIP) ||
		old.Geo.GeoSite != in.Geo.GeoSite || old.Geo.MMDB != in.Geo.MMDB || old.Geo.ASN != in.Geo.ASN ||
		old.Geo.Mode != in.Geo.Mode || old.Geo.AutoUpdate != in.Geo.AutoUpdate || old.Geo.IntervalHours != in.Geo.IntervalHours ||
		old.TUNStack != in.TUNStack
}

// ApplyDNS saves the DNS section and reloads.
func (s *Service) ApplyDNS(d model.DNSSettings) error {
	set := s.Settings()
	set.DNS = d
	return s.SaveSettings(set)
}

// ApplySniffer saves the sniffer section and reloads (or patches the switch).
func (s *Service) ApplySniffer(sn model.SnifferSettings) error {
	set := s.Settings()
	set.Sniffer = sn
	return s.SaveSettings(set)
}

// overrideArgs builds composeArgs that substitute the text of the override
// entry key: an override ID, or the legacy keys "global" (a global entry)
// and a subscription's ID (an entry that subscription selects), created on
// demand (profile.Store.SetOverride).
func (s *Service) overrideArgs(key, text string) (composeArgs, error) {
	o, ok := s.d.store.GetOverride(key)
	switch {
	case ok:
	case key == "global":
		o = model.Override{ID: key, Name: i18n.T("全局覆写"), Enabled: true, Global: true, Source: model.OverrideLocal}
	default:
		sub, found := s.d.store.Get(key)
		if !found {
			return composeArgs{}, errors.New(i18n.T("覆写不存在，可能已被删除。"))
		}
		o = model.Override{ID: key, Name: sub.Name + i18n.T(" 覆写"), Enabled: true, Source: model.OverrideLocal}
	}
	o.Text = text
	args := composeArgs{ov: &o}
	if sub, found := s.d.store.Get(key); found {
		// The legacy per-subscription entry: compose against that subscription,
		// selected as SetOverride will select it.
		args.subID = sub.ID
		if !hasID(sub.Overrides, key) {
			sel := append(append([]string(nil), sub.Overrides...), key)
			args.subOverrides = &sel
		}
	}
	return args, nil
}

// SaveOverride validates the override by composing and running mihomo -t,
// stores it, and reloads when it affects the running config.
func (s *Service) SaveOverride(key, text string) (err error) {
	defer func() { s.logOp("save override "+key, err) }()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if text == "" {
		if _, ok := s.d.store.GetOverride(key); !ok {
			return nil
		}
		return s.deleteOverrideLocked(key)
	}
	args, err := s.overrideArgs(key, text)
	if err != nil {
		return err
	}
	if err := s.validateOverrideLocked(*args.ov, args); err != nil {
		return err
	}
	if err := s.d.store.SetOverride(key, text); err != nil {
		return fmt.Errorf(i18n.T("保存覆写失败：%v"), err)
	}
	return s.applyOverrideChangeLocked(*args.ov)
}

// PreviewOverride diffs the current runtime config against the config
// composed with the new override text.
func (s *Service) PreviewOverride(key, text string) ([]model.DiffLine, error) {
	args, err := s.overrideArgs(key, text)
	if err != nil {
		return nil, err
	}
	in, _, err := s.buildInput(args)
	if err != nil {
		return nil, err
	}
	out, err := s.d.prof.Compose(in)
	if err != nil {
		return nil, err
	}
	cur, err := s.currentYAML(args.subID)
	if err != nil {
		return nil, err
	}
	return s.d.prof.Diff(cur, out.YAML), nil
}

// currentYAML returns the runtime YAML (the last composed one when the
// subscription matches, else a fresh composition with the stored overrides).
func (s *Service) currentYAML(subID string) ([]byte, error) {
	s.mu.Lock()
	y := s.state.runtimeYAML
	curID := s.state.st.CurrentSubscription
	s.mu.Unlock()
	if y != nil && (subID == "" || subID == curID) {
		return y, nil
	}
	in, _, err := s.buildInput(composeArgs{subID: subID})
	if err != nil {
		return nil, err
	}
	out, err := s.d.prof.Compose(in)
	if err != nil {
		return nil, err
	}
	return out.YAML, nil
}

// RuntimeConfig returns the last composed runtime config, annotated by layer.
func (s *Service) RuntimeConfig() ([]model.RuntimeLine, error) {
	s.mu.Lock()
	lines := s.state.runtimeLines
	s.mu.Unlock()
	if lines != nil {
		return append([]model.RuntimeLine(nil), lines...), nil
	}
	in, _, err := s.buildInput(composeArgs{})
	if err != nil {
		return nil, err
	}
	out, err := s.d.prof.Compose(in)
	if err != nil {
		return nil, err
	}
	return out.Lines, nil
}

// UpdateGeoData asks the running core to download the geo databases.
func (s *Service) UpdateGeoData() error {
	if !s.running() {
		return errors.New(i18n.T("请先启动内核，再更新地理数据库。"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := s.d.ctrl.UpdateGeo(ctx); err != nil {
		return fmt.Errorf(i18n.T("更新地理数据库失败：%v"), err)
	}
	err := s.updateSettings(func(set *model.Settings) {
		set.Geo.UpdatedAt = s.d.now()
		s.state.geoInfoAt = time.Time{} // GeoDataInfo re-reads the files
	})
	s.em.emit(TopicSettings)
	return err
}

// changedSettings names the top-level settings that differ (the
// remembered UI state is left out: it changes on every click).
func changedSettings(old, in model.Settings) []string {
	var out []string
	a, b := reflect.ValueOf(old), reflect.ValueOf(in)
	for i := 0; i < a.NumField(); i++ {
		name := a.Type().Field(i).Name
		if name == "UI" || name == "ConnHiddenColumns" {
			continue
		}
		if !reflect.DeepEqual(a.Field(i).Interface(), b.Field(i).Interface()) {
			out = append(out, name)
		}
	}
	return out
}
