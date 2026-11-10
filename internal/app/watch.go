package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"lanflow/internal/core"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/platform/sysdns"
	"lanflow/internal/platform/sysproxy"
)

// Sleep/wake and network changes (docs/design.md §9 「睡眠唤醒、网络切换」):
// after a wake or a network change the service checks that the core still
// answers and that the system proxy it set still points at it, re-reads the
// network information, looks the public IP up again and re-measures the
// main exit. Detection is portable and cheap: a ticker that notices a jump
// of the wall clock (the machine slept, or the process was paused) and a
// fingerprint of the network interfaces (stdlib, no commands) that must
// stay changed for two ticks before it counts, so DHCP churn is ignored.
// main.go additionally feeds the OS's own events in (Resumed,
// NetworkChanged); both paths meet in systemChanged, which debounces.

//
// Every recheck writes one line to the app log saying what triggered it
// (a wake, the OS's network event, a change of the interfaces), what
// actually changed (interfaces, DNS servers, the gateway or the default
// interface), what was repaired, and how long it took. Events that come
// within recheckDebounce of each other make one recheck (at most
// recheckMaxDelay after the first). A network event after which nothing
// changed — typically Lanflow's own system DNS, system proxy or TUN change,
// which the OS reports as a network change too — is skipped; changes that
// Lanflow itself made shortly before (selfNotes) do not count. A recheck
// that found nothing changed and did nothing is logged at debug level;
// changes, repairs and failures at info level or above. The core is only
// restarted when it has exited or does not answer.

const (
	watchInterval   = 5 * time.Second  // how often the watcher looks
	wakeGap         = 30 * time.Second // a tick this late (beyond the interval) means a wake
	recheckDebounce = 2 * time.Second  // wake and network change usually come together
	recheckMaxDelay = 10 * time.Second // a storm of events still gets a recheck this soon
	selfChangeQuiet = 30 * time.Second // how long a change Lanflow made explains a network event
	pingTimeout     = 5 * time.Second
)

// Trigger sources of a recheck.
const (
	srcWake       = "wake"       // the OS's resume event, or the clock jumped
	srcNetwork    = "network"    // the OS's network event (mygo Network.OnChanged)
	srcInterfaces = "interfaces" // sysWatcher saw the interfaces change
)

// Kinds of changes Lanflow makes itself (selfNotes).
const (
	selfDNS   = "system DNS"
	selfProxy = "system proxy"
	selfCore  = "core start/stop" // a TUN core adds and removes routes
)

// sysWatcher turns ticks into wake and network-change callbacks. It is
// not safe for concurrent use; the owner calls tick from one goroutine.
type sysWatcher struct {
	now         func() time.Time
	fingerprint func() string
	interval    time.Duration
	onWake      func(slept time.Duration)
	onNetChange func()

	last    time.Time
	fp      string
	pending string // a changed fingerprint waiting for its second tick
}

// tick looks at the clock and the network once.
func (w *sysWatcher) tick() {
	now := w.now()
	if gap := now.Sub(w.last); !w.last.IsZero() && gap > w.interval+wakeGap && w.onWake != nil {
		w.onWake(gap)
	}
	w.last = now

	fp := w.fingerprint()
	switch {
	case fp == w.fp:
		w.pending = ""
	case w.fp == "":
		w.fp = fp // first look
	case fp == w.pending:
		w.fp, w.pending = fp, ""
		if w.onNetChange != nil {
			w.onNetChange()
		}
	default:
		w.pending = fp
	}
}

// ifaceAddrs maps the interfaces that are up to their sorted addresses, so
// a new Wi-Fi network, a cable or a VPN shows as a change. Tunnel
// interfaces of the core itself (utun on macOS, wintun on Windows) come
// and go with the core and are left out.
func ifaceAddrs() map[string]string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(i.Name)
		if strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "tun") || strings.Contains(name, "wintun") || strings.HasPrefix(name, "mihomo") {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		var as []string
		for _, a := range addrs {
			as = append(as, a.String())
		}
		sort.Strings(as)
		out[i.Name] = strings.Join(as, ",")
	}
	return out
}

