package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/core"
	"lanflow/internal/locate"
	"lanflow/internal/model"
	"lanflow/internal/platform/atrust"
	"lanflow/internal/platform/helper"
	"lanflow/internal/platform/sysdns"
	"lanflow/internal/platform/sysproxy"
	"lanflow/internal/profile"
)

// ---- controller ----

type fakeCtrl struct {
	mu       sync.Mutex
	proxies  map[string]controller.Proxy
	conns    controller.Connections
	rules    []controller.Rule
	provs    map[string]controller.RuleProvider
	patches  []controller.ConfigPatch
	selected [][2]string
	reloads  int
	reloadFn func(path string) error
	version  string
	trafficC chan controller.Traffic
	connsC   chan controller.Connections
	logsC    chan controller.Log
	// connIntervals are the intervals of every StreamConnections call.
	connIntervals []time.Duration

	// proxy providers
	pprovs   map[string]controller.ProxyProvider
	pupdated []string // UpdateProxyProvider calls
	pchecked []string // HealthCheckProvider calls
	// delayFn, when set, answers ProxyDelay (default: 100 ms for every node).
	delayFn func(name string) (int, error)
	// dnsAnswers, when set, is what DNSQuery returns (default one fake-ip).
	dnsAnswers []string
	// closedIDs are the CloseConnection calls.
	closedIDs []string
	// delays records the (url, timeout) of every ProxyDelay call.
	delays [][2]string
}

func newFakeCtrl() *fakeCtrl {
	return &fakeCtrl{
		version:  "v1.19.33",
		proxies:  map[string]controller.Proxy{},
		trafficC: make(chan controller.Traffic, 16),
		connsC:   make(chan controller.Connections, 16),
		logsC:    make(chan controller.Log, 16),
	}
}

func (f *fakeCtrl) Version(context.Context) (string, error) { return f.version, nil }
func (f *fakeCtrl) Configs(context.Context) (controller.Configs, error) {
	return controller.Configs{}, nil
}
func (f *fakeCtrl) PatchConfigs(_ context.Context, p controller.ConfigPatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches = append(f.patches, p)
	return nil
}
func (f *fakeCtrl) ReloadConfig(_ context.Context, path string) error {
	f.mu.Lock()
	f.reloads++
	fn := f.reloadFn
	f.mu.Unlock()
	if fn != nil {
		return fn(path)
	}
	return nil
}
func (f *fakeCtrl) UpdateGeo(context.Context) error { return nil }
func (f *fakeCtrl) Proxies(context.Context) (map[string]controller.Proxy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]controller.Proxy, len(f.proxies))
	for k, v := range f.proxies {
		out[k] = v
	}
	return out, nil
}
func (f *fakeCtrl) SelectProxy(_ context.Context, group, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selected = append(f.selected, [2]string{group, name})
	if p, ok := f.proxies[group]; ok {
		p.Now = name
		f.proxies[group] = p
	}
	return nil
}
func (f *fakeCtrl) ProxyDelay(_ context.Context, name, url string, timeout time.Duration) (int, error) {
	f.mu.Lock()
	fn := f.delayFn
	f.delays = append(f.delays, [2]string{url, timeout.String()})
	f.mu.Unlock()
	d, err := 100, error(nil)
	if fn != nil {
		d, err = fn(name)
	}
	// Like the kernel, record the result in the node's history (0 = failed).
	f.mu.Lock()
	if p, ok := f.proxies[name]; ok {
		rec := d
		if err != nil {
			rec = 0
		}
		p.History = append(append([]controller.DelayHistory(nil), p.History...), controller.DelayHistory{Time: time.Now(), Delay: rec})
		f.proxies[name] = p
	}
	f.mu.Unlock()
	return d, err
}
func (f *fakeCtrl) GroupDelay(context.Context, string, string, time.Duration) (map[string]int, error) {
	return map[string]int{}, nil
}
func (f *fakeCtrl) Rules(context.Context) ([]controller.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]controller.Rule(nil), f.rules...), nil
}
func (f *fakeCtrl) DisableRules(context.Context, map[int]bool) error { return nil }
func (f *fakeCtrl) RuleProviders(context.Context) (map[string]controller.RuleProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.provs, nil
}
func (f *fakeCtrl) UpdateRuleProvider(context.Context, string) error { return nil }
func (f *fakeCtrl) ProxyProviders(context.Context) (map[string]controller.ProxyProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pprovs, nil
}
func (f *fakeCtrl) UpdateProxyProvider(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pupdated = append(f.pupdated, name)
	if p, ok := f.pprovs[name]; ok {
		p.UpdatedAt = time.Now()
		f.pprovs[name] = p
	}
	return nil
}
func (f *fakeCtrl) HealthCheckProvider(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pchecked = append(f.pchecked, name)
	return nil
}
func (f *fakeCtrl) Connections(context.Context) (controller.Connections, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns, nil
}
func (f *fakeCtrl) CloseConnection(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedIDs = append(f.closedIDs, id)
	return nil
}
func (f *fakeCtrl) CloseAllConnections(context.Context) error { return nil }
func (f *fakeCtrl) DNSQuery(context.Context, string, string) (controller.DNSResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.dnsAnswers) > 0 {
		var out controller.DNSResult
		for _, a := range f.dnsAnswers {
			out.Answer = append(out.Answer, controller.DNSAnswer{Data: a})
		}
		return out, nil
	}
	return controller.DNSResult{Answer: []controller.DNSAnswer{{Data: "198.18.0.5"}}}, nil
}
func (f *fakeCtrl) FlushDNSCache(context.Context) error    { return nil }
func (f *fakeCtrl) FlushFakeIPCache(context.Context) error { return nil }
func (f *fakeCtrl) StreamTraffic(ctx context.Context, fn func(controller.Traffic)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-f.trafficC:
			fn(t)
		}
	}
}
func (f *fakeCtrl) StreamMemory(ctx context.Context, _ func(controller.Memory)) error {
	<-ctx.Done()
	return ctx.Err()
}
func (f *fakeCtrl) StreamLogs(ctx context.Context, _ string, fn func(controller.Log)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case l := <-f.logsC:
			fn(l)
		}
	}
}
func (f *fakeCtrl) StreamConnections(ctx context.Context, interval time.Duration, fn func(controller.Connections)) error {
	f.mu.Lock()
	f.connIntervals = append(f.connIntervals, interval)
	f.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c := <-f.connsC:
			fn(c)
		}
	}
}

