package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// Settings export / import (docs/design.md:485, P2). The file is
// lanflow.yaml (package syncfile, configdoc.go), the format configuration
// sync uses; the JSON export of earlier versions (model.SettingsExport)
// is still read. Subscription URLs carry access tokens, so they are
// written only when the user asks for them; machine-local items
// (syncfile.Excluded: the core path, the TUN choice, agent grants…) are
// never written and never overwritten by an import.

const (
	exportApp    = "lanflow"
	exportFormat = 1
)

// parseExport validates a JSON export of an earlier version.
func parseExport(data []byte) (model.SettingsExport, error) {
	var ex model.SettingsExport
	if err := json.Unmarshal(data, &ex); err != nil {
		return ex, fmt.Errorf(i18n.T("文件不是有效的 JSON：%v"), err)
	}
	if ex.App != exportApp {
		return ex, errors.New(i18n.T("这不是澜流导出的设置文件。"))
	}
	if ex.Format > exportFormat {
		return ex, fmt.Errorf(i18n.T("文件由更新版本的澜流导出（格式 %d），当前版本无法读取。"), ex.Format)
	}
	if ex.Settings == nil && ex.Overrides == nil && len(ex.CustomRules) == 0 && len(ex.Subscriptions) == 0 {
		return ex, errors.New(i18n.T("文件中没有可导入的内容。"))
	}
	for i, r := range ex.CustomRules {
		if _, err := validateCustomRule(r); err != nil {
			return ex, fmt.Errorf(i18n.T("第 %d 条自定义规则无效：%v"), i+1, err)
		}
	}
	for i, s := range ex.Subscriptions {
		if !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://") {
			return ex, fmt.Errorf(i18n.T("第 %d 个订阅的链接无效。"), i+1)
		}
	}
	return ex, nil
}

// ExportSettings implements API.ExportSettings.
func (s *Service) ExportSettings(path string, includeSubs bool) error {
	if strings.TrimSpace(path) == "" {
		return errors.New(i18n.T("没有选择保存位置。"))
	}
	data := s.encodeExport(includeSubs)
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf(i18n.T("写入 %s 失败：%v"), path, err)
	}
	return nil
}