// ifaceFingerprint is ifaceAddrs as one comparable string.
func ifaceFingerprint() string { return fingerprintOf(ifaceAddrs()) }

func fingerprintOf(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for name, as := range m {
		parts = append(parts, name+"="+as)
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// netSnap is what a recheck compares: the interfaces, and from the network
// information the default interface, the gateway and the DNS servers.
type netSnap struct {
	at      time.Time
	ifaces  map[string]string
	iface   string // default interface, e.g. "Wi-Fi (en0)"
	gateway string
	dns     string // servers, comma-joined
}

// netChange is one difference between two snapshots.
type netChange struct {
	kind string // "interface", "route" or "dns"
	text string
}

// diffNet lists what changed from a to b.
func diffNet(a, b netSnap) []netChange {
	var out []netChange
	names := map[string]bool{}
	for n := range a.ifaces {
		names[n] = true
	}
	for n := range b.ifaces {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		old, had := a.ifaces[n]
		cur, has := b.ifaces[n]
		switch {
		case had && !has:
			out = append(out, netChange{"interface", n + " down"})
		case !had && has:
			out = append(out, netChange{"interface", fmt.Sprintf("%s up (%s)", n, cur)})
		case old != cur:
			out = append(out, netChange{"interface", fmt.Sprintf("%s %s → %s", n, old, cur)})
		}
	}
	if a.iface != b.iface {
		out = append(out, netChange{"route", fmt.Sprintf("default interface %q → %q", a.iface, b.iface)})
	}
	if a.gateway != b.gateway {
		out = append(out, netChange{"route", fmt.Sprintf("gateway %q → %q", a.gateway, b.gateway)})
	}
	if a.dns != b.dns {
		out = append(out, netChange{"dns", fmt.Sprintf("DNS %q → %q", a.dns, b.dns)})
	}
	return out
}

// watchState is the recheck bookkeeping of the Service.
type watchState struct {
	mu       sync.Mutex
	pending  []string  // trigger descriptions waiting for the debounce
	first    time.Time // when the first of them came
	base     netSnap   // the network as of the last recheck (zero: unknown)
	selfMu   sync.Mutex
	selfAt   map[string]time.Time // when Lanflow last changed each kind itself
	runMu    sync.Mutex           // one recheck at a time
	ifaceSrc func() map[string]string
	clock    func() time.Time // the Service's d.now, read at each call
}

// noteSelf records that Lanflow itself is changing kind now, so the
// network event the OS sends for it does not trigger a recheck.
func (w *watchState) noteSelf(kind string, now time.Time) {
	w.selfMu.Lock()
	if w.selfAt == nil {
		w.selfAt = map[string]time.Time{}
	}
	w.selfAt[kind] = now
	w.selfMu.Unlock()
}

// selfSince lists the kinds Lanflow changed after t (and within
// selfChangeQuiet of now).
func (w *watchState) selfSince(t, now time.Time) map[string]bool {
	w.selfMu.Lock()
	defer w.selfMu.Unlock()
	out := map[string]bool{}
	for k, at := range w.selfAt {
		if !at.Before(t.Add(-time.Second)) && now.Sub(at) < selfChangeQuiet {
			out[k] = true
		}
	}
	return out
}

// watchSystem runs the watcher until shutdown.
func (s *Service) watchSystem() {
	w := &sysWatcher{
		now:         s.d.now,
		fingerprint: ifaceFingerprint,
		interval:    watchInterval,
		onWake: func(slept time.Duration) {
			s.systemChanged(srcWake, "clock jumped "+slept.Round(time.Second).String())
		},
		onNetChange: func() { s.systemChanged(srcInterfaces, "") },
	}
	w.tick()
	s.takeNetBaseline()
	t := time.NewTicker(watchInterval)
	defer t.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-t.C:
			w.tick()
			s.feat2Tick() // Wi-Fi subscription rules, automatic WebDAV backup (feat2state.go)
		}
	}
}