// ---- manager ----

type fakeMgr struct {
	mu       sync.Mutex
	running  bool
	starts   int
	stops    int
	startErr error
	startFn  func(spec core.Spec) error // when set, decides each Start (cores tests)
	onStop   func()                     // when set, called on each Stop of a running core (cores tests)
	runner   core.Runner
	specs    []core.Spec
	crashCh  chan error
}

func newFakeMgr() *fakeMgr { return &fakeMgr{crashCh: make(chan error, 4)} }

func (m *fakeMgr) Start(_ context.Context, spec core.Spec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts++
	m.specs = append(m.specs, spec)
	if m.startFn != nil {
		if err := m.startFn(spec); err != nil {
			return err
		}
	} else if m.startErr != nil {
		return m.startErr
	}
	m.running = true
	return nil
}
func (m *fakeMgr) Stop(context.Context) error {
	m.mu.Lock()
	wasRunning := m.running
	if m.running {
		m.stops++
	}
	m.running = false
	onStop := m.onStop
	m.mu.Unlock()
	if wasRunning && onStop != nil {
		onStop()
	}
	return nil
}
func (m *fakeMgr) SetRunner(r core.Runner) { m.mu.Lock(); m.runner = r; m.mu.Unlock() }
func (m *fakeMgr) Running() bool           { m.mu.Lock(); defer m.mu.Unlock(); return m.running }
func (m *fakeMgr) Crashes() <-chan error   { return m.crashCh }

// crash simulates an unexpected exit.
func (m *fakeMgr) crash(err error) {
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
	m.crashCh <- err
}

// ---- core tools ----

type namedRunner struct{ name string }

func (namedRunner) Start(context.Context, core.Spec) error { return nil }
func (namedRunner) Stop(context.Context) error             { return nil }
func (namedRunner) Running() bool                          { return false }
func (namedRunner) Exited() <-chan error                   { return nil }

type fakeCore struct {
	mu          sync.Mutex
	bin         string
	findErr     error
	validateErr error
	validated   []string
	findFn      func(custom string) (string, error) // when set, replaces bin/findErr (coredl tests)
	versionFn   func(bin string) (string, error)    // when set, replaces the fixed version
}

