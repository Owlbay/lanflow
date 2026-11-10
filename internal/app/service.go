package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"lanflow/internal/applog"
	"lanflow/internal/controller"
	"lanflow/internal/core"
	"lanflow/internal/i18n"
	"lanflow/internal/locate"
	"lanflow/internal/model"
	"lanflow/internal/platform/atrust"
	"lanflow/internal/platform/helper"
	"lanflow/internal/profile"
)

// Service is the real implementation of API.
//
// Locking: mu guards every field of state and is never held across I/O
// (process start, controller calls, disk, network). opMu serializes the
// actions that change the core's configuration (start, stop, reload,
// subscription switch) and is held across their I/O; it is never taken
// while mu is held.
type Service struct {
	opt Options
	d   deps

	ctrlAddr string           // controller socket / pipe
	workDir  string           // <DataDir>/core
	runtime  string           // <DataDir>/core/runtime.yaml
	logFile  string           // <LogDir>/mihomo.log
	alog     *applog.Logger   // <LogDir>/lanflow.log (applog.go)
	elog     *applog.ErrorLog // <LogDir>/errors.jsonl (feedback.go)
	pac      pacServer        // PAC file server for the PAC system-proxy mode (pac.go)

	rootCtx    context.Context
	rootCancel context.CancelFunc
	wg         sync.WaitGroup // background goroutines (schedulers, streams)
	shutdown   sync.Once

	opMu sync.Mutex

	// settingsMu serializes settings.json writes with the snapshot they
	// write (persistSettings); taken before mu, never while mu is held.
	settingsMu sync.Mutex

	mu    sync.Mutex
	state state
	em    *emitter

	// streams of the running core (guarded by mu)
	streamCtx    context.Context // parent of every stream; nil while stopped
	streamCancel context.CancelFunc
	logsCancel   context.CancelFunc
	logsDone     chan struct{} // closed when the current log stream returned
	connsCancel  context.CancelFunc
	connsDone    chan struct{} // closed when the current connections stream returned
	streamWG     sync.WaitGroup
	background   atomic.Bool // the window is out of sight (SetBackground, streams.go)

	probeMu    sync.Mutex // debounces the automatic main-exit probe
	selMu      sync.Mutex // guards selections (selections.go)
	selections map[string]map[string]string
	probeTimer *time.Timer

	recheckMu    sync.Mutex // debounces the wake / network-change recheck (watch.go)
	recheckTimer *time.Timer
	nw           *watchState // what the recheck compares and Lanflow's own changes (watch.go)

	probeExitMu sync.Mutex // one 检测 probe uses the probe group at a time (probe.go)

	f2 feat2State // Wi-Fi rules and WebDAV backup (feat2state.go); its own lock

	// bgMu guards closing against wg.Add: once Shutdown sets closing, no
	// new background goroutine or timer callback joins wg (Add racing Wait).
	bgMu    sync.Mutex
	closing bool

	stats   *statsStore   // 流量统计 (stats.go); its own lock
	cores   coresState    // installed Mihomo versions and release lists (cores.go); its own lock
	started time.Time     // when the service was created
	fb      feedbackState // reports built for 反馈问题 and the automatic report (feedback.go); its own lock
}

