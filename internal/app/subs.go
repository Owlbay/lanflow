package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

const (
	fetchTimeout      = 60 * time.Second
	autoUpdateTick    = 10 * time.Minute
	fileSubscriptionK = "file"
	urlSubscriptionK  = "url"
	// defaultSubTimeout is a subscription's download timeout when it sets
	// none (Subscription.TimeoutSec == 0).
	defaultSubTimeout = 30 * time.Second
	maxSubTimeout     = 600 * time.Second
)

// subTimeout is the download timeout of a subscription.
func subTimeout(sub model.Subscription) time.Duration {
	if sub.TimeoutSec <= 0 {
		return defaultSubTimeout
	}
	return time.Duration(sub.TimeoutSec) * time.Second
}

// fetchClient returns the HTTP client for a subscription download: through
// the running core when asked and possible, else direct. timeout 0 means
// the general fetchTimeout.
func (s *Service) fetchClient(viaProxy bool, timeout time.Duration) *http.Client {
	s.mu.Lock()
	running := s.state.st.Core == model.CoreRunning
	port := s.state.settings.MixedPort
	s.mu.Unlock()
	if timeout <= 0 {
		timeout = fetchTimeout
	}
	if viaProxy && running {
		return s.d.httpVia(port, timeout)
	}
	return s.d.httpDirect(timeout)
}

// setUpdating flags a subscription as updating for the UI.
func (s *Service) setUpdating(id string, on bool) {
	s.mu.Lock()
	if on {
		s.state.subsUpdating[id] = true
	} else {
		delete(s.state.subsUpdating, id)
	}
	s.mu.Unlock()
	s.em.emit(TopicSubs)
}

// autoUpdateChoices are the auto-update intervals the UI offers besides
// 关闭 (docs/design.md:345, §6.2).
var autoUpdateChoices = []int{6, 12, 24}

