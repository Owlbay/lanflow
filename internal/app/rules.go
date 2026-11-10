package app

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

const (
	testRuleWait = 3 * time.Second
	testRulePoll = 200 * time.Millisecond
)

// customRuleTypes are the rule types users may add.
var customRuleTypes = map[string]bool{
	"DOMAIN-SUFFIX": true, "DOMAIN": true, "DOMAIN-KEYWORD": true, "IP-CIDR": true, "PROCESS-NAME": true,
}

// mapRules converts GET /rules; the first customCount entries are Lanflow's.
func mapRules(raw []controller.Rule, customCount int) []model.Rule {
	out := make([]model.Rule, 0, len(raw))
	for i, r := range raw {
		idx := r.Index
		if idx == 0 && i > 0 {
			idx = i // older cores do not report index
		}
		m := model.Rule{Index: idx, Type: r.Type, Payload: r.Payload, Proxy: r.Proxy, Size: r.Size, Custom: idx < customCount}
		if r.Size == 0 {
			m.Size = -1
		}
		if r.Extra != nil {
			m.Disabled = r.Extra.Disabled
			m.HitCount = int64(r.Extra.HitCount)
			m.HitAt = r.Extra.HitAt
		}
		out = append(out, m)
	}
	return out
}

// refreshRules reloads GET /rules, emitting only when they changed. It
// runs after every config change and on demand (Rules, when the list is
// older than refreshInterval): a long rule list is not worth fetching
// every 10 s while nobody looks at it.
func (s *Service) refreshRules() {
	if !s.running() {
		return
	}
	ctx, cancel := s.ctrlCtx(nil, ctrlTimeout)
	raw, err := s.d.ctrl.Rules(ctx)
	cancel()
	s.mu.Lock()
	s.state.rulesBusy = false
	if err != nil {
		s.mu.Unlock()
		return
	}
	rules := mapRules(raw, len(s.state.custom))
	changed := !reflect.DeepEqual(rules, s.state.rules)
	s.state.rules = rules
	s.state.rulesAt = s.d.now()
	s.mu.Unlock()
	if changed {
		s.em.emit(TopicRules)
	}
}

// refreshRulesIfStaleLocked starts a background refreshRules when the
// rules (hit counts) are older than refreshInterval; call with mu held.
func (s *Service) refreshRulesIfStaleLocked() {
	if s.state.st.Core != model.CoreRunning || s.state.rulesBusy || s.d.now().Sub(s.state.rulesAt) < refreshInterval {
		return
	}
	s.state.rulesBusy = true
	s.goBackground(s.refreshRules)
}

// refreshProviders reloads GET /providers/rules.
func (s *Service) refreshProviders() {
	if !s.running() {
		return
	}
	ctx, cancel := s.ctrlCtx(nil, ctrlTimeout)
	raw, err := s.d.ctrl.RuleProviders(ctx)
	cancel()
	if err != nil {
		return
	}
	provs := make([]model.RuleProvider, 0, len(raw))
	for name, p := range raw {
		provs = append(provs, model.RuleProvider{Name: name, Behavior: p.Behavior, Format: p.Format, Count: p.RuleCount, VehicleType: p.VehicleType, UpdatedAt: p.UpdatedAt})
	}
	sort.Slice(provs, func(i, j int) bool { return provs[i].Name < provs[j].Name })
	s.mu.Lock()
	changed := !reflect.DeepEqual(provs, s.state.provs)
	s.state.provs = provs
	s.mu.Unlock()
	if changed {
		s.em.emit(TopicRules)
	}
}

// validateCustomRule checks a rule the user typed.
func validateCustomRule(r model.CustomRule) (model.CustomRule, error) {
	r.Type = strings.ToUpper(strings.TrimSpace(r.Type))
	r.Payload = strings.TrimSpace(r.Payload)
	r.Target = strings.TrimSpace(r.Target)
	if !customRuleTypes[r.Type] {
		return r, fmt.Errorf(i18n.T("不支持的规则类型「%s」"), r.Type)
	}
	if r.Payload == "" {
		return r, errors.New(i18n.T("规则内容不能为空。"))
	}
	if strings.ContainsAny(r.Payload, ", \t\n") {
		return r, errors.New(i18n.T("规则内容不能包含逗号或空格。"))
	}
	if r.Type == "IP-CIDR" {
		if _, _, err := net.ParseCIDR(r.Payload); err != nil {
			if ip := net.ParseIP(r.Payload); ip != nil {
				if ip.To4() != nil {
					r.Payload += "/32"
				} else {
					r.Payload += "/128"
				}
			} else {
				return r, errors.New(i18n.T("IP 段格式错误，例如 192.168.1.0/24。"))
			}
		}
	}
	if r.Target == "" {
		return r, errors.New(i18n.T("请选择规则的走向。"))
	}
	return r, nil
}