// state is everything the reads return. Every slice/map handed out is copied.
type state struct {
	st       model.Status
	settings model.Settings
	up, down []int64
	totalUp  int64 // last connections snapshot totals
	totalDn  int64

	proxies     model.ProxySnapshot
	rawProxy    map[string]controller.Proxy
	testing     map[string]bool
	snapPending bool // delay results wait for flushSnapshot (proxies.go)

	pprovs    []model.ProxyProvider // proxy-providers, sorted by name (proxies.go)
	pprovBusy map[string]string     // provider → "update" | "check" while an action runs

	subs         []model.Subscription
	subsUpdating map[string]bool

	conns   []model.Connection
	closed  ring[model.Connection] // the newest closedRing closed connections
	tracker connTracker

	logs ring[model.LogEntry] // the newest logRing core log lines

	rules        []model.Rule
	rulesAt      time.Time // when rules were fetched (refreshed on demand, rules.go)
	rulesBusy    bool      // a refreshRules runs
	custom       []model.CustomRule
	provs        []model.RuleProvider
	provUpdating map[string]bool

	exits     map[string]model.ExitInfo // by node
	exitEntry map[string]string         // node → entry IP last analysed
	routes    []model.ExitRoute
	you       *model.EndpointInfo

	net   model.NetInfo
	sites []model.SiteCheck
	leak  model.LeakResult // 泄漏检查 (leak.go)
	probe probeState       // 检测 page (probe.go)

	// effective DNS / sniffer sections (effective.go): what the current
	// subscription's config says after the overrides, refreshed with the
	// store caches; Lanflow's own settings take over at read time.
	effDNS model.EffectiveDNS
	effSnf model.EffectiveSniffer

	effKey [sha256.Size]byte // what effDNS / effSnf were parsed from (effective.go)

	geoInfo   model.GeoDataInfo // geo file times (geoinfo.go), re-read when geoInfoAt is stale
	geoInfoAt time.Time

	overrides    model.Overrides
	runtimeYAML  []byte
	runtimeDNS   runtimeDNS // the dns keys of runtimeYAML, parsed once per commit (network.go)
	runtimeLines []model.RuntimeLine
	groupIcons   map[string]string // proxy-group name → icon reference of the runtime config (icons.go)
	icons        *iconCache        // decoded / downloaded icons (icons.go), allocated lazily
	procIcons    *procIconCache    // application icons by process path (procicon.go), allocated lazily

	notices []Notice

	coreBin       string // resolved binary ("" when missing)
	probePort     int
	weSetProxy    bool        // Lanflow pointed the system proxy at itself
	tunRunner     bool        // the manager currently uses the helper runner
	helperOK      bool        // helper.Check passed at last check
	helperStale   bool        // last Check: installed helper of another version/protocol
	runningTUN    bool        // the running config has TUN enabled
	coexist       atrust.Info // aTrust found beside Lanflow (coexist.go)
	lastAutoStart time.Time
	autoFail      map[string]autoUpdateFailure // subscription ID → failed auto updates (subs.go)
	youDone       bool                         // You() fetched (or being fetched) for this start
	youBusy       bool                         // a fetchYou loop is running
	youGen        int                          // bumps to stop an older fetchYou loop
	hintDone      bool                         // the system-proxy hint bubble was dismissed or the proxy was turned on once (hintFile)
	hintSaving    bool                         // hintFile write in flight
}

var _ API = (*Service)(nil)

// NewService wires the real packages and returns a ready service. It does
// not start the core; that happens in the background (auto system proxy,
// or a current subscription exists) once the service is created.
func NewService(o Options) (*Service, error) {
	for _, dir := range []string{o.DataDir, o.LogDir, o.CacheDir, filepath.Join(o.DataDir, "core")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf(i18n.T("无法创建数据目录 %s：%v"), dir, err)
		}
	}
	store, err := profile.Open(o.DataDir)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("无法打开订阅存储：%v"), err)
	}
	addr := core.ControllerAddress()
	ctrl := controller.New(func(ctx context.Context) (net.Conn, error) { return core.Dial(ctx, addr) })
	mgr := core.NewManager(core.Options{
		Runner: core.NewExecRunner(),
		Ready: func(ctx context.Context) error {
			_, err := ctrl.Version(ctx)
			return err
		},
	})
	workDir := filepath.Join(o.DataDir, "core")
	copyBundledGeo(filepath.Join(o.ResourceDir, "geo"), workDir)
	loc := locate.New(filepath.Join(o.CacheDir, "exits.json"),
		filepath.Join(workDir, "geoip.metadb"), filepath.Join(workDir, "GeoLite2-ASN.mmdb"))

	s, err := newServiceWith(o, realDeps(ctrl, mgr, store, loc))
	if err != nil {
		return nil, err
	}
	s.ctrlAddr = addr
	s.run()
	return s, nil
}