func (c *fakeCore) FindBinary(custom string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.findFn != nil {
		return c.findFn(custom)
	}
	if c.findErr != nil {
		return "", c.findErr
	}
	return c.bin, nil
}
func (c *fakeCore) Version(_ context.Context, bin string) (string, error) {
	c.mu.Lock()
	fn := c.versionFn
	c.mu.Unlock()
	if fn != nil {
		return fn(bin)
	}
	return "v1.19.33", nil
}
func (c *fakeCore) Validate(_ context.Context, _, _, config string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.validated = append(c.validated, config)
	return c.validateErr
}
func (c *fakeCore) ExecRunner() core.Runner   { return namedRunner{"exec"} }
func (c *fakeCore) HelperRunner() core.Runner { return namedRunner{"helper"} }

// ---- system proxy ----

type fakeSys struct {
	mu              sync.Mutex
	state           sysproxy.State
	sets            int
	clears          int
	getErr          error
	snaps, restores int
	pacSets         int
}

func (f *fakeSys) Set(host string, port int, bypass []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	f.state = sysproxy.State{Enabled: true, Host: host, Port: port, Bypass: bypass}
	return nil
}
func (f *fakeSys) Clear() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clears++
	f.state = sysproxy.State{}
	return nil
}
func (f *fakeSys) SetPAC(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if url == "" {
		f.clears++
	} else {
		f.pacSets++
	}
	f.state.PACURL = url
	return nil
}
func (f *fakeSys) Get() (sysproxy.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.getErr
}
func (f *fakeSys) Snapshot() (sysproxy.Saved, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snaps++
	return sysproxy.Saved(fmt.Sprintf(`{"enabled":%v,"host":%q,"port":%d,"pac":%q}`, f.state.Enabled, f.state.Host, f.state.Port, f.state.PACURL)), nil
}
func (f *fakeSys) Restore(s sysproxy.Saved) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restores++
	var st struct {
		Enabled bool
		Host    string
		Port    int
		PAC     string
	}
	if err := json.Unmarshal(s, &st); err != nil {
		return err
	}
	f.state = sysproxy.State{Enabled: st.Enabled, Host: st.Host, Port: st.Port, PACURL: st.PAC}
	return nil
}

// counts returns how often the proxy was set and released (cleared or
// restored to its saved state).
func (f *fakeSys) counts() (sets, clears int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sets, f.clears + f.restores
}

// ---- system DNS ----

type fakeDNS struct {
	mu       sync.Mutex
	servers  []string // nil = automatic
	sets     int
	restores int
}

func (f *fakeDNS) Supported() bool { return true }
func (f *fakeDNS) Snapshot() (sysdns.Saved, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return json.Marshal(f.servers)
}
func (f *fakeDNS) Set(servers []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	f.servers = append([]string(nil), servers...)
	return nil
}
func (f *fakeDNS) Restore(s sysdns.Saved, ours []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.servers, ours) {
		return nil
	}
	f.restores++
	return json.Unmarshal(s, &f.servers)
}
func (f *fakeDNS) get() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.servers
}

// ---- helper ----

type fakeHelper struct {
	mu         sync.Mutex
	installed  bool
	installs   []helper.InstallOptions // every Install call (coredl tests)
	installErr error
}

func (h *fakeHelper) Installed(context.Context) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.installed
}
func (h *fakeHelper) Install(_ context.Context, o helper.InstallOptions) error {
	h.mu.Lock()
	h.installs = append(h.installs, o)
	if h.installErr != nil {
		h.mu.Unlock()
		return h.installErr
	}
	h.installed = true
	h.mu.Unlock()
	return nil
}
func (h *fakeHelper) Uninstall(context.Context) error {
	h.mu.Lock()
	h.installed = false
	h.mu.Unlock()
	return nil
}

// ---- locator ----

type fakeLoc struct {
	mu     sync.Mutex
	probed []string
	// directErr, when set, decides whether Direct fails (public IP retry tests).
	directErr func() error
}

