package app

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// Loading lanflow.yaml into the service (configuration sync and
// API.ApplyConfigFile). The document is declarative: for the parts it
// covers — settings without the machine-local items, the override list,
// the custom rules and the URL subscriptions — this machine ends up with
// exactly what the file says. Subscriptions imported from local files
// stay, and so does the current subscription (never deleted from under
// the running core).
//
// Everything is checked before anything is stored: the file's own rules
// (syncfile.Decode), the custom rules, the ports, and the composed
// config of the current subscription with `mihomo -t`. Then the store is
// written and the settings go through SaveSettings, which applies them
// hot, reloads or restarts the core as each change needs.

// docError is a problem with one part of the document; path locates it
// for the line number (syncfile.LineOf).
type docError struct {
	path string
	err  error
}

func (e *docError) Error() string { return e.path + i18n.T("：") + e.err.Error() }
func (e *docError) Unwrap() error { return e.err }

// applyPlan is what loading a document changes.
type applyPlan struct {
	cur, next localState
	put       []model.Subscription // subscriptions to add or update
	del       []model.Subscription // URL subscriptions the file no longer has
	keptCur   string               // the current subscription, which the file drops but stays
	missing   []string             // names of subscriptions without a link on this machine
	fresh     []string             // IDs of new subscriptions to download
	changed   []string             // "settings", "overrides", "customRules", "subscriptions"
	before    []byte               // lanflow.yaml of this machine now
	after     []byte               // lanflow.yaml of this machine after loading
}

// planDoc works out what loading doc does, without changing anything.
func (s *Service) planDoc(doc syncfile.Doc) (*applyPlan, error) {
	cur := s.snapshotLocal()
	p := &applyPlan{cur: cur}
	next := localState{set: normalizeSettings(syncfile.KeepLocal(doc.Settings, cur.set))}

	// ---- subscriptions ----
	byID := map[string]model.Subscription{}
	for _, sub := range cur.subs {
		byID[sub.ID] = sub
	}
	inDoc := map[string]bool{}
	curID := ""
	if c, ok := s.d.store.Current(); ok {
		curID = c.ID
	}
	for i, e := range doc.Subscriptions {
		inDoc[e.ID] = true
		old, had := byID[e.ID]
		if had && old.Source != urlSubscriptionK {
			return nil, &docError{"subscriptions[" + strconv.Itoa(i) + "].id", errors.New(i18n.T("与本机一个从文件导入的订阅 ID 相同"))}
		}
		sub := old
		if !had {
			sub = model.Subscription{ID: e.ID, Source: urlSubscriptionK}
		}
		sub.Name = strings.TrimSpace(e.Name)
		sub.URL = s.resolveLink(e.URL, old.URL)
		sub.UserAgent, sub.ViaProxy, sub.TimeoutSec = e.UserAgent, e.ViaProxy, e.TimeoutSec
		sub.AutoUpdateHours = snapAutoUpdate(e.AutoUpdateHours)
		sub.Overrides = append([]string(nil), e.Overrides...)
		if sub.URL == "" {
			p.missing = append(p.missing, sub.Name)
		}
		if !had || !subEqual(old, sub) {
			p.put = append(p.put, sub)
		}
		if (!had || old.URL == "") && sub.URL != "" {
			// new, or its link just arrived (entered, or unsealed): download
			p.fresh = append(p.fresh, sub.ID)
		}
		next.subs = append(next.subs, sub)
	}
	for _, sub := range cur.subs {
		switch {
		case inDoc[sub.ID]:
		case sub.Source != urlSubscriptionK:
			next.subs = append(next.subs, sub) // local file subscriptions stay
		case sub.ID == curID:
			p.keptCur = sub.Name
			next.subs = append(next.subs, sub)
		default:
			p.del = append(p.del, sub)
		}
	}

	// ---- overrides: the file's list, then the entries of this machine's
	// own subscriptions that never travel (buildDoc) ----
	stays := map[string]bool{}
	for _, sub := range next.subs {
		if !inDoc[sub.ID] {
			stays[sub.ID] = true
		}
	}
	for _, o := range doc.Overrides {
		next.ovs = append(next.ovs, overrideFromDoc(o))
	}
	for _, o := range cur.ovs {
		if stays[o.ID] {
			next.ovs = append(next.ovs, o)
		}
	}
	// keep the download time of URL entries whose text did not change
	for i, o := range next.ovs {
		for _, old := range cur.ovs {
			if old.ID == o.ID && old.Text == o.Text {
				next.ovs[i].UpdatedAt = old.UpdatedAt
			}
		}
	}

	// ---- custom rules ----
	for i, r := range doc.CustomRules {
		cr, err := validateCustomRule(model.CustomRule{Type: r.Type, Payload: r.Payload, Target: r.Target})
		if err != nil {
			return nil, &docError{"customRules[" + strconv.Itoa(i) + "]", err}
		}
		next.rules = append(next.rules, cr)
	}
	// keep local IDs of rules that did not change, so nothing churns
	for i := range next.rules {
		if i < len(cur.rules) && ruleKey(cur.rules[i]) == ruleKey(next.rules[i]) {
			next.rules[i].ID = cur.rules[i].ID
		}
	}

	p.next = next
	p.before = syncfile.Encode(buildDoc(cur, linksKeychain))
	p.after = syncfile.Encode(buildDoc(next, linksKeychain))
	section := func(st localState, part string) []byte {
		d := buildDoc(st, linksKeychain)
		switch part {
		case "settings":
			d.Overrides, d.CustomRules, d.Subscriptions = nil, nil, nil
		case "overrides":
			d = syncfile.Doc{Overrides: d.Overrides}
		case "customRules":
			d = syncfile.Doc{CustomRules: d.CustomRules}
		}
		return syncfile.Encode(d)
	}
	for _, part := range []string{"settings", "overrides", "customRules"} {
		if !bytes.Equal(section(cur, part), section(next, part)) {
			p.changed = append(p.changed, part)
		}
	}
	if len(p.put) > 0 || len(p.del) > 0 {
		p.changed = append(p.changed, "subscriptions")
	}
	return p, nil
}