// newServiceWith builds a service from explicit dependencies (tests pass
// fakes). It loads settings, resolves the core binary and detects a
// leftover system proxy, but starts no goroutines; see run.
func newServiceWith(o Options, d deps) (*Service, error) {
	if d.now == nil {
		d.now = time.Now
	}
	if d.freePort == nil {
		d.freePort = freeTCPPort
	}
	if d.portInUse == nil {
		d.portInUse = tcpPortInUse
	}
	if d.otherTUN == nil {
		d.otherTUN = func(context.Context) (string, bool) { return "", false }
	}
	if d.appDNS == nil {
		d.appDNS = lookupVia
	}
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf(i18n.T("无法创建数据目录：%v"), err)
	}
	_, statErr := os.Stat(filepath.Join(o.DataDir, settingsFile))
	set, loadErr := loadSettings(o.DataDir)
	if os.IsNotExist(statErr) && o.Language != "" {
		set.Language = i18n.Normalize(o.Language)
	}
	i18n.SetLang(set.Language)
	if os.IsNotExist(statErr) && d.portInUse(set.MixedPort, set.AllowLAN) {
		// First run next to another proxy client that holds 7890: take the
		// next free port instead of silently sharing theirs.
		for p := set.MixedPort + 1; p < set.MixedPort+100; p++ {
			if !d.portInUse(p, set.AllowLAN) {
				set.MixedPort = p
				_ = saveSettings(o.DataDir, set)
				break
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	nw := &watchState{}
	noteSelfChanges(&d, nw)
	s := &Service{
		nw:         nw,
		opt:        o,
		d:          d,
		workDir:    filepath.Join(o.DataDir, "core"),
		runtime:    filepath.Join(o.DataDir, "core", "runtime.yaml"),
		logFile:    filepath.Join(o.LogDir, "mihomo.log"),
		rootCtx:    ctx,
		rootCancel: cancel,
		em:         newEmitter(),
	}
	nw.clock = func() time.Time { return s.d.now() }
	s.alog, _ = applog.Open(o.LogDir) // a failure keeps the log in memory only
	s.alog.OnWrite(func() { s.em.emit(TopicLogs) })
	s.alog.Infof("Lanflow starting (%s/%s), data %s", runtime.GOOS, runtime.GOARCH, o.DataDir)
	s.state = state{
		settings:     set,
		testing:      map[string]bool{},
		subsUpdating: map[string]bool{},
		provUpdating: map[string]bool{},
		exits:        map[string]model.ExitInfo{},
		exitEntry:    map[string]string{},
		logs:         newRing[model.LogEntry](logRing),
		closed:       newRing[model.Connection](closedRing),
		overrides:    model.Overrides{PerSub: map[string]string{}},
	}
	s.state.st = model.Status{Core: model.CoreStopped, Mode: set.Mode, MixedPort: set.MixedPort, AllowLAN: set.AllowLAN}
	s.started = d.now()
	s.openErrorLog()
	if loadErr != nil {
		s.state.notices = append(s.state.notices, Notice{Kind: "error", Title: i18n.T("设置文件有问题"), Detail: loadErr.Error()})
		s.alog.Errorf("settings file: %v", loadErr)
	}
	if port, err := d.freePort(); err == nil {
		s.state.probePort = port
	}
	if _, err := os.Stat(s.hintPath()); err == nil {
		s.state.hintDone = true
	}
	s.migrateLegacyCore()
	s.resolveBinaryLocked()
	v := s.readStore()
	s.applyStoreLocked(v)
	s.state.effDNS, s.state.effSnf = s.subscriptionSections(v)
	s.detectRecovery()
	s.releaseDNS() // a previous run that died with TUN on; set again if TUN starts
	s.stats = newStatsStore(filepath.Join(o.DataDir, "stats"), d.now, s.statsCountry)
	if err := s.stats.load(); err != nil {
		s.alog.Errorf("traffic statistics: %v", err)
	}
	return s, nil
}

// run starts the background work: crash watcher, subscription scheduler,
// sleep/network watcher, one-off startup tasks and the automatic core start.
func (s *Service) run() {
	s.goBackground(s.watchCrashes)
	s.goBackground(s.autoUpdateLoop)
	s.goBackground(s.watchSystem)
	s.goBackground(s.watchCoexist)
	s.goBackground(s.bootstrap)
	s.goBackground(s.coreUpdateLoop)
	s.goBackground(func() { s.stats.loop(s.rootCtx) })
}

func (s *Service) goBackground(fn func()) {
	if !s.enterBackground() {
		return
	}
	go func() {
		defer s.wg.Done()
		defer s.elog.CatchPanic("background")
		fn()
	}()
}

// enterBackground adds one to wg unless Shutdown is waiting for it; the
// caller must call wg.Done when it returns true.
func (s *Service) enterBackground() bool {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	if s.closing {
		return false
	}
	s.wg.Add(1)
	return true
}

// afterFunc is time.AfterFunc whose callback is tracked by wg (Shutdown
// waits for a callback that already started) and skipped once the service
// is shutting down.
func (s *Service) afterFunc(d time.Duration, fn func()) *time.Timer {
	return time.AfterFunc(d, func() {
		if s.rootCtx.Err() != nil || !s.enterBackground() {
			return
		}
		defer s.wg.Done()
		fn()
	})
}

// bootstrap runs once after creation: core version, helper availability,
// network info, then starts the core when appropriate.
func (s *Service) bootstrap() {
	s.mu.Lock()
	bin := s.state.coreBin
	set := s.state.settings
	pending := s.state.st.PendingRecovery
	s.mu.Unlock()

	if bin != "" {
		ctx, cancel := s.ctrlCtx(nil, 10*time.Second)
		v, err := s.d.core.Version(ctx, bin)
		cancel()
		if err == nil {
			s.mu.Lock()
			if s.state.st.CoreVersion == "" {
				s.state.st.CoreVersion = v
			}
			s.mu.Unlock()
			s.em.emit(TopicStatus)
		}
	}
	s.learnBundledHelper()
	s.refreshHelper()
	s.learnBundledVersion()
	_ = s.RefreshNetwork()

	if s.rootCtx.Err() != nil || pending {
		return // the recovery answer decides whether to start
	}
	_, hasSub := s.d.store.Current()
	switch {
	case set.AutoSystemProxy && hasSub:
		_ = s.SetSystemProxy(true)
	case hasSub:
		_ = s.ensureStarted()
	}
}

// refreshHelper re-checks whether the privileged helper is installed and
// of this app's version. A helper left over from another release counts
// as not installed (TUN needs a reinstall); the first time that is seen
// the user is told so.
func (s *Service) refreshHelper() bool {
	ctx, cancel := s.ctrlCtx(nil, 3*time.Second)
	err := s.d.helper.Check(ctx)
	cancel()
	ok := err == nil
	stale := errors.Is(err, helper.ErrVersionMismatch) || errors.Is(err, helper.ErrProtocolMismatch)
	s.mu.Lock()
	changed := s.state.helperOK != ok || s.state.helperStale != stale
	becameStale := stale && !s.state.helperStale
	s.state.helperOK = ok
	s.state.helperStale = stale
	s.state.st.TUNAvailable = ok
	s.state.st.HelperReinstall = stale
	s.mu.Unlock()
	if becameStale {
		s.notify(Notice{Kind: "toast", Title: i18n.T("需要重新安装权限组件"), Detail: helperStaleText(err)})
	}
	if changed {
		s.em.emit(TopicStatus)
	}
	return ok
}

// helperStaleText explains a version/protocol mismatch of the helper.
func helperStaleText(err error) string {
	var ve *helper.VersionError
	if errors.As(err, &ve) {
		return i18n.Tf("已安装的权限组件（%s）与澜流自带的版本（%s）不一致，TUN 模式暂不可用。请在「设置 › 代理」中重新安装权限组件。", ve.Installed, ve.Bundled)
	}
	return i18n.T("已安装的权限组件与这个版本的澜流不兼容，TUN 模式暂不可用。请在「设置 › 代理」中重新安装权限组件。")
}

// learnBundledHelper tells the helper package which helper version this
// app ships, so Check can spot a helper left over from another release.
// A build without a bundled helper (go run, tests) skips it.
func (s *Service) learnBundledHelper() {
	bin := s.opt.helperBinary()
	if _, err := os.Stat(bin); err != nil {
		return
	}
	ctx, cancel := s.ctrlCtx(nil, 10*time.Second)
	defer cancel()
	if err := s.d.helper.SetBundled(ctx, bin); err != nil {
		s.alog.Warnf("read bundled helper version: %v", err)
	}
}

// resolveBinaryLocked finds the core binary and updates Status; call with mu held or before run.
func (s *Service) resolveBinaryLocked() {
	bin, err := s.d.core.FindBinary(s.state.settings.CorePath)
	if err != nil {
		s.state.coreBin = ""
		if s.state.st.Core == model.CoreStopped || s.state.st.Core == model.CoreMissing {
			s.state.st.Core = model.CoreMissing
			s.state.st.CoreError = coreMissingText(s.state.settings.CorePath, err)
		}
		return
	}
	s.state.coreBin = bin
	if s.state.st.Core == model.CoreMissing {
		s.state.st.Core = model.CoreStopped
		s.state.st.CoreError = ""
	}
}

func coreMissingText(custom string, err error) string {
	if custom != "" {
		return fmt.Sprintf(i18n.T("未找到 Mihomo 内核：%s 不存在或不可执行。请在设置 › 内核中重新选择内核文件。"), custom)
	}
	return i18n.T("未找到 Mihomo 内核。请在设置 › 内核中选择内核文件，或重新安装澜流。")
}

// storeView is what the service caches from the store, read in one go
// without mu (the store has its own lock and may wait for a disk write).
type storeView struct {
	subs   []model.Subscription
	cur    model.Subscription
	hasCur bool
	ovs    []model.Override
	custom []model.CustomRule
}

// readStore reads the store's lists; call without mu.
func (s *Service) readStore() storeView {
	v := storeView{subs: s.d.store.List(), ovs: s.d.store.Overrides(), custom: s.d.store.CustomRules()}
	v.cur, v.hasCur = s.d.store.Current()
	return v
}

// applyStoreLocked refreshes the cached subscription list, overrides and
// custom rules from v; call with mu held or before run.
func (s *Service) applyStoreLocked(v storeView) {
	subs, cur, hasCur := v.subs, v.cur, v.hasCur
	for i := range subs {
		subs[i].Current = hasCur && subs[i].ID == cur.ID
		subs[i].Updating = s.state.subsUpdating[subs[i].ID]
	}
	s.state.subs = subs
	s.state.st.HasSubscriptions = len(subs) > 0
	s.state.st.CurrentSubscription = ""
	if hasCur {
		s.state.st.CurrentSubscription = cur.ID
	}
	s.state.overrides = overridesSnapshot(v.ovs, subs)
	s.state.custom = v.custom
}

// refreshStore reloads the store caches and emits. The store and the
// effective dns / sniffer sections (effective.go, cached by content) are
// read first, outside mu; only the assignment happens under it.
func (s *Service) refreshStore(topics ...Topic) {
	v := s.readStore()
	dns, snf := s.subscriptionSections(v)
	s.mu.Lock()
	s.applyStoreLocked(v)
	s.state.effDNS, s.state.effSnf = dns, snf
	s.mu.Unlock()
	s.em.emit(append(topics, TopicStatus)...)
}

// detectRecovery checks whether a previous run left the system proxy
// pointing at Lanflow (marker present, proxy at 127.0.0.1:<our port>).
func (s *Service) detectRecovery() {
	m, ok := s.readMarker()
	if !ok {
		return
	}
	st, err := s.d.sys.Get()
	port := m.Port
	if port == 0 {
		port = s.state.settings.MixedPort
	}
	if err != nil || !proxyIsOurs(st, m, port) {
		// Something else changed the proxy since; our marker is stale.
		s.removeMarker()
		return
	}
	if s.d.mgr.Running() {
		return
	}
	s.state.st.PendingRecovery = true
	detail := fmt.Sprintf(i18n.T("系统代理仍指向 127.0.0.1:%d。可以恢复为关闭，或保持开启并启动内核。"), port)
	if m.PAC != "" {
		detail = i18n.T("系统代理仍使用澜流的 PAC 文件。可以恢复为关闭，或保持开启并启动内核。")
	}
	s.state.notices = append(s.state.notices, Notice{
		Kind:   "recovery",
		Title:  i18n.T("上次澜流未正常退出"),
		Detail: detail,
	})
}

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}

// copyBundledGeo copies the bundled geodata files into the work dir when
// they are missing, so the first start needs no download.
func copyBundledGeo(src, dst string) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		target := filepath.Join(dst, e.Name())
		if _, err := os.Stat(target); err == nil {
			continue
		}
		in, err := os.Open(filepath.Join(src, e.Name()))
		if err != nil {
			continue
		}
		out, err := os.OpenFile(target+".tmp", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			in.Close()
			continue
		}
		_, cerr := io.Copy(out, in)
		in.Close()
		if err := out.Close(); cerr == nil {
			cerr = err
		}
		if cerr != nil {
			os.Remove(target + ".tmp")
			continue
		}
		// Keep the bundle's time, so GeoDataInfo can tell the shipped copy
		// from a download (and show when it was built, not when it was copied).
		if fi, err := e.Info(); err == nil {
			_ = os.Chtimes(target+".tmp", fi.ModTime(), fi.ModTime())
		}
		_ = os.Rename(target+".tmp", target)
	}
}