func (l *fakeLoc) Guess(node string) model.ExitInfo {
	if strings.Contains(node, "东京") {
		p := model.Place{Country: "JP", City: "东京", Lat: 35.68, Lon: 139.69}
		return model.ExitInfo{Node: node, Place: &p, Source: model.LocateName}
	}
	return model.ExitInfo{Node: node, Source: model.LocateUnknown}
}
func (l *fakeLoc) Probe(ctx context.Context, sel locate.SelectFunc, _ int, node string) (model.ExitInfo, error) {
	l.mu.Lock()
	l.probed = append(l.probed, node)
	l.mu.Unlock()
	if err := sel(ctx, node); err != nil {
		return model.ExitInfo{}, err
	}
	p := model.Place{Country: "HK", City: "香港"}
	return model.ExitInfo{Node: node, Place: &p, Source: model.LocateProbe, Colo: "HKG"}, nil
}
func (l *fakeLoc) Direct(context.Context) (model.EndpointInfo, error) {
	if l.directErr != nil {
		if err := l.directErr(); err != nil {
			return model.EndpointInfo{}, err
		}
	}
	return model.EndpointInfo{IP: "1.2.3.4"}, nil
}
func (l *fakeLoc) Analyze(info *model.ExitInfo, entryIP string, hops int) {
	info.Entry = &model.EndpointInfo{IP: entryIP}
	info.ChainHops = hops
	info.Path = model.PathDirect
}
func (l *fakeLoc) Lookup(ip string) model.EndpointInfo { return model.EndpointInfo{IP: ip} }

// ---- profile store ----

type fakeStore struct {
	mu      sync.Mutex
	subs    []model.Subscription
	current string
	content map[string][]byte
	ovs     []model.Override
	rules   []model.CustomRule
	nextID  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{content: map[string][]byte{}}
}

func (s *fakeStore) List() []model.Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]model.Subscription(nil), s.subs...)
	for i := range out {
		out[i].Current = out[i].ID == s.current
	}
	return out
}
func (s *fakeStore) Get(id string) (model.Subscription, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.subs {
		if x.ID == id {
			x.Current = id == s.current
			return x, true
		}
	}
	return model.Subscription{}, false
}
func (s *fakeStore) Current() (model.Subscription, bool) {
	s.mu.Lock()
	cur := s.current
	s.mu.Unlock()
	if cur == "" {
		return model.Subscription{}, false
	}
	return s.Get(cur)
}
func (s *fakeStore) SetCurrent(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = id
	return nil
}
func (s *fakeStore) Put(sub model.Subscription) (model.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub.ID == "" {
		s.nextID++
		sub.ID = fmt.Sprintf("s%d", s.nextID)
		s.subs = append(s.subs, sub)
		return sub, nil
	}
	for i := range s.subs {
		if s.subs[i].ID == sub.ID {
			s.subs[i] = sub
			return sub, nil
		}
	}
	s.subs = append(s.subs, sub)
	return sub, nil
}
func (s *fakeStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.subs {
		if s.subs[i].ID == id {
			s.subs = append(s.subs[:i], s.subs[i+1:]...)
			break
		}
	}
	delete(s.content, id)
	s.removeOverride(id)
	return nil
}

