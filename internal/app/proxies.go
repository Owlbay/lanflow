package app

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

const (
	probeGroup          = "LANFLOW-PROBE"
	defaultDelayURL     = "https://www.gstatic.com/generate_204"
	defaultDelayTimeout = 5 * time.Second
	minDelayTimeout     = 1000  // ms
	maxDelayTimeout     = 20000 // ms
)

// validDelayURL reports whether u is an http(s) URL a delay test can use.
func validDelayURL(u string) bool {
	u = strings.TrimSpace(u)
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

// validDelayTimeout reports whether ms is within 1000–20000.
func validDelayTimeout(ms int) bool { return ms >= minDelayTimeout && ms <= maxDelayTimeout }

// delayParams returns the URL and timeout every delay test uses
// (Settings.DelayURL / DelayTimeoutMs, docs/design.md §5.8).
func (s *Service) delayParams() (string, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ms := s.state.settings.DelayURL, s.state.settings.DelayTimeoutMs
	if !validDelayURL(u) {
		u = defaultDelayURL
	}
	if !validDelayTimeout(ms) {
		return u, defaultDelayTimeout
	}
	return u, time.Duration(ms) * time.Millisecond
}

var errCoreNotRunning = i18n.Error("内核未启动。请先启动内核。")

// groupTypes maps Mihomo's adapter type names to the normalized group kinds.
var groupTypes = map[string]model.GroupType{
	"Selector":    model.GroupSelect,
	"URLTest":     model.GroupURLTest,
	"Fallback":    model.GroupFallback,
	"LoadBalance": model.GroupLoadBalance,
	"Relay":       model.GroupRelay,
}

// buildSnapshot turns GET /proxies into the UI view: groups in config order
// (GLOBAL's member list lists the groups in config order; GLOBAL itself goes
// last), normalized types, the probe group hidden, delays from the last
// history entry, and the main group.
func buildSnapshot(raw map[string]controller.Proxy, testing map[string]bool, mainPref string) model.ProxySnapshot {
	snap := model.ProxySnapshot{Proxies: make(map[string]model.Proxy, len(raw))}
	for name, p := range raw {
		if name == probeGroup {
			continue
		}
		mp := model.Proxy{Name: name, Type: p.Type, UDP: p.UDP, Provider: p.Provider, Testing: testing[name]}
		if n := len(p.History); n > 0 {
			mp.Delay = p.History[n-1].Delay
			if mp.Delay <= 0 {
				mp.Delay = -1
			}
		}
		snap.Proxies[name] = mp
	}
	// order: GLOBAL.all lists DIRECT, REJECT, nodes, then groups in config order.
	var order []string
	seen := map[string]bool{}
	add := func(name string) {
		if seen[name] || name == probeGroup || name == "GLOBAL" {
			return
		}
		p, ok := raw[name]
		if !ok {
			return
		}
		if _, isGroup := groupTypes[p.Type]; !isGroup {
			return
		}
		seen[name] = true
		order = append(order, name)
	}
	if g, ok := raw["GLOBAL"]; ok {
		for _, n := range g.All {
			add(n)
		}
	}
	// groups not reachable from GLOBAL (should not happen): append sorted for stability.
	var rest []string
	for name, p := range raw {
		if _, isGroup := groupTypes[p.Type]; isGroup && !seen[name] && name != "GLOBAL" && name != probeGroup {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	order = append(order, rest...)
	if _, ok := raw["GLOBAL"]; ok {
		order = append(order, "GLOBAL")
	}
	for _, name := range order {
		p := raw[name]
		g := model.ProxyGroup{Name: name, Type: groupTypes[p.Type], Now: p.Now, Hidden: p.Hidden}
		if g.Type == "" {
			g.Type = model.GroupSelect
		}
		for _, m := range p.All {
			if m != probeGroup {
				g.All = append(g.All, m)
			}
		}
		if g.Type == model.GroupLoadBalance {
			g.Now = ""
		}
		snap.Groups = append(snap.Groups, g)
	}
	snap.MainGroup = pickMainGroup(snap.Groups, mainPref)
	return snap
}

// pickMainGroup returns pref when it is a group, else the first visible
// select group, else the first group.
func pickMainGroup(groups []model.ProxyGroup, pref string) string {
	if pref != "" {
		for _, g := range groups {
			if g.Name == pref {
				return pref
			}
		}
	}
	for _, g := range groups {
		if g.Type == model.GroupSelect && !g.Hidden && g.Name != "GLOBAL" {
			return g.Name
		}
	}
	for _, g := range groups {
		if g.Name != "GLOBAL" {
			return g.Name
		}
	}
	if len(groups) > 0 {
		return groups[0].Name
	}
	return ""
}

// resolveNode follows group selections down to a node name.
func resolveNode(snap model.ProxySnapshot, name string) string {
	byName := map[string]model.ProxyGroup{}
	for _, g := range snap.Groups {
		byName[g.Name] = g
	}
	for i := 0; i < 16; i++ {
		g, ok := byName[name]
		if !ok || g.Now == "" {
			return name
		}
		name = g.Now
	}
	return name
}

// refreshProxies reloads GET /proxies (and the proxy-providers) into the
// snapshot.
func (s *Service) refreshProxies() {
	if !s.running() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctrlTimeout)
	raw, err := s.d.ctrl.Proxies(ctx)
	cancel()
	if err != nil {
		return
	}
	s.mu.Lock()
	s.state.rawProxy = raw
	s.state.proxies = buildSnapshot(raw, s.state.testing, s.state.settings.MainGroup)
	s.mu.Unlock()
	s.em.emit(TopicProxies)
	s.refreshProxyProviders()
	s.rebuildRoutes()
}

// setTesting marks names as being tested (or not) and emits.
func (s *Service) setTesting(names []string, on bool) {
	s.mu.Lock()
	for _, n := range names {
		if on {
			s.state.testing[n] = true
		} else {
			delete(s.state.testing, n)
		}
	}
	s.state.proxies = buildSnapshot(s.state.rawProxy, s.state.testing, s.state.settings.MainGroup)
	s.mu.Unlock()
	s.em.emit(TopicProxies)
}

// delayConcurrency bounds how many nodes are tested at once by testNodes.
const delayConcurrency = 8

// testNodes tests names one by one (delayConcurrency at a time) with the
// usual URL and timeout, recording each result the moment it arrives so
// Proxies() fills in progressively (docs/design.md §5.2 组内测速: 结果逐个出现).
// Emissions are coalesced by the emitter, so a burst of results costs the UI
// at most one redraw per coalesceWindow. It returns when every node is done.
func (s *Service) testNodes(names []string) {
	if len(names) == 0 {
		return
	}
	s.setTesting(names, true)
	delayURL, delayTimeout := s.delayParams()
	sem := make(chan struct{}, delayConcurrency)
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func(n string) {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), delayTimeout+5*time.Second)
			d, err := s.d.ctrl.ProxyDelay(ctx, n, delayURL, delayTimeout)
			cancel()
			if err != nil || d <= 0 {
				d = 0 // Mihomo records a failed test as delay 0; buildSnapshot shows it as 超时
			}
			s.finishDelay(n, d)
		}(n)
	}
	wg.Wait()
	s.refreshProxies()
}