// ---- reads ----

func (s *Service) Status() model.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state.st
	// The first-run bubble (docs/design.md §8.1): shown from the first
	// successful start until the user turns the system proxy on or
	// dismisses it. Turning the proxy on settles it for good.
	if st.SystemProxy && !s.state.hintDone {
		s.state.hintDone = true
		if !s.state.hintSaving {
			s.state.hintSaving = true
			s.goBackground(s.saveHintDone)
		}
	}
	st.ShowProxyHint = st.Core == model.CoreRunning && !st.SystemProxy && !s.state.hintDone
	return st
}

// hintFile exists once the system-proxy hint bubble has served its purpose.
const hintFile = "sysproxy-hint-done"

func (s *Service) hintPath() string { return filepath.Join(s.opt.DataDir, hintFile) }

func (s *Service) saveHintDone() {
	_ = writeFileAtomic(s.hintPath(), []byte("1\n"), 0o600)
	s.mu.Lock()
	s.state.hintSaving = false
	s.mu.Unlock()
}

// DismissProxyHint hides the first-run bubble next to the system proxy
// switch, now and in later runs.
func (s *Service) DismissProxyHint() error {
	s.mu.Lock()
	s.state.hintDone = true
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	if err := writeFileAtomic(s.hintPath(), []byte("1\n"), 0o600); err != nil {
		return fmt.Errorf(i18n.T("记录提示状态失败：%v"), err)
	}
	return nil
}