// removeOverride drops the entry id and its selections; caller holds mu.
func (s *fakeStore) removeOverride(id string) {
	for i := range s.ovs {
		if s.ovs[i].ID == id {
			s.ovs = append(s.ovs[:i], s.ovs[i+1:]...)
			break
		}
	}
	for i := range s.subs {
		var keep []string
		for _, x := range s.subs[i].Overrides {
			if x != id {
				keep = append(keep, x)
			}
		}
		s.subs[i].Overrides = keep
	}
}
func (s *fakeStore) Content(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.content[id]
	if !ok {
		return nil, errors.New("no content")
	}
	return d, nil
}
func (s *fakeStore) SetContent(id string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.content[id] = data
	return nil
}
func (s *fakeStore) Override(key string) string {
	o, ok := s.GetOverride(key)
	if !ok {
		return ""
	}
	return o.Text
}
func (s *fakeStore) SetOverride(key, text string) error {
	if text == "" {
		return s.DeleteOverride(key)
	}
	o, ok := s.GetOverride(key)
	if !ok {
		o = model.Override{ID: key, Name: key, Enabled: true, Global: key == "global", Source: model.OverrideLocal}
	}
	o.Text = text
	if _, err := s.PutOverride(o); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.subs {
		if s.subs[i].ID == key && !slices.Contains(s.subs[i].Overrides, key) {
			s.subs[i].Overrides = append(s.subs[i].Overrides, key)
		}
	}
	return nil
}
func (s *fakeStore) Overrides() []model.Override {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.Override(nil), s.ovs...)
}
func (s *fakeStore) GetOverride(id string) (model.Override, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.ovs {
		if o.ID == id {
			return o, true
		}
	}
	return model.Override{}, false
}
func (s *fakeStore) PutOverride(o model.Override) (model.Override, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.ID == "" {
		s.nextID++
		o.ID = fmt.Sprintf("o%d", s.nextID)
		s.ovs = append(s.ovs, o)
		return o, nil
	}
	for i := range s.ovs {
		if s.ovs[i].ID == o.ID {
			s.ovs[i] = o
			return o, nil
		}
	}
	s.ovs = append(s.ovs, o)
	return o, nil
}
func (s *fakeStore) DeleteOverride(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeOverride(id)
	return nil
}
func (s *fakeStore) MoveOverride(id string, delta int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.ovs {
		if s.ovs[i].ID != id {
			continue
		}
		j := min(max(i+delta, 0), len(s.ovs)-1)
		o := s.ovs[i]
		s.ovs = append(s.ovs[:i], s.ovs[i+1:]...)
		s.ovs = append(s.ovs[:j], append([]model.Override{o}, s.ovs[j:]...)...)
		return nil
	}
	return errors.New("no such override")
}
func (s *fakeStore) CustomRules() []model.CustomRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.CustomRule(nil), s.rules...)
}
func (s *fakeStore) SetCustomRules(r []model.CustomRule) ([]model.CustomRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]model.CustomRule(nil), r...)
	for i := range out {
		if out[i].ID == "" {
			s.nextID++
			out[i].ID = fmt.Sprintf("r%d", s.nextID)
		}
	}
	s.rules = out
	return append([]model.CustomRule(nil), out...), nil
}

// ---- composer ----

type fakeProf struct {
	mu       sync.Mutex
	fetched  profile.Fetched
	fetchErr error
	inputs   []profile.Input
}

func (p *fakeProf) Compose(in profile.Input) (profile.Output, error) {
	p.mu.Lock()
	p.inputs = append(p.inputs, in)
	p.mu.Unlock()
	if strings.Contains(in.GlobalOverride, "BAD") || strings.Contains(in.SubOverride, "BAD") {
		return profile.Output{}, errors.New("覆写 YAML 有误")
	}
	var layers strings.Builder
	for _, l := range in.Overrides {
		if strings.Contains(l.Text, "BAD") {
			return profile.Output{}, errors.New(l.Name + "覆写 YAML 有误")
		}
		fmt.Fprintf(&layers, "# %s\n%s", l.Name, l.Text)
	}
	y := fmt.Sprintf("# sub\n%s# global\n%s%s# port %d\n", in.Subscription, in.GlobalOverride, layers.String(), in.Managed.MixedPort)
	var lines []model.RuntimeLine
	for _, l := range strings.Split(strings.TrimSuffix(y, "\n"), "\n") {
		lines = append(lines, model.RuntimeLine{Text: l, Source: "订阅"})
	}
	return profile.Output{YAML: []byte(y), Lines: lines}, nil
}
func (p *fakeProf) Diff(a, b []byte) []model.DiffLine {
	return []model.DiffLine{{Op: '-', Text: string(a)}, {Op: '+', Text: string(b)}}
}
func (p *fakeProf) Fetch(context.Context, *http.Client, string, string) (profile.Fetched, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fetched, p.fetchErr
}
func (p *fakeProf) CheckYAML(data []byte) error {
	if !strings.Contains(string(data), "proxies") {
		return errors.New("不是 Clash/Mihomo 格式的订阅")
	}
	return nil
}

// ---- harness ----

type harness struct {
	s      *Service
	ctrl   *fakeCtrl
	mgr    *fakeMgr
	core   *fakeCore
	sys    *fakeSys
	dns    *fakeDNS
	helper *fakeHelper
	loc    *fakeLoc
	store  *fakeStore
	prof   *fakeProf
	dir    string
	taken  map[int]bool // ports another program listens on
	tunOf  string       // another client's TUN interface, "" for none
	vpn    *fakeVPN
}

// fakeVPN is the aTrust detector.
type fakeVPN struct {
	mu    sync.Mutex
	info  atrust.Info
	calls int
}