// SaveCustomRule adds (empty ID, placed first) or updates a custom rule,
// then recomposes and reloads. On failure the previous rules are restored.
func (s *Service) SaveCustomRule(r model.CustomRule) (model.CustomRule, error) {
	r, err := validateCustomRule(r)
	if err != nil {
		return r, err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	old := s.d.store.CustomRules()
	rules := append([]model.CustomRule(nil), old...)
	if r.ID == "" {
		rules = append([]model.CustomRule{r}, rules...)
	} else {
		found := false
		for i := range rules {
			if rules[i].ID == r.ID {
				rules[i] = r
				found = true
			}
		}
		if !found {
			rules = append([]model.CustomRule{r}, rules...)
		}
	}
	saved, err := s.applyCustomRules(old, rules)
	if err != nil {
		return r, err
	}
	for _, c := range saved {
		if c.Type == r.Type && c.Payload == r.Payload && c.Target == r.Target && (r.ID == "" || c.ID == r.ID) {
			return c, nil
		}
	}
	return r, nil
}

// applyCustomRules stores rules, recomposes and reloads; it restores old on failure.
func (s *Service) applyCustomRules(old, rules []model.CustomRule) ([]model.CustomRule, error) {
	saved, err := s.d.store.SetCustomRules(rules)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("保存自定义规则失败：%v"), err)
	}
	if err := s.reloadLocked(); err != nil {
		_, _ = s.d.store.SetCustomRules(old)
		s.refreshStore(TopicRules)
		return nil, fmt.Errorf(i18n.T("规则已撤销：%v"), err)
	}
	s.refreshStore(TopicRules)
	s.refreshRules()
	return saved, nil
}

// DeleteCustomRule removes a custom rule.
func (s *Service) DeleteCustomRule(id string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	old := s.d.store.CustomRules()
	rules := make([]model.CustomRule, 0, len(old))
	for _, r := range old {
		if r.ID != id {
			rules = append(rules, r)
		}
	}
	if len(rules) == len(old) {
		return nil
	}
	_, err := s.applyCustomRules(old, rules)
	return err
}

// MoveCustomRule moves a rule by delta positions (-1 = up).
func (s *Service) MoveCustomRule(id string, delta int) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	old := s.d.store.CustomRules()
	rules := append([]model.CustomRule(nil), old...)
	idx := -1
	for i, r := range rules {
		if r.ID == id {
			idx = i
		}
	}
	if idx < 0 || delta == 0 {
		return nil
	}
	to := idx + delta
	if to < 0 {
		to = 0
	}
	if to >= len(rules) {
		to = len(rules) - 1
	}
	if to == idx {
		return nil
	}
	r := rules[idx]
	rules = append(rules[:idx], rules[idx+1:]...)
	rules = append(rules[:to], append([]model.CustomRule{r}, rules[to:]...)...)
	_, err := s.applyCustomRules(old, rules)
	return err
}

// SetRuleDisabled temporarily disables a runtime rule (until the next reload).
func (s *Service) SetRuleDisabled(index int, disabled bool) error {
	if !s.running() {
		return errCoreNotRunning
	}
	ctx, cancel := s.ctrlCtx(nil, ctrlTimeout)
	err := s.d.ctrl.DisableRules(ctx, map[int]bool{index: disabled})
	cancel()
	if err != nil {
		return fmt.Errorf(i18n.T("停用规则失败：%v"), err)
	}
	s.refreshRules()
	return nil
}

// UpdateRuleProvider re-downloads a rule provider.
func (s *Service) UpdateRuleProvider(name string) error {
	if !s.running() {
		return errCoreNotRunning
	}
	s.mu.Lock()
	s.state.provUpdating[name] = true
	s.mu.Unlock()
	s.em.emit(TopicRules)
	defer func() {
		s.mu.Lock()
		delete(s.state.provUpdating, name)
		s.mu.Unlock()
		s.em.emit(TopicRules)
	}()
	ctx, cancel := s.ctrlCtx(nil, fetchTimeout)
	err := s.d.ctrl.UpdateRuleProvider(ctx, name)
	cancel()
	s.refreshProviders()
	if err != nil {
		return fmt.Errorf(i18n.T("更新规则集「%s」失败：%v"), name, err)
	}
	return nil
}