// snapAutoUpdate maps an interval in hours (a server's
// profile-update-interval, or any stored value) to the nearest offered
// choice: ≤ 0 stays off, anything else becomes 6, 12 or 24 (ties round down).
func snapAutoUpdate(hours int) int {
	if hours <= 0 {
		return 0
	}
	best := autoUpdateChoices[0]
	for _, c := range autoUpdateChoices[1:] {
		if abs(c-hours) < abs(best-hours) {
			best = c
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// applyFetched copies the download metadata into the subscription record.
func applyFetched(sub *model.Subscription, f profile.Fetched, now time.Time) {
	sub.Upload, sub.Download, sub.Total, sub.Expire = f.Upload, f.Download, f.Total, f.Expire
	sub.UpdatedAt = now
	sub.LastError = ""
	if f.HomePage != "" {
		sub.HomePage = f.HomePage
	}
}

// ErrInvalidConfig marks an update whose new content failed the core's
// validation: the stored config is unchanged and an "invalid-config" notice
// (the §6.3 dialog) has been raised, so the UI need not toast the error.
var ErrInvalidConfig = i18n.Error("配置未通过校验")

// setImportPhase publishes where a running import is (Status.ImportPhase),
// so the import dialog can show 下载 → 校验 → 完成.
func (s *Service) setImportPhase(phase string) {
	s.mu.Lock()
	s.state.st.ImportPhase = phase
	s.mu.Unlock()
	s.em.emit(TopicStatus)
}

// ImportSubscriptionURL downloads a subscription and stores it; use switches to it.
func (s *Service) ImportSubscriptionURL(rawURL, name, userAgent string, viaProxy, use bool) (sub model.Subscription, err error) {
	defer func() { s.logOp(fmt.Sprintf("import subscription %s (via proxy %v)", rawURL, viaProxy), err) }()
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return model.Subscription{}, errors.New(i18n.T("订阅链接无效。请输入以 http:// 或 https:// 开头的完整链接。"))
	}
	s.setImportPhase(model.ImportDownloading)
	defer s.setImportPhase("")
	ctx, cancel := s.ctrlCtx(nil, defaultSubTimeout)
	defer cancel()
	f, err := s.d.prof.Fetch(ctx, s.fetchClient(viaProxy, defaultSubTimeout), rawURL, userAgent)
	if err != nil {
		return model.Subscription{}, fmt.Errorf(i18n.T("下载订阅失败：%v"), err)
	}
	s.setImportPhase(model.ImportValidating)
	if err := s.d.prof.CheckYAML(f.Data); err != nil {
		return model.Subscription{}, fmt.Errorf(i18n.T("下载的内容不是 Clash/Mihomo 格式的配置：%v"), err)
	}
	if name = strings.TrimSpace(name); name == "" {
		name = f.Name
	}
	if name == "" {
		name = u.Hostname()
	}
	sub = model.Subscription{Name: name, Source: urlSubscriptionK, URL: rawURL, UserAgent: userAgent, ViaProxy: viaProxy, AutoUpdateHours: snapAutoUpdate(f.UpdateInterval)}
	applyFetched(&sub, f, s.d.now())
	sub, err = s.d.store.Put(sub)
	if err != nil {
		return model.Subscription{}, fmt.Errorf(i18n.T("保存订阅失败：%v"), err)
	}
	if err := s.d.store.SetContent(sub.ID, f.Data); err != nil {
		_ = s.d.store.Delete(sub.ID)
		return model.Subscription{}, fmt.Errorf(i18n.T("保存订阅内容失败：%v"), err)
	}
	return s.finishImport(sub, use)
}

// ImportSubscriptionFile imports a local YAML file.
func (s *Service) ImportSubscriptionFile(path string, use bool) (sub model.Subscription, err error) {
	defer func() { s.logOp("import subscription file "+filepath.Base(path), err) }()
	data, err := os.ReadFile(path)
	if err != nil {
		return model.Subscription{}, fmt.Errorf(i18n.T("读取文件失败：%v"), err)
	}
	s.setImportPhase(model.ImportValidating)
	defer s.setImportPhase("")
	if err := s.d.prof.CheckYAML(data); err != nil {
		return model.Subscription{}, fmt.Errorf(i18n.T("「%s」不是 Clash/Mihomo 格式的配置：%v"), filepath.Base(path), err)
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	sub = model.Subscription{Name: name, Source: fileSubscriptionK, FilePath: path, UpdatedAt: s.d.now()}
	sub, err = s.d.store.Put(sub)
	if err != nil {
		return model.Subscription{}, fmt.Errorf(i18n.T("保存订阅失败：%v"), err)
	}
	if err := s.d.store.SetContent(sub.ID, data); err != nil {
		_ = s.d.store.Delete(sub.ID)
		return model.Subscription{}, fmt.Errorf(i18n.T("保存订阅内容失败：%v"), err)
	}
	return s.finishImport(sub, use)
}

// finishImport refreshes the caches and switches to the new subscription
// when asked. The first subscription is always used: without one the core
// cannot start. A subscription imported without switching is still checked
// with the core (mihomo -t) and dropped when it fails, so the dialog's 校验
// step means the same either way.
func (s *Service) finishImport(sub model.Subscription, use bool) (model.Subscription, error) {
	s.refreshStore(TopicSubs)
	if _, hasCurrent := s.d.store.Current(); !hasCurrent {
		use = true
	}
	if use {
		if err := s.UseSubscription(sub.ID); err != nil {
			return sub, err
		}
	} else if err := s.validateStored(sub.ID); err != nil {
		_ = s.d.store.Delete(sub.ID)
		s.refreshStore(TopicSubs)
		return model.Subscription{}, fmt.Errorf(i18n.T("「%s」的配置未通过校验：%v"), sub.Name, err)
	}
	got, _ := s.d.store.Get(sub.ID)
	return got, nil
}

// validateStored composes and validates a stored subscription without
// touching the runtime config; it passes when no core binary is available.
func (s *Service) validateStored(id string) error {
	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()
	if bin == "" {
		return nil
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	p, err := s.prepare(composeArgs{subID: id})
	if err != nil {
		return err
	}
	p.discard()
	return nil
}

// UseSubscription validates the composed config with the subscription and
// switches to it; the core is reloaded (or started when this is the first
// subscription). On failure the current subscription stays.
func (s *Service) UseSubscription(id string) (err error) {
	label := s.subLabel(id)
	defer func() { s.logOp("use subscription "+label, err) }()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	sub, ok := s.d.store.Get(id)
	if !ok {
		return errors.New(i18n.T("订阅不存在，可能已被删除。"))
	}
	prev, hadPrev := s.d.store.Current()
	if hadPrev && prev.ID == id {
		return nil
	}
	s.mu.Lock()
	bin := s.state.coreBin
	running := s.state.st.Core == model.CoreRunning
	s.mu.Unlock()
	var p prepared
	if bin != "" {
		if p, err = s.prepare(composeArgs{subID: id}); err != nil {
			detail := err.Error()
			msg := fmt.Sprintf(i18n.T("「%s」的配置未通过校验"), sub.Name)
			if hadPrev {
				msg += fmt.Sprintf(i18n.T("，已继续使用「%s」"), prev.Name)
			}
			s.notify(Notice{Kind: "invalid-config", Title: i18n.T("配置无法使用"), Detail: msg + i18n.T("。") + "\n" + detail, Subject: id})
			return fmt.Errorf(i18n.T("%s：%s"), msg, detail)
		}
	}
	if err := s.d.store.SetCurrent(id); err != nil {
		p.discard()
		return fmt.Errorf(i18n.T("切换订阅失败：%v"), err)
	}
	s.refreshStore(TopicSubs)
	if !running {
		p.discard() // a stopped core composes afresh when it starts
		if !hadPrev {
			// first subscription: start the core; the system proxy stays off
			return s.startLocked()
		}
		return nil
	}
	if p.tmp != "" {
		// p composes exactly the now-current subscription: reload it
		// without composing and validating a second time.
		err = s.reloadPrepared(p)
	} else {
		err = s.reloadLocked()
	}
	if err != nil {
		if hadPrev {
			_ = s.d.store.SetCurrent(prev.ID)
			s.refreshStore(TopicSubs)
		}
		s.notify(Notice{Kind: "invalid-config", Title: i18n.T("配置无法使用"), Detail: err.Error(), Subject: id})
		return err
	}
	return nil
}

// UpdateSubscription re-downloads one subscription.
func (s *Service) UpdateSubscription(id string) (err error) {
	label := s.subLabel(id)
	defer func() { s.logOp("update subscription "+label, err) }()
	sub, ok := s.d.store.Get(id)
	if !ok {
		return errors.New(i18n.T("订阅不存在，可能已被删除。"))
	}
	s.setUpdating(id, true)
	defer s.setUpdating(id, false)
	err = s.updateOne(sub)
	s.refreshStore(TopicSubs)
	return err
}

// updateOne downloads, validates and stores a subscription's new content,
// reloading the core when it is the current one. The record's LastError
// explains failures; the stored content is replaced only when the new one
// passes validation.
func (s *Service) updateOne(sub model.Subscription) error {
	fail := func(err error) error {
		sub.LastError = err.Error()
		_, _ = s.d.store.Put(sub)
		return err
	}
	var data []byte
	switch sub.Source {
	case urlSubscriptionK:
		timeout := subTimeout(sub)
		ctx, cancel := s.ctrlCtx(nil, timeout)
		f, err := s.d.prof.Fetch(ctx, s.fetchClient(sub.ViaProxy, timeout), sub.URL, sub.UserAgent)
		cancel()
		if err != nil {
			hint := i18n.T("可稍后重试")
			if !sub.ViaProxy {
				hint += i18n.T("，或在编辑中开启“通过代理更新”")
			}
			return fail(fmt.Errorf(i18n.T("订阅更新失败：%v。仍在使用上次的配置。%s。"), err, hint))
		}
		data = f.Data
		applyFetched(&sub, f, s.d.now())
		if sub.AutoUpdateHours == 0 && f.UpdateInterval > 0 {
			sub.AutoUpdateHours = snapAutoUpdate(f.UpdateInterval)
		}
	case fileSubscriptionK:
		d, err := os.ReadFile(sub.FilePath)
		if err != nil {
			return fail(fmt.Errorf(i18n.T("重新读取文件失败：%v。仍在使用上次的配置。"), err))
		}
		if err := s.d.prof.CheckYAML(d); err != nil {
			return fail(fmt.Errorf(i18n.T("文件不是 Clash/Mihomo 格式的配置：%v。仍在使用上次的配置。"), err))
		}
		data = d
		sub.UpdatedAt = s.d.now()
		sub.LastError = ""
	default:
		return fail(errors.New(i18n.T("未知的订阅来源")))
	}

	// opMu covers validate → store → reload, so the validated config is
	// the one reloaded (no switch or edit can slip in between) and is
	// composed and checked once.
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()
	var p prepared
	if bin != "" {
		var err error
		if p, err = s.prepare(composeArgs{subID: sub.ID, content: data}); err != nil {
			// §6.3: the dialog, not a toast; the stored content is unchanged.
			msg := fmt.Sprintf(i18n.T("「%s」更新后的配置未通过校验，仍在使用上次的配置"), sub.Name)
			s.notify(Notice{Kind: "invalid-config", Title: i18n.T("配置无法使用"), Detail: msg + i18n.T("。") + "\n" + err.Error(), Subject: sub.ID})
			return fail(fmt.Errorf(i18n.T("「%s」更新后的%w：%v。仍在使用上次的配置。"), sub.Name, ErrInvalidConfig, err))
		}
	}
	if err := s.d.store.SetContent(sub.ID, data); err != nil {
		p.discard()
		return fail(fmt.Errorf(i18n.T("保存订阅内容失败：%v"), err))
	}
	if _, err := s.d.store.Put(sub); err != nil {
		p.discard()
		return fmt.Errorf(i18n.T("保存订阅失败：%v"), err)
	}
	cur, ok := s.d.store.Current()
	if !ok || cur.ID != sub.ID {
		p.discard()
		return nil
	}
	var err error
	if p.tmp != "" {
		err = s.reloadPrepared(p)
	} else {
		err = s.reloadLocked()
	}
	if err != nil {
		sub.LastError = err.Error()
		_, _ = s.d.store.Put(sub)
		return err
	}
	return nil
}

// UpdateAllSubscriptions updates every URL subscription in turn.
func (s *Service) UpdateAllSubscriptions() error {
	var failed []string
	for _, sub := range s.d.store.List() {
		if sub.Source != urlSubscriptionK {
			continue
		}
		if err := s.UpdateSubscription(sub.ID); err != nil {
			failed = append(failed, sub.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf(i18n.T("以下订阅更新失败：%s。详情见订阅列表。"), strings.Join(failed, i18n.T("、")))
	}
	return nil
}

// autoUpdateLoop checks every 10 minutes which subscriptions are due.
func (s *Service) autoUpdateLoop() {
	t := time.NewTicker(autoUpdateTick)
	defer t.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-t.C:
			s.runAutoUpdates()
		}
	}
}

// autoUpdateFailure remembers failed automatic updates of a subscription,
// whose UpdatedAt does not move on failure: without it a failing update
// would be retried (and notified) on every 10-minute tick.
type autoUpdateFailure struct {
	lastAttempt time.Time
	failures    int    // consecutive failures
	notified    string // the error last raised as a system notification
}

// autoRetryMin is the first wait after a failed automatic update; it
// doubles with each further failure, up to the subscription's own interval.
const autoRetryMin = time.Hour

// retryAfter is the wait after n consecutive failures of a subscription
// updating every interval.
func retryAfter(n int, interval time.Duration) time.Duration {
	d := autoRetryMin
	for i := 1; i < n && d < interval; i++ {
		d *= 2
	}
	if d > interval && interval >= autoRetryMin {
		d = interval
	}
	return d
}

// runAutoUpdates updates the subscriptions whose interval has elapsed and
// reports failures as notices: once per distinct error, retrying with an
// exponential backoff (at least an hour) rather than on every tick.
func (s *Service) runAutoUpdates() {
	now := s.d.now()
	for _, sub := range s.d.store.List() {
		if sub.Source != urlSubscriptionK || sub.AutoUpdateHours <= 0 {
			continue
		}
		interval := time.Duration(sub.AutoUpdateHours) * time.Hour
		if now.Sub(sub.UpdatedAt) < interval {
			continue
		}
		s.mu.Lock()
		f, failed := s.state.autoFail[sub.ID]
		s.mu.Unlock()
		if failed && now.Sub(f.lastAttempt) < retryAfter(f.failures, interval) {
			continue
		}
		if s.rootCtx.Err() != nil {
			return
		}
		s.alog.Infof("subscription %q due for its %dh auto update", sub.Name, sub.AutoUpdateHours)
		err := s.UpdateSubscription(sub.ID)
		s.mu.Lock()
		if err == nil {
			delete(s.state.autoFail, sub.ID)
			s.mu.Unlock()
			continue
		}
		if s.state.autoFail == nil {
			s.state.autoFail = map[string]autoUpdateFailure{}
		}
		f = s.state.autoFail[sub.ID]
		f.lastAttempt = now
		f.failures++
		repeat := f.notified == err.Error()
		f.notified = err.Error()
		s.state.autoFail[sub.ID] = f
		s.mu.Unlock()
		if repeat {
			s.alog.Warnf("subscription %q auto update failed again (%d in a row): %v", sub.Name, f.failures, err)
			continue
		}
		s.notify(Notice{Kind: "error", Title: fmt.Sprintf(i18n.T("订阅「%s」自动更新失败"), sub.Name), Detail: err.Error(), Subject: sub.ID, System: true})
	}
}

// EditSubscription updates name, URL, auto update, User-Agent, via-proxy,
// the download timeout and the override selection. A changed selection of
// the current subscription is validated with the core first and reloads it.
func (s *Service) EditSubscription(in model.Subscription) error {
	sub, ok := s.d.store.Get(in.ID)
	if !ok {
		return errors.New(i18n.T("订阅不存在，可能已被删除。"))
	}
	if in.TimeoutSec < 0 || time.Duration(in.TimeoutSec)*time.Second > maxSubTimeout {
		return fmt.Errorf(i18n.T("下载超时须为 1–%d 秒之间的数字。"), int(maxSubTimeout/time.Second))
	}
	if name := strings.TrimSpace(in.Name); name != "" {
		sub.Name = name
	}
	if sub.Source == urlSubscriptionK {
		u := strings.TrimSpace(in.URL)
		if u == "" {
			return errors.New(i18n.T("订阅链接不能为空。"))
		}
		if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return errors.New(i18n.T("订阅链接无效。请输入以 http:// 或 https:// 开头的完整链接。"))
		}
		sub.URL = u
	}
	sub.AutoUpdateHours = snapAutoUpdate(in.AutoUpdateHours)
	sub.UserAgent = in.UserAgent
	sub.ViaProxy = in.ViaProxy
	sub.TimeoutSec = in.TimeoutSec
	selection := cleanSelection(in.Overrides, s.d.store.Overrides())
	selectionChanged := !equalStrings(selection, sub.Overrides)
	sub.Overrides = selection
	if !selectionChanged {
		if _, err := s.d.store.Put(sub); err != nil {
			return fmt.Errorf(i18n.T("保存订阅失败：%v"), err)
		}
		s.refreshStore(TopicSubs)
		return nil
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()
	cur, hasCur := s.d.store.Current()
	affects := hasCur && cur.ID == sub.ID
	var p prepared
	if bin != "" {
		var err error
		if p, err = s.prepare(composeArgs{subID: sub.ID, subOverrides: &selection}); err != nil {
			return fmt.Errorf(i18n.T("覆写未保存，%v"), err)
		}
	}
	if _, err := s.d.store.Put(sub); err != nil {
		p.discard()
		return fmt.Errorf(i18n.T("保存订阅失败：%v"), err)
	}
	s.refreshStore(TopicSubs, TopicSettings)
	switch {
	case affects && p.tmp != "":
		return s.reloadPrepared(p) // p composes the stored selection
	case affects:
		return s.reloadLocked()
	}
	p.discard()
	return nil
}

// cleanSelection keeps the IDs of list in ids, without duplicates, in the
// list's order (the order overrides apply in).
func cleanSelection(ids []string, list []model.Override) []string {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []string
	for _, o := range list {
		if want[o.ID] {
			out = append(out, o.ID)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// DeleteSubscription removes a subscription; the current one is refused.
func (s *Service) DeleteSubscription(id string) (err error) {
	label := s.subLabel(id)
	defer func() { s.logOp("delete subscription "+label, err) }()
	sub, ok := s.d.store.Get(id)
	if !ok {
		return nil
	}
	if cur, ok := s.d.store.Current(); ok && cur.ID == id {
		return fmt.Errorf(i18n.T("「%s」正在使用中，无法删除。请先切换到其他订阅。"), sub.Name)
	}
	if err := s.d.store.Delete(id); err != nil {
		return fmt.Errorf(i18n.T("删除订阅失败：%v"), err)
	}
	s.refreshStore(TopicSubs, TopicSettings)
	return nil
}

// SubscriptionRaw returns the stored YAML of a subscription.
func (s *Service) SubscriptionRaw(id string) (string, error) {
	data, err := s.d.store.Content(id)
	if err != nil {
		return "", fmt.Errorf(i18n.T("读取订阅内容失败：%v"), err)
	}
	return string(data), nil
}