func (f *fakeVPN) set(i atrust.Info) { f.mu.Lock(); f.info = i; f.mu.Unlock() }
func (f *fakeVPN) detect(context.Context, func(string) bool) atrust.Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.info
}

// newHarness builds a service on fakes with one stored subscription.
func newHarness(t testingT, prepare func(h *harness)) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{
		ctrl: newFakeCtrl(), mgr: newFakeMgr(), core: &fakeCore{bin: "/fake/mihomo"},
		sys: &fakeSys{}, dns: &fakeDNS{}, helper: &fakeHelper{}, loc: &fakeLoc{}, store: newFakeStore(), prof: &fakeProf{}, dir: dir, taken: map[int]bool{}, vpn: &fakeVPN{},
	}
	sub, _ := h.store.Put(model.Subscription{Name: "测试机场", Source: "url", URL: "https://example.com/sub"})
	_ = h.store.SetContent(sub.ID, []byte("proxies: []\n"))
	_ = h.store.SetCurrent(sub.ID)
	h.ctrl.proxies = sampleProxies()
	if prepare != nil {
		prepare(h)
	}
	d := deps{
		ctrl: h.ctrl, mgr: h.mgr, core: h.core, sys: h.sys, dns: h.dns, helper: h.helper, loc: h.loc, store: h.store, prof: h.prof,
		netinfo:    func(context.Context) (model.NetInfo, error) { return model.NetInfo{LANIP: "192.168.1.23"}, nil },
		httpDirect: func(time.Duration) *http.Client { return &http.Client{} },
		httpVia:    func(int, time.Duration) *http.Client { return &http.Client{} },
		freePort:   func() (int, error) { return 40000, nil },
		portInUse:  func(p int, _ bool) bool { return h.taken[p] },
		otherTUN:   func(context.Context) (string, bool) { return h.tunOf, h.tunOf != "" },
		vpnDetect:  h.vpn.detect,
		diagCommands: func(context.Context) map[string]string {
			return map[string]string{"routes.txt": "default 192.168.1.1 en0\nfetched https://sub.example.com/link/abc?token=zzz9"}
		},
		now: time.Now,
	}
	s, err := newServiceWith(Options{DataDir: dir, LogDir: dir, CacheDir: dir, ResourceDir: dir}, d)
	if err != nil {
		t.Fatalf("newServiceWith: %v", err)
	}
	h.s = s
	t.Cleanup(s.Shutdown)
	return h
}

// testingT is the subset of *testing.T the harness needs.
type testingT interface {
	Helper()
	TempDir() string
	Fatalf(format string, args ...any)
	Cleanup(func())
}

func sampleProxies() map[string]controller.Proxy {
	nodes := []string{"香港 01", "东京 03", "美国 05"}
	m := map[string]controller.Proxy{
		"DIRECT": {Name: "DIRECT", Type: "Direct"},
		"REJECT": {Name: "REJECT", Type: "Reject"},
	}
	for i, n := range nodes {
		m[n] = controller.Proxy{Name: n, Type: "Shadowsocks", UDP: true, History: []controller.DelayHistory{{Delay: 100 + i}}}
	}
	m["美国 05"] = controller.Proxy{Name: "美国 05", Type: "Trojan", History: []controller.DelayHistory{{Delay: 0}}}
	m["自动选择"] = controller.Proxy{Name: "自动选择", Type: "URLTest", Now: "香港 01", All: nodes}
	m["节点选择"] = controller.Proxy{Name: "节点选择", Type: "Selector", Now: "东京 03", All: append([]string{"自动选择"}, nodes...)}
	m["流媒体"] = controller.Proxy{Name: "流媒体", Type: "Selector", Now: "节点选择", All: []string{"节点选择", "香港 01"}}
	m[probeGroup] = controller.Proxy{Name: probeGroup, Type: "Selector", Hidden: true, Now: "DIRECT", All: append([]string{"DIRECT"}, nodes...)}
	m["GLOBAL"] = controller.Proxy{Name: "GLOBAL", Type: "Selector", Now: "节点选择",
		All: []string{"DIRECT", "REJECT", "香港 01", "东京 03", "美国 05", "自动选择", "节点选择", "流媒体", probeGroup}}
	return m
}

// waitFor polls cond for up to 2 s.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