// ImportSettings implements API.ImportSettings. Each part is applied
// through the normal paths (SaveSettings, SaveOverride, custom rules,
// ImportSubscriptionURL) so everything is validated the same way as a
// manual edit; parts that fail are reported in the summary and the rest
// still applies.
func (s *Service) ImportSettings(path string) (summary string, err error) {
	defer func() { s.logOp("import settings "+filepath.Base(path), err) }()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf(i18n.T("读取文件失败：%v"), err)
	}
	if len(data) > 8<<20 {
		return "", errors.New(i18n.T("文件过大，不像是澜流的设置文件。"))
	}
	ex, notes, err := s.parseImport(data)
	if err != nil {
		return "", err
	}

	var done, skipped []string
	skipped = append(skipped, notes...)

	if ex.Settings != nil {
		cur := s.Settings()
		// machine-local choices stay as they are
		in := syncfile.KeepLocal(copySettings(*ex.Settings), cur)
		if err := s.SaveSettings(in); err != nil {
			skipped = append(skipped, i18n.Tf("设置（%s）", err.Error()))
		} else {
			done = append(done, i18n.T("设置|项"))
		}
	}

	if ex.Overrides != nil {
		cur := s.Overrides()
		// The legacy single texts: from older files, or from the list's
		// "global" and per-subscription entries.
		global, perSub := ex.Overrides.Global, map[string]string{}
		for id, text := range ex.Overrides.PerSub {
			perSub[id] = text
		}
		var others []model.Override
		for _, o := range ex.Overrides.List {
			switch {
			case o.ID == "global":
				global = o.Text
			case s.hasSubscription(o.ID):
				perSub[o.ID] = o.Text
			default:
				others = append(others, o)
			}
		}
		if global != cur.Global {
			if err := s.SaveOverride("global", global); err != nil {
				skipped = append(skipped, i18n.Tf("全局覆写（%s）", err.Error()))
			} else {
				done = append(done, i18n.T("全局覆写|项"))
			}
		}
		applied, unknown := 0, 0
		for id, text := range perSub {
			if !s.hasSubscription(id) {
				unknown++
				continue
			}
			if text == cur.PerSub[id] {
				continue
			}
			if err := s.SaveOverride(id, text); err != nil {
				skipped = append(skipped, i18n.Tf("订阅覆写（%s）", err.Error()))
				continue
			}
			applied++
		}
		if applied > 0 {
			done = append(done, fmt.Sprintf(i18n.T("%d 条订阅覆写"), applied))
		}
		if unknown > 0 {
			skipped = append(skipped, fmt.Sprintf(i18n.T("%d 条订阅覆写（对应的订阅不在本机）"), unknown))
		}
		n, errs := s.mergeOverrideList(others, cur.List)
		if n > 0 {
			done = append(done, fmt.Sprintf(i18n.T("%d 条覆写"), n))
		}
		skipped = append(skipped, errs...)
	}

	if len(ex.CustomRules) > 0 {
		n, err := s.mergeCustomRules(ex.CustomRules)
		switch {
		case err != nil:
			skipped = append(skipped, i18n.Tf("自定义规则（%s）", err.Error()))
		case n > 0:
			done = append(done, fmt.Sprintf(i18n.T("%d 条自定义规则"), n))
		}
	}

	if len(ex.Subscriptions) > 0 {
		have := map[string]bool{}
		for _, sub := range s.Subscriptions() {
			have[sub.URL] = true
		}
		n := 0
		for _, es := range ex.Subscriptions {
			if have[es.URL] {
				continue
			}
			sub, err := s.ImportSubscriptionURL(es.URL, es.Name, es.UserAgent, es.ViaProxy, false)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf(i18n.T("订阅「%s」（%v）"), es.Name, err))
				continue
			}
			if es.AutoUpdateHours != sub.AutoUpdateHours {
				sub.AutoUpdateHours = es.AutoUpdateHours
				_ = s.EditSubscription(sub)
			}
			n++
		}
		if n > 0 {
			done = append(done, fmt.Sprintf(i18n.T("%d 个订阅"), n))
		}
	}

	if len(done) == 0 {
		if len(skipped) == 0 {
			return i18n.T("设置与文件中的一致，没有需要导入的内容。"), nil
		}
		return "", errors.New(i18n.T("没有导入任何内容：") + strings.Join(skipped, i18n.T("；")))
	}
	summary = i18n.T("已导入 ") + strings.Join(done, i18n.T("、"))
	if len(skipped) > 0 {
		summary += i18n.Tf("。未导入：%s", strings.Join(skipped, i18n.T("；")))
	}
	return summary, nil
}

// hasSubscription reports whether id is a subscription on this machine.
func (s *Service) hasSubscription(id string) bool {
	_, ok := s.d.store.Get(id)
	return ok
}

// mergeOverrideList adds the imported override entries as new entries
// (local IDs are assigned here), skipping those an entry of the same name
// and text already covers. It returns how many were added and the
// failures.
func (s *Service) mergeOverrideList(in, cur []model.Override) (int, []string) {
	have := map[string]bool{}
	for _, o := range cur {
		have[o.Name+"\x00"+o.Text] = true
	}
	n := 0
	var errs []string
	for _, o := range in {
		if have[o.Name+"\x00"+o.Text] {
			continue
		}
		o.ID = ""
		saved, err := s.SaveOverrideEntry(o)
		if err != nil {
			errs = append(errs, i18n.Tf("覆写「%s」（%s）", o.Name, err.Error()))
			continue
		}
		have[saved.Name+"\x00"+saved.Text] = true
		n++
	}
	return n, errs
}

// mergeCustomRules appends the imported rules that do not exist yet and
// returns how many were added.
func (s *Service) mergeCustomRules(in []model.CustomRule) (int, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	old := s.d.store.CustomRules()
	seen := map[string]bool{}
	for _, r := range old {
		seen[r.Type+","+r.Payload+","+r.Target] = true
	}
	rules := append([]model.CustomRule(nil), old...)
	added := 0
	for _, r := range in {
		r, err := validateCustomRule(r)
		if err != nil {
			return 0, err
		}
		key := r.Type + "," + r.Payload + "," + r.Target
		if seen[key] {
			continue
		}
		seen[key] = true
		r.ID = "" // the store assigns local IDs
		rules = append(rules, r)
		added++
	}
	if added == 0 {
		return 0, nil
	}
	if _, err := s.applyCustomRules(old, rules); err != nil {
		return 0, err
	}
	return added, nil
}