// finishDelay records one node's result: appends it to the cached history
// (so the snapshot shows it before the next GET /proxies), clears the
// node's testing flag and emits.
func (s *Service) finishDelay(name string, delay int) {
	s.mu.Lock()
	if p, ok := s.state.rawProxy[name]; ok {
		p.History = append(append([]controller.DelayHistory(nil), p.History...), controller.DelayHistory{Time: s.d.now(), Delay: delay})
		s.state.rawProxy[name] = p
	}
	delete(s.state.testing, name)
	s.state.proxies = buildSnapshot(s.state.rawProxy, s.state.testing, s.state.settings.MainGroup)
	s.mu.Unlock()
	s.em.emit(TopicProxies)
}

// testableMembers keeps the members of a group that a delay test can say
// something about: nodes, not nested groups (the UI shows "—" for those)
// and not DIRECT/REJECT.
func testableMembers(snap model.ProxySnapshot, members []string) []string {
	groups := make(map[string]bool, len(snap.Groups))
	for _, g := range snap.Groups {
		groups[g.Name] = true
	}
	var out []string
	for _, m := range members {
		if groups[m] || m == "DIRECT" || m == "REJECT" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// SelectProxy selects name in group.
func (s *Service) SelectProxy(group, name string) error {
	if !s.running() {
		return errCoreNotRunning
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctrlTimeout)
	err := s.d.ctrl.SelectProxy(ctx, group, name)
	cancel()
	if err != nil {
		return fmt.Errorf(i18n.T("切换「%s」到「%s」失败：%v"), group, name, err)
	}
	s.refreshProxies()
	s.scheduleMainProbe()
	s.disconnectGroup(group)
	return nil
}

// disconnectGroup closes the active connections going through group when
// Settings.DisconnectOnSwitch asks for it (切换节点时断开该组的连接), so
// they reconnect through the new node. Failures are logged, not reported:
// the switch itself succeeded.
func (s *Service) disconnectGroup(group string) {
	s.mu.Lock()
	on := s.state.settings.DisconnectOnSwitch
	var ids []string
	if on {
		for _, c := range s.state.conns {
			if hasID(c.Chains, group) {
				ids = append(ids, c.ID)
			}
		}
	}
	s.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctrlTimeout)
	defer cancel()
	closed := 0
	for _, id := range ids {
		if err := s.d.ctrl.CloseConnection(ctx, id); err != nil {
			s.alog.Errorf("close connection %s after switching %q: %v", id, group, err)
			continue
		}
		closed++
	}
	s.alog.Infof("closed %d connection(s) of %q after switching its node", closed, group)
}

// TestGroupDelay tests every node of group, one by one, so results appear
// progressively; it returns when all are done. A node that fails or times
// out shows 超时 rather than failing the action.
func (s *Service) TestGroupDelay(group string) error {
	if !s.running() {
		return errCoreNotRunning
	}
	s.mu.Lock()
	var members []string
	found := false
	for _, g := range s.state.proxies.Groups {
		if g.Name == group {
			found = true
			members = testableMembers(s.state.proxies, g.All)
		}
	}
	s.mu.Unlock()
	if !found {
		return fmt.Errorf(i18n.T("没有找到代理组「%s」"), group)
	}
	s.testNodes(members)
	return nil
}

// TestProxyDelay tests one node (or a group's current node).
func (s *Service) TestProxyDelay(name string) error {
	if !s.running() {
		return errCoreNotRunning
	}
	// A timeout is reported by the delay (超时), not as an action failure.
	s.testNodes([]string{name})
	return nil
}

// TestAllDelays tests every node, one by one, in snapshot order.
func (s *Service) TestAllDelays() error {
	if !s.running() {
		return errCoreNotRunning
	}
	s.mu.Lock()
	var names []string
	for n, p := range s.state.proxies.Proxies {
		if _, isGroup := groupTypes[p.Type]; !isGroup && n != "DIRECT" && n != "REJECT" {
			names = append(names, n)
		}
	}
	s.mu.Unlock()
	sort.Strings(names)
	s.testNodes(names)
	return nil
}

// ---- proxy providers (代理集合) ----

// refreshProxyProviders reloads GET /providers/proxies.
func (s *Service) refreshProxyProviders() {
	if !s.running() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctrlTimeout)
	raw, err := s.d.ctrl.ProxyProviders(ctx)
	cancel()
	if err != nil {
		return
	}
	provs := make([]model.ProxyProvider, 0, len(raw))
	for name, p := range raw {
		mp := model.ProxyProvider{Name: name, VehicleType: p.VehicleType, TestURL: p.TestURL, UpdatedAt: p.UpdatedAt}
		for _, n := range p.Proxies {
			mp.Proxies = append(mp.Proxies, n.Name)
		}
		if si := p.SubscriptionInfo; si != nil {
			mp.Upload, mp.Download, mp.Total = si.Upload, si.Download, si.Total
			if si.Expire > 0 {
				mp.Expire = time.Unix(si.Expire, 0)
			}
		}
		provs = append(provs, mp)
	}
	sort.Slice(provs, func(i, j int) bool { return provs[i].Name < provs[j].Name })
	s.mu.Lock()
	s.state.pprovs = provs
	s.mu.Unlock()
	s.em.emit(TopicProxies)
}