// Resumed tells the service the system woke up (main.go wires the OS event).
func (s *Service) Resumed() { s.systemChanged(srcWake, "OS resume event") }

// NetworkChanged tells the service the network connection changed.
func (s *Service) NetworkChanged() { s.systemChanged(srcNetwork, "OS network event") }

// systemChanged schedules one recheck shortly, however many events arrive:
// each event moves it recheckDebounce later, but never more than
// recheckMaxDelay after the first one. All the events are named in the log.
func (s *Service) systemChanged(source, detail string) {
	if s.rootCtx.Err() != nil {
		return
	}
	desc := source
	if detail != "" {
		desc += " (" + detail + ")"
	}
	now := s.d.now()
	s.nw.mu.Lock()
	if len(s.nw.pending) == 0 {
		s.nw.first = now
	}
	if !slices.Contains(s.nw.pending, desc) {
		s.nw.pending = append(s.nw.pending, desc)
	}
	delay := recheckDelay(s.nw.first, now)
	s.nw.mu.Unlock()

	s.recheckMu.Lock()
	defer s.recheckMu.Unlock()
	if s.recheckTimer != nil {
		s.recheckTimer.Stop()
	}
	s.recheckTimer = s.afterFunc(delay, s.runPendingRecheck)
}

// recheckDelay is how long after now the recheck runs when the first of
// the pending events came at first.
func recheckDelay(first, now time.Time) time.Duration {
	if left := recheckMaxDelay - now.Sub(first); left < recheckDebounce {
		return max(left, 0)
	}
	return recheckDebounce
}

// runPendingRecheck takes the events gathered by systemChanged and runs
// one recheck for them.
func (s *Service) runPendingRecheck() {
	s.nw.mu.Lock()
	triggers := s.nw.pending
	s.nw.pending = nil
	s.nw.mu.Unlock()
	if len(triggers) == 0 {
		return
	}
	s.recheckFor(triggers)
}

// snapNet reads the interfaces and the network information now.
func (s *Service) snapNet() (netSnap, model.NetInfo, error) {
	ctx, cancel := s.ctrlCtx(nil, 5*time.Second)
	info, err := s.d.netinfo(ctx)
	cancel()
	src := s.nw.ifaceSrc
	if src == nil {
		src = ifaceAddrs
	}
	return netSnap{at: s.d.now(), ifaces: src(), iface: info.Interface, gateway: info.Gateway, dns: strings.Join(info.DNS, ",")}, info, err
}

// takeNetBaseline records the network as it is, for the next recheck to
// compare with.
func (s *Service) takeNetBaseline() {
	snap, _, err := s.snapNet()
	if err != nil {
		return
	}
	s.nw.mu.Lock()
	s.nw.base = snap
	s.nw.mu.Unlock()
}

// recheckResult is what a recheck did, for its log line.
type recheckResult struct {
	actions []string // repairs made
	failed  []string // what went wrong
	warned  []string // problems reported to the user
}