func (s *Service) TrafficHistory() (up, down []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.state.up...), append([]int64(nil), s.state.down...)
}

func (s *Service) Proxies() model.ProxySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := copySnapshot(s.state.proxies)
	for i := range out.Groups {
		out.Groups[i].Icon = s.state.groupIcons[out.Groups[i].Name] // icons.go
	}
	return out
}

func copySnapshot(p model.ProxySnapshot) model.ProxySnapshot {
	out := model.ProxySnapshot{MainGroup: p.MainGroup, Proxies: make(map[string]model.Proxy, len(p.Proxies))}
	out.Groups = make([]model.ProxyGroup, len(p.Groups))
	for i, g := range p.Groups {
		g.All = append([]string(nil), g.All...)
		out.Groups[i] = g
	}
	for k, v := range p.Proxies {
		out.Proxies[k] = v
	}
	return out
}

func (s *Service) Subscriptions() []model.Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]model.Subscription(nil), s.state.subs...)
	for i := range out {
		out[i].Updating = s.state.subsUpdating[out[i].ID]
	}
	return out
}

func (s *Service) Connections() []model.Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyConns(s.state.conns)
}

func (s *Service) ClosedConnections() []model.Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyConns(s.state.closed.newestFirst())
}

func copyConns(in []model.Connection) []model.Connection {
	out := make([]model.Connection, len(in))
	for i, c := range in {
		c.Chains = append([]string(nil), c.Chains...)
		out[i] = c
	}
	return out
}