// testTarget splits user input into the host to match and a URL to request.
func testTarget(input string) (host, reqURL string, err error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return "", "", errors.New(i18n.T("请输入域名或 IP 地址。"))
	}
	if !strings.Contains(in, "://") {
		in = "https://" + in
	}
	u, err := url.Parse(in)
	if err != nil || u.Hostname() == "" {
		return "", "", errors.New(i18n.T("地址格式不正确，例如 www.google.com 或 1.1.1.1。"))
	}
	host = u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	scheme := "https"
	if port == "80" || u.Scheme == "http" {
		scheme = "http"
	}
	reqURL = scheme + "://" + net.JoinHostPort(host, port) + "/"
	return host, reqURL, nil
}

// TestRule sends one request for host through the core and reads back
// which rule the resulting connection matched.
func (s *Service) TestRule(input string) (model.RuleMatch, error) {
	host, reqURL, err := testTarget(input)
	if err != nil {
		return model.RuleMatch{}, err
	}
	s.mu.Lock()
	running := s.state.st.Core == model.CoreRunning
	port := s.state.settings.MixedPort
	rules := append([]model.Rule(nil), s.state.rules...)
	customCount := len(s.state.custom)
	s.mu.Unlock()
	if !running {
		return model.RuleMatch{}, errCoreNotRunning
	}
	started := s.d.now()
	client := s.d.httpVia(port, testRuleWait)
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, err := http.NewRequest(http.MethodHead, reqURL, nil)
		if err != nil {
			return
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	// The connections stream already reports every connection each
	// second (connInterval): watch its state instead of asking the core
	// for the whole list five times a second.
	wait := testRuleWait
	if iv := 2*s.connInterval() + time.Second; iv > wait {
		wait = iv // the window is in the background: the stream is slower
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(testRulePoll)
		s.mu.Lock()
		found, open := pickTestConnState(s.state.conns, &s.state.closed, host, started)
		var m model.Connection
		if found != nil {
			m = *found
			m.Chains = append([]string(nil), found.Chains...)
		}
		s.mu.Unlock()
		if found == nil {
			continue
		}
		if open {
			ctx, cancel := s.ctrlCtx(nil, ctrlTimeout)
			_ = s.d.ctrl.CloseConnection(ctx, m.ID)
			cancel()
		}
		return matchFrom(m.Rule, m.RulePayload, m.Chains, host, rules, customCount), nil
	}
	<-done
	return model.RuleMatch{}, errors.New(i18n.T("3 秒内没有在连接列表中看到这次请求，无法得知命中的规则。请检查内核是否正常，然后重试。"))
}

// pickTestConnState returns the newest connection to host started after
// since, among the open connections (open = true) or else the closed ring.
// Call with mu held; the result points into state.
func pickTestConnState(conns []model.Connection, closed *ring[model.Connection], host string, since time.Time) (best *model.Connection, open bool) {
	match := func(c *model.Connection) bool {
		return (connHost(c.Host) == host || c.DstIP == host) && !c.Start.Before(since.Add(-time.Second))
	}
	for i := range conns {
		if c := &conns[i]; match(c) && (best == nil || c.Start.After(best.Start)) {
			best = c
		}
	}
	if best != nil {
		return best, true
	}
	for i := closed.Len() - 1; i >= 0; i-- { // newest closed first
		c := closed.At(i)
		if match(&c) {
			return &c, false
		}
	}
	return nil, false
}

func connHost(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return hostPort
}

// matchFrom builds the RuleMatch from a connection's rule fields and chain.
func matchFrom(ruleType, payload string, chains []string, host string, rules []model.Rule, customCount int) model.RuleMatch {
	m := model.RuleMatch{Host: host, RuleIndex: -1, Chain: append([]string(nil), chains...)}
	if len(chains) > 0 {
		m.Policy = chains[0]
	}
	if payload != "" {
		m.Rule = ruleType + "," + payload
	} else {
		m.Rule = ruleType
	}
	for _, r := range rules {
		if strings.EqualFold(r.Type, ruleType) && r.Payload == payload {
			m.RuleIndex = r.Index
			m.Custom = r.Index < customCount
			m.Rule = r.Type + "," + r.Payload
			if r.Payload == "" {
				m.Rule = r.Type
			}
			break
		}
	}
	return m
}