func ruleKey(r model.CustomRule) string { return r.Type + "," + r.Payload + "," + r.Target }

// subEqual compares what the file sets of a subscription.
func subEqual(a, b model.Subscription) bool {
	return a.Name == b.Name && a.URL == b.URL && a.UserAgent == b.UserAgent && a.ViaProxy == b.ViaProxy &&
		a.AutoUpdateHours == b.AutoUpdateHours && a.TimeoutSec == b.TimeoutSec && equalStrings(a.Overrides, b.Overrides)
}

// applyDoc loads doc (a dry run only plans). The returned result has
// the diff of this machine's lanflow.yaml; on error nothing is stored
// unless the error says it came after the store was written.
func (s *Service) applyDoc(doc syncfile.Doc, dryRun bool) (model.ConfigApplyResult, error) {
	p, err := s.planDoc(doc)
	if err != nil {
		return model.ConfigApplyResult{}, err
	}
	res := model.ConfigApplyResult{Changed: p.changed, Diff: s.d.prof.Diff(p.before, p.after)}
	if len(p.changed) == 0 {
		res.Diff = nil
		res.Summary = i18n.T("文件与本机配置一致，没有需要加载的内容。")
		return res, nil
	}
	res.Summary = p.summary()
	if dryRun {
		return res, nil
	}
	if err := s.checkPlan(p); err != nil {
		return res, err
	}
	if err := s.storePlan(p); err != nil {
		return res, fmt.Errorf(i18n.T("保存配置失败，可能只加载了一部分：%v"), err)
	}
	if err := s.applyPlanSettings(p); err != nil {
		return res, err
	}
	res.Applied = true
	for _, id := range p.fresh {
		id := id
		s.goBackground(func() { s.downloadSynced(id) })
	}
	return res, nil
}

// summary says what loading the plan does, in one sentence.
func (p *applyPlan) summary() string {
	names := map[string]string{
		"settings": i18n.T("设置"), "overrides": i18n.T("覆写"), "customRules": i18n.T("自定义规则"), "subscriptions": i18n.T("订阅"),
	}
	var parts []string
	for _, c := range p.changed {
		parts = append(parts, names[c])
	}
	out := i18n.Tf("更新了%s", strings.Join(parts, i18n.T("、")))
	if n := len(p.missing); n > 0 {
		out += i18n.Tf("；%d 个订阅需要重新输入链接", n)
	}
	if p.keptCur != "" {
		out += i18n.Tf("；正在使用的订阅「%s」已保留", p.keptCur)
	}
	return out
}

// checkPlan validates the plan before anything is stored: the ports,
// and the composed config of the current subscription with the core.
func (s *Service) checkPlan(p *applyPlan) error {
	if err := s.checkSettingsChange(p.cur.set, p.next.set); err != nil {
		return &docError{"settings", err}
	}
	cur, ok := s.d.store.Current()
	if !ok {
		return nil // nothing to compose yet
	}
	if _, err := s.d.store.Content(cur.ID); err != nil {
		return nil // not downloaded yet: the core checks it when it is
	}
	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()
	if bin == "" {
		return nil
	}
	sel := cur.Overrides
	for _, sub := range p.next.subs {
		if sub.ID == cur.ID {
			sel = sub.Overrides
		}
	}
	set := p.next.set
	ovs := p.next.ovs
	pr, err := s.prepare(composeArgs{settings: &set, custom: p.next.rules, useRules: true, ovs: &ovs, subOverrides: &sel})
	if err != nil {
		return err
	}
	pr.discard()
	return nil
}