// ProxyProviders returns the proxy-providers with their busy flags.
func (s *Service) ProxyProviders() []model.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.ProxyProvider, len(s.state.pprovs))
	for i, p := range s.state.pprovs {
		p.Proxies = append([]string(nil), p.Proxies...)
		p.Updating = s.state.pprovBusy[p.Name] == "update"
		p.Checking = s.state.pprovBusy[p.Name] == "check"
		out[i] = p
	}
	return out
}

// setProviderBusy marks a provider's running action ("" clears it) and emits.
func (s *Service) setProviderBusy(name, action string) {
	s.mu.Lock()
	if s.state.pprovBusy == nil {
		s.state.pprovBusy = map[string]string{}
	}
	if action == "" {
		delete(s.state.pprovBusy, name)
	} else {
		s.state.pprovBusy[name] = action
	}
	s.mu.Unlock()
	s.em.emit(TopicProxies)
}

// hasProvider reports whether name is a known proxy-provider.
func (s *Service) hasProvider(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.state.pprovs {
		if p.Name == name {
			return true
		}
	}
	return false
}

// UpdateProxyProvider re-downloads provider name and reloads the proxies.
func (s *Service) UpdateProxyProvider(name string) error {
	if !s.running() {
		return errCoreNotRunning
	}
	if !s.hasProvider(name) {
		return fmt.Errorf(i18n.T("没有找到代理集合「%s」"), name)
	}
	s.setProviderBusy(name, "update")
	defer s.setProviderBusy(name, "")
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	err := s.d.ctrl.UpdateProxyProvider(ctx, name)
	cancel()
	s.refreshProxies()
	if err != nil {
		return fmt.Errorf(i18n.T("更新代理集合「%s」失败：%v"), name, err)
	}
	return nil
}