func (s *Service) Logs() []model.LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.logs.oldestFirst()
}

func (s *Service) Rules() []model.Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshRulesIfStaleLocked() // the fresh list arrives with TopicRules
	return append([]model.Rule(nil), s.state.rules...)
}

func (s *Service) CustomRules() []model.CustomRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.CustomRule(nil), s.state.custom...)
}

func (s *Service) RuleProviders() []model.RuleProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]model.RuleProvider(nil), s.state.provs...)
	for i := range out {
		out[i].Updating = s.state.provUpdating[out[i].Name]
	}
	return out
}

func (s *Service) ExitRoutes() []model.ExitRoute {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.ExitRoute(nil), s.state.routes...)
}

func (s *Service) You() *model.EndpointInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.you == nil {
		return nil
	}
	y := *s.state.you
	return &y
}

func (s *Service) NetInfo() model.NetInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.state.net
	n.DNS = append([]string(nil), n.DNS...)
	return n
}

func (s *Service) Sites() []model.SiteCheck {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.SiteCheck(nil), s.state.sites...)
}

func (s *Service) Settings() model.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copySettings(s.state.settings)
}

func copySettings(in model.Settings) model.Settings {
	out := in
	out.Bypass = append([]string(nil), in.Bypass...)
	out.Sites = append([]model.Site(nil), in.Sites...)
	out.DNS.Nameserver = append([]string(nil), in.DNS.Nameserver...)
	out.DNS.ProxyServerNameserver = append([]string(nil), in.DNS.ProxyServerNameserver...)
	out.DNS.DirectNameserver = append([]string(nil), in.DNS.DirectNameserver...)
	out.DNS.Policy = append([]model.KV(nil), in.DNS.Policy...)
	out.DNS.FakeIPFilter = append([]string(nil), in.DNS.FakeIPFilter...)
	out.DNS.Hosts = append([]model.KV(nil), in.DNS.Hosts...)
	out.Sniffer.ForceDomain = append([]string(nil), in.Sniffer.ForceDomain...)
	out.Sniffer.SkipDomain = append([]string(nil), in.Sniffer.SkipDomain...)
	out.Sniffer.SkipDstAddress = append([]string(nil), in.Sniffer.SkipDstAddress...)
	out.ATrust = in.ATrust.Clone()
	out.Advanced = in.Advanced.Clone()
	out.UI = copyUIState(in.UI)
	out.ConnHiddenColumns = append([]string(nil), in.ConnHiddenColumns...)
	out.Shortcuts = append([]model.Shortcut(nil), in.Shortcuts...)
	out.SSIDRules = append([]model.SSIDRule(nil), in.SSIDRules...)
	out.AgentGrants = append([]model.AgentGrant(nil), in.AgentGrants...)
	return out
}