// recheckFor is the recheck after the events in triggers.
func (s *Service) recheckFor(triggers []string) {
	s.nw.runMu.Lock()
	defer s.nw.runMu.Unlock()
	start := time.Now()
	wake := false
	for _, t := range triggers {
		wake = wake || strings.HasPrefix(t, srcWake)
	}

	snap, info, infoErr := s.snapNet()
	s.nw.mu.Lock()
	base := s.nw.base
	if infoErr == nil {
		s.nw.base = snap
	}
	s.nw.mu.Unlock()

	var changes, own []string
	known := !base.at.IsZero()
	if known && infoErr == nil {
		self := s.nw.selfSince(base.at, snap.at)
		for _, c := range diffNet(base, snap) {
			switch {
			case c.kind == "dns" && self[selfDNS], c.kind == "route" && self[selfCore]:
				own = append(own, c.text)
			default:
				changes = append(changes, c.text)
			}
		}
	}
	trig := strings.Join(triggers, ", ")

	// Nothing changed after a network event: the OS reported Lanflow's own
	// DNS, proxy or TUN change, or a change that came and went.
	if known && infoErr == nil && !wake && len(changes) == 0 {
		why := "no change in interfaces, DNS or routes"
		if self := s.nw.selfSince(base.at, snap.at); len(self) > 0 {
			why += " besides Lanflow's own changes (" + strings.Join(sortedKeys(self), ", ") + ")"
		}
		s.alog.Debugf("network recheck skipped after %s: %s (%s)", trig, why, time.Since(start).Round(time.Millisecond))
		return
	}

	res := s.recheckWith(info, infoErr, !known || len(changes) > 0 || infoErr != nil)

	var b strings.Builder
	fmt.Fprintf(&b, "network recheck after %s", trig)
	switch {
	case !known:
		b.WriteString("; changes: unknown (no earlier snapshot)")
	case infoErr != nil:
		fmt.Fprintf(&b, "; network information unavailable: %v", infoErr)
	case len(changes) > 0:
		b.WriteString("; changed: " + strings.Join(changes, "; "))
	default:
		b.WriteString("; nothing changed")
	}
	if len(own) > 0 {
		b.WriteString("; Lanflow's own: " + strings.Join(own, "; "))
	}
	if len(res.actions) > 0 {
		b.WriteString("; repaired: " + strings.Join(res.actions, "; "))
	} else {
		b.WriteString("; no repair needed")
	}
	if len(res.warned) > 0 {
		b.WriteString("; reported: " + strings.Join(res.warned, "; "))
	}
	if len(res.failed) > 0 {
		b.WriteString("; failed: " + strings.Join(res.failed, "; "))
	}
	fmt.Fprintf(&b, " (%s)", time.Since(start).Round(time.Millisecond))
	switch {
	case len(res.failed) > 0:
		s.alog.Errorf("%s", b.String())
	case len(res.warned) > 0:
		s.alog.Warnf("%s", b.String())
	case len(res.actions) > 0 || len(changes) > 0 || !known || infoErr != nil:
		s.alog.Infof("%s", b.String())
	default:
		s.alog.Debugf("%s", b.String())
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// recheck is a full recheck (network information re-read, public IP and
// main exit looked up again) without the log line; tests call it.
func (s *Service) recheck() {
	ctx, cancel := s.ctrlCtx(nil, 5*time.Second)
	info, err := s.d.netinfo(ctx)
	cancel()
	s.recheckWith(info, err, true)
}

// recheckWith is the work after a wake or a network change: info is the
// network information just read (infoErr its error). It runs off the UI
// thread and never holds mu across I/O. With netChanged the public IP and
// the main exit are looked up again; the core is restarted only when it
// has exited or does not answer.
func (s *Service) recheckWith(info model.NetInfo, infoErr error, netChanged bool) recheckResult {
	var res recheckResult
	s.mu.Lock()
	running := s.state.st.Core == model.CoreRunning
	ours := s.state.weSetProxy
	port := s.state.settings.MixedPort
	s.mu.Unlock()

	// 1. The core: gone while asleep, or alive but not answering.
	if running {
		switch {
		case !s.d.mgr.Running():
			s.handleCrash(errors.New(i18n.T("系统唤醒后发现内核已退出")))
			if s.Status().Core == model.CoreRunning {
				res.actions = append(res.actions, "core had exited, restarted")
			} else {
				res.failed = append(res.failed, "core had exited and is not running")
			}
		case !s.corePings():
			s.notify(Notice{Kind: "error", Title: i18n.T("内核无响应，正在重新启动"), Detail: i18n.T("系统唤醒或网络切换后内核没有应答，澜流会重新启动它。")})
			if err := s.RestartCore(); err != nil {
				s.notify(Notice{Kind: "error", Title: i18n.T("重新启动内核失败"), Detail: err.Error()})
				res.failed = append(res.failed, "core did not answer; restart failed: "+err.Error())
			} else {
				res.actions = append(res.actions, "core did not answer, restarted")
			}
		}
	}

	// 2. The system proxy we set: still ours? The design asks to check,
	// not to fight another app over it, so a foreign change is reported
	// and our ownership dropped.
	if ours {
		m, _ := s.readMarker()
		if cur, err := s.d.sys.Get(); err == nil && !proxyIsOurs(cur, m, port) {
			s.mu.Lock()
			s.state.weSetProxy = false
			s.state.st.SystemProxy = false
			s.mu.Unlock()
			s.removeMarker()
			s.em.emit(TopicStatus)
			s.notify(Notice{Kind: "error", Title: i18n.T("系统代理已被其他程序修改"), Detail: i18n.T("系统代理不再指向澜流。需要时请在「概览」中重新打开系统代理。")})
			res.warned = append(res.warned, "system proxy changed by another app")
		}
	}

	// 3. Network information; after a change also the public IP and the
	// main exit.
	s.storeNetInfo(info, infoErr)
	s.wifiCheck() // a new Wi-Fi network may name another subscription (wifi.go)
	if netChanged {
		s.refreshYou()
		s.rebuildRoutes()
		s.scheduleMainProbe()
	}
	return res
}

// corePings reports whether the controller answers, trying twice.
func (s *Service) corePings() bool {
	for i := 0; i < 2; i++ {
		ctx, cancel := s.ctrlCtx(nil, pingTimeout)
		_, err := s.d.ctrl.Version(ctx)
		cancel()
		if err == nil {
			return true
		}
		if s.rootCtx.Err() != nil {
			return true // shutting down: nothing to restart
		}
		time.Sleep(time.Second)
	}
	return false
}

// Wrappers that note Lanflow's own changes of the system DNS, the system
// proxy and the core (whose TUN changes routes) for watchState.selfSince.

type notingSysProxy struct {
	sysProxy
	note func(kind string)
}

func (p notingSysProxy) Set(host string, port int, bypass []string) error {
	p.note(selfProxy)
	return p.sysProxy.Set(host, port, bypass)
}
func (p notingSysProxy) Clear() error { p.note(selfProxy); return p.sysProxy.Clear() }
func (p notingSysProxy) SetPAC(url string) error {
	p.note(selfProxy)
	return p.sysProxy.SetPAC(url)
}
func (p notingSysProxy) Restore(saved sysproxy.Saved) error {
	p.note(selfProxy)
	return p.sysProxy.Restore(saved)
}

type notingSysDNS struct {
	sysDNS
	note func(kind string)
}

func (d notingSysDNS) Set(servers []string) error { d.note(selfDNS); return d.sysDNS.Set(servers) }
func (d notingSysDNS) Restore(saved sysdns.Saved, ours []string) error {
	d.note(selfDNS)
	return d.sysDNS.Restore(saved, ours)
}

type notingManager struct {
	coreManager
	note func(kind string)
}

func (m notingManager) Start(ctx context.Context, spec core.Spec) error {
	m.note(selfCore)
	defer m.note(selfCore) // the TUN routes appear once it runs
	return m.coreManager.Start(ctx, spec)
}
func (m notingManager) Stop(ctx context.Context) error {
	m.note(selfCore)
	defer m.note(selfCore)
	return m.coreManager.Stop(ctx)
}

// noteSelfChanges wraps d so Lanflow's own system changes are noted in w.
func noteSelfChanges(d *deps, w *watchState) {
	note := func(kind string) {
		now := time.Now()
		if w.clock != nil {
			now = w.clock()
		}
		w.noteSelf(kind, now)
	}
	if d.sys != nil {
		d.sys = notingSysProxy{d.sys, note}
	}
	if d.dns != nil {
		d.dns = notingSysDNS{d.dns, note}
	}
	if d.mgr != nil {
		d.mgr = notingManager{d.mgr, note}
	}
}