// storePlan writes the subscriptions, overrides and rules of the plan.
func (s *Service) storePlan(p *applyPlan) error {
	st := s.d.store
	for _, sub := range p.put {
		if _, err := st.Put(sub); err != nil {
			return err
		}
	}
	// overrides: update or add, drop the others, then order as the file
	want := map[string]bool{}
	for _, o := range p.next.ovs {
		want[o.ID] = true
		if old, ok := st.GetOverride(o.ID); ok && old == o {
			continue
		}
		if _, err := st.PutOverride(o); err != nil {
			return err
		}
	}
	for _, o := range st.Overrides() {
		if !want[o.ID] {
			if err := st.DeleteOverride(o.ID); err != nil {
				return err
			}
		}
	}
	for i, o := range p.next.ovs {
		for j, have := range st.Overrides() {
			if have.ID == o.ID && j != i {
				if err := st.MoveOverride(o.ID, i-j); err != nil {
					return err
				}
				break
			}
		}
	}
	if !rulesEqual(p.cur.rules, p.next.rules) {
		if _, err := st.SetCustomRules(p.next.rules); err != nil {
			return err
		}
	}
	for _, sub := range p.del {
		if err := st.Delete(sub.ID); err != nil {
			return err
		}
		s.forgetLinkSecret(sub.ID)
	}
	s.refreshStore(TopicSubs, TopicSettings, TopicRules)
	return nil
}

func rulesEqual(a, b []model.CustomRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if ruleKey(a[i]) != ruleKey(b[i]) {
			return false
		}
	}
	return true
}

// applyPlanSettings brings the core up to date: SaveSettings for changed
// settings (hot, reload or restart as each change needs), a reload when
// only the store changed what the current subscription composes.
func (s *Service) applyPlanSettings(p *applyPlan) error {
	settingsChanged := false
	storeChanged := false
	for _, c := range p.changed {
		if c == "settings" {
			settingsChanged = true
		} else {
			storeChanged = true
		}
	}
	reloaded := false
	if settingsChanged {
		if err := s.SaveSettings(p.next.set); err != nil {
			return &docError{"settings", err}
		}
		reloaded = needsReload(p.cur.set, p.next.set) || advancedNeedsReload(p.cur.set.Advanced, p.next.set.Advanced)
	}
	if storeChanged && !reloaded && s.running() {
		s.opMu.Lock()
		err := s.reloadLocked()
		s.opMu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// downloadSynced fetches a subscription that arrived through the file;
// the first one becomes the current subscription when there is none.
func (s *Service) downloadSynced(id string) {
	if err := s.UpdateSubscription(id); err != nil {
		s.alog.Warnf("config sync: download %s: %v", id, err)
		return
	}
	if _, ok := s.d.store.Current(); !ok {
		if err := s.UseSubscription(id); err != nil {
			s.alog.Warnf("config sync: use %s: %v", id, err)
		}
	}
}

// ApplyConfigFile implements API.ApplyConfigFile: loads a lanflow.yaml
// (path "" = the sync file) like an external change of the sync file —
// checked with the core first, nothing stored when anything fails. A dry
// run returns the diff only. The diff is this machine's lanflow.yaml
// text, links as keychain references, so it never reveals them.
func (s *Service) ApplyConfigFile(path string, dryRun bool) (res model.ConfigApplyResult, err error) {
	defer func() {
		if !dryRun {
			s.logOp("apply config file "+filepath.Base(path), err)
		}
	}()
	syncPath := ""
	if dir := s.syncDir(s.Settings().Sync); dir != "" {
		syncPath = filepath.Join(dir, syncfile.FileName)
	}
	if strings.TrimSpace(path) == "" {
		if syncPath == "" {
			return res, errors.New(i18n.T("配置同步未开启，请指定要加载的文件。"))
		}
		path = syncPath
	}
	if !filepath.IsAbs(path) {
		return res, errors.New(i18n.T("请使用文件的绝对路径。"))
	}
	fi, err := os.Stat(path)
	if err != nil {
		return res, fmt.Errorf(i18n.T("读取文件失败：%v"), err)
	}
	if fi.Size() > 8<<20 {
		return res, errors.New(i18n.T("文件过大，不像是澜流的设置文件。"))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return res, fmt.Errorf(i18n.T("读取文件失败：%v"), err)
	}
	doc, err := syncfile.Decode(data, defaultSettings())
	if err != nil {
		return res, err
	}
	s.cs.runMu.Lock()
	defer s.cs.runMu.Unlock()
	plain, webdav := s.unsealDoc(doc)
	res, err = s.applyDoc(plain, dryRun)
	if err != nil {
		var de *docError
		if errors.As(err, &de) {
			if line := syncfile.LineOf(data, de.path); line > 0 {
				err = fmt.Errorf(i18n.T("第 %d 行：%w"), line, err)
			}
		}
		return res, err
	}
	if !dryRun && path == syncPath {
		// loaded: the poller must not load it again
		s.rememberHash(path, syncfile.Hash(data))
		s.primeSeal(doc)
		s.adoptWebDAVPassword(webdav)
		s.cs.mu.Lock()
		s.cs.canon, s.cs.dirty = syncfile.Encode(doc), true
		s.cs.mu.Unlock()
	}
	return res, nil
}