// HealthCheckProvider tests every member of provider name; the members show
// as testing until the kernel reports back, then their delays are reloaded.
func (s *Service) HealthCheckProvider(name string) error {
	if !s.running() {
		return errCoreNotRunning
	}
	s.mu.Lock()
	var members []string
	found := false
	for _, p := range s.state.pprovs {
		if p.Name == name {
			found = true
			members = append([]string(nil), p.Proxies...)
		}
	}
	s.mu.Unlock()
	if !found {
		return fmt.Errorf(i18n.T("没有找到代理集合「%s」"), name)
	}
	s.setProviderBusy(name, "check")
	defer s.setProviderBusy(name, "")
	s.setTesting(members, true)
	defer s.setTesting(members, false)
	_, delayTimeout := s.delayParams()
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout+delayTimeout)
	err := s.d.ctrl.HealthCheckProvider(ctx, name)
	cancel()
	s.refreshProxies()
	if err != nil {
		return fmt.Errorf(i18n.T("代理集合「%s」健康检查失败：%v"), name, err)
	}
	return nil
}

// SetMainGroup records the main group and recomputes the snapshot.
func (s *Service) SetMainGroup(group string) error {
	s.mu.Lock()
	s.state.settings.MainGroup = group
	set := s.state.settings
	s.state.proxies = buildSnapshot(s.state.rawProxy, s.state.testing, group)
	s.mu.Unlock()
	s.em.emit(TopicProxies, TopicSettings)
	s.rebuildRoutes()
	s.scheduleMainProbe()
	return saveSettings(s.opt.DataDir, set)
}