func (s *Service) Overrides() model.Overrides {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := model.Overrides{Global: s.state.overrides.Global, PerSub: make(map[string]string, len(s.state.overrides.PerSub)),
		List: append([]model.Override(nil), s.state.overrides.List...)}
	for k, v := range s.state.overrides.PerSub {
		out.PerSub[k] = v
	}
	return out
}

func (s *Service) TakeNotices() []Notice {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.state.notices
	s.state.notices = nil
	return n
}

func (s *Service) ConfigDir() string { return s.opt.DataDir }
func (s *Service) LogDir() string    { return s.opt.LogDir }

func (s *Service) OnChange(fn func(Topic)) (off func()) { return s.em.add(fn) }

// notify queues a notice for the UI.
func (s *Service) notify(n Notice) {
	s.logNotice(n)
	s.mu.Lock()
	s.state.notices = append(s.state.notices, n)
	s.mu.Unlock()
	s.em.emit(TopicNotice)
}

// Shutdown restores the system proxy if Lanflow set it, stops the core and
// every background goroutine. Safe to call more than once.
func (s *Service) Shutdown() {
	s.shutdown.Do(func() {
		s.rootCancel()
		s.probeMu.Lock()
		if s.probeTimer != nil {
			s.probeTimer.Stop()
		}
		s.probeMu.Unlock()
		s.recheckMu.Lock()
		if s.recheckTimer != nil {
			s.recheckTimer.Stop()
		}
		s.recheckMu.Unlock()

		s.opMu.Lock()
		s.stopStreams()
		s.mu.Lock()
		ours := s.state.weSetProxy
		s.mu.Unlock()
		if ours {
			_ = s.releaseProxy()
		}
		s.stopPAC()
		s.releaseDNS()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = s.d.mgr.Stop(ctx)
		cancel()
		s.mu.Lock()
		s.state.weSetProxy = false
		s.state.st.SystemProxy = false
		s.state.st.Core = model.CoreStopped
		s.mu.Unlock()
		s.opMu.Unlock()

		s.bgMu.Lock()
		s.closing = true
		s.bgMu.Unlock()
		s.wg.Wait()
		s.alog.Infof("Lanflow exited cleanly (system proxy restored: %v)", ours)
		s.alog.Close()
		s.em.close()
	})
}
