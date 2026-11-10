package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/core"
	"lanflow/internal/locate"
	"lanflow/internal/model"
	"lanflow/internal/netx"
	"lanflow/internal/platform/atrust"
	"lanflow/internal/platform/helper"
	"lanflow/internal/platform/netinfo"
	"lanflow/internal/platform/procicon"
	"lanflow/internal/platform/sysdns"
	"lanflow/internal/platform/sysproxy"
	"lanflow/internal/profile"
)

// The Service talks to every other package through the small interfaces
// below, so unit tests can substitute in-memory fakes. The real packages
// satisfy them directly (controller.Client, core.Manager, profile.Store) or
// through the thin adapters at the bottom of this file.

// ctrlClient is the subset of *controller.Client the service uses.
type ctrlClient interface {
	Version(ctx context.Context) (string, error)
	Configs(ctx context.Context) (controller.Configs, error)
	PatchConfigs(ctx context.Context, p controller.ConfigPatch) error
	ReloadConfig(ctx context.Context, path string) error
	UpdateGeo(ctx context.Context) error
	Proxies(ctx context.Context) (map[string]controller.Proxy, error)
	SelectProxy(ctx context.Context, group, name string) error
	ProxyDelay(ctx context.Context, name, url string, timeout time.Duration) (int, error)
	GroupDelay(ctx context.Context, group, url string, timeout time.Duration) (map[string]int, error)
	Rules(ctx context.Context) ([]controller.Rule, error)
	DisableRules(ctx context.Context, m map[int]bool) error
	RuleProviders(ctx context.Context) (map[string]controller.RuleProvider, error)
	UpdateRuleProvider(ctx context.Context, name string) error
	ProxyProviders(ctx context.Context) (map[string]controller.ProxyProvider, error)
	UpdateProxyProvider(ctx context.Context, name string) error
	HealthCheckProvider(ctx context.Context, name string) error
	Connections(ctx context.Context) (controller.Connections, error)
	CloseConnection(ctx context.Context, id string) error
	CloseAllConnections(ctx context.Context) error
	DNSQuery(ctx context.Context, name, qtype string) (controller.DNSResult, error)
	FlushDNSCache(ctx context.Context) error
	FlushFakeIPCache(ctx context.Context) error
	StreamTraffic(ctx context.Context, fn func(controller.Traffic)) error
	StreamMemory(ctx context.Context, fn func(controller.Memory)) error
	StreamLogs(ctx context.Context, level string, fn func(controller.Log)) error
	StreamConnections(ctx context.Context, interval time.Duration, fn func(controller.Connections)) error
}

// coreManager is the subset of *core.Manager the service uses.
type coreManager interface {
	Start(ctx context.Context, spec core.Spec) error
	Stop(ctx context.Context) error
	SetRunner(r core.Runner)
	Running() bool
	Crashes() <-chan error
}

// coreTools wraps the process-level functions of package core.
type coreTools interface {
	FindBinary(custom string) (string, error)
	Version(ctx context.Context, bin string) (string, error)
	Validate(ctx context.Context, bin, workDir, config string) error
	ExecRunner() core.Runner
	HelperRunner() core.Runner
}

// sysProxy wraps package sysproxy.
type sysProxy interface {
	Set(host string, port int, bypass []string) error
	Clear() error
	SetPAC(url string) error // "" turns PAC off
	Get() (sysproxy.State, error)
	Snapshot() (sysproxy.Saved, error)
	Restore(sysproxy.Saved) error
}

// sysDNS wraps package sysdns.
type sysDNS interface {
	Supported() bool
	Snapshot() (sysdns.Saved, error)
	Set(servers []string) error
	Restore(s sysdns.Saved, ours []string) error
}

// helperAPI wraps package helper.
type helperAPI interface {
	Installed(ctx context.Context) bool
	Install(ctx context.Context, o helper.InstallOptions) error
	Uninstall(ctx context.Context) error
}

// locator is the subset of *locate.Locator the service uses.
type locator interface {
	Guess(node string) model.ExitInfo
	Probe(ctx context.Context, sel locate.SelectFunc, probePort int, node string) (model.ExitInfo, error)
	Direct(ctx context.Context) (model.EndpointInfo, error)
	Analyze(info *model.ExitInfo, entryIP string, chainHops int)
	Lookup(ip string) model.EndpointInfo
}

// profileStore is the subset of *profile.Store the service uses.
type profileStore interface {
	List() []model.Subscription
	Get(id string) (model.Subscription, bool)
	Current() (model.Subscription, bool)
	SetCurrent(id string) error
	Put(sub model.Subscription) (model.Subscription, error)
	Delete(id string) error
	Content(id string) ([]byte, error)
	SetContent(id string, data []byte) error
	Override(key string) string
	SetOverride(key, text string) error
	Overrides() []model.Override
	GetOverride(id string) (model.Override, bool)
	PutOverride(o model.Override) (model.Override, error)
	DeleteOverride(id string) error
	MoveOverride(id string, delta int) error
	CustomRules() []model.CustomRule
	SetCustomRules(r []model.CustomRule) ([]model.CustomRule, error)
}

// composer wraps the package-level functions of package profile.
type composer interface {
	Compose(in profile.Input) (profile.Output, error)
	Diff(a, b []byte) []model.DiffLine
	Fetch(ctx context.Context, client *http.Client, url, userAgent string) (profile.Fetched, error)
	CheckYAML(data []byte) error
}

// deps bundles everything the service depends on.
type deps struct {
	ctrl       ctrlClient
	mgr        coreManager
	core       coreTools
	sys        sysProxy
	dns        sysDNS // nil = unsupported
	helper     helperAPI
	loc        locator
	store      profileStore
	prof       composer
	netinfo    func(ctx context.Context) (model.NetInfo, error)
	httpDirect func(timeout time.Duration) *http.Client
	httpVia    func(port int, timeout time.Duration) *http.Client
	freePort   func() (int, error)
	portInUse  func(port int, lan bool) bool
	otherTUN   func(ctx context.Context) (string, bool)
	// v6Route reads where the IPv6 default route points (the IPv6 leak
	// check, leak.go); nil = unknown.
	v6Route func(ctx context.Context) netinfo.V6Route
	// vpnDetect looks for Sangfor aTrust (coexist.go); nil = never found.
	vpnDetect func(ctx context.Context, own func(iface string) bool) atrust.Info
	// diagCommands describes the system network for 导出诊断信息 (diag.go);
	// nil = systemDiagnostics.
	diagCommands func(ctx context.Context) map[string]string
	now          func() time.Time
	// appDNS resolves name (qtype A/AAAA/CNAME) the way an application
	// would: through the DNS server at server ("host:port"), or the system
	// resolver when server is "". nil = lookupVia (network.go).
	appDNS func(ctx context.Context, server, name, qtype string) ([]string, error)

	// Leak check (leak.go). traceURL is the Cloudflare trace endpoint
	// ("" = locate.TraceURL; tests point it at httptest). httpDirect6
	// returns a direct client that only dials IPv6 (nil = directIPv6Client,
	// built from netx.Direct).
	traceURL    string
	httpDirect6 func(timeout time.Duration) *http.Client
	// stun returns the public address a STUN server sees for a direct UDP
	// socket (the WebRTC check); nil = netx.STUNPublicIP on netx.STUNServers.
	stun func(ctx context.Context) (string, error)

	// coreDL holds the progress of DownloadCore (coredl.go). It is
	// allocated lazily under Service.mu so no constructor needs changing.
	coreDL *model.CoreDownload

	// fileIcon draws the icon of a file or application bundle as a PNG of
	// px by px pixels (procicon.go); nil = no process icons.
	fileIcon func(path string, px int) ([]byte, error)
}

// ---- real adapters ----

type realCore struct{}

func (realCore) FindBinary(custom string) (string, error) { return core.FindBinary(custom) }
func (realCore) Version(ctx context.Context, bin string) (string, error) {
	return core.Version(ctx, bin)
}
func (realCore) Validate(ctx context.Context, bin, workDir, config string) error {
	return core.Validate(ctx, bin, workDir, config)
}
func (realCore) ExecRunner() core.Runner   { return core.NewExecRunner() }
func (realCore) HelperRunner() core.Runner { return helper.NewRunner() }

type realSysProxy struct{}

func (realSysProxy) Set(host string, port int, bypass []string) error {
	return sysproxy.Set(host, port, bypass)
}
func (realSysProxy) Clear() error                      { return sysproxy.Clear() }
func (realSysProxy) SetPAC(url string) error           { return sysproxy.SetPAC(url) }
func (realSysProxy) Get() (sysproxy.State, error)      { return sysproxy.Get() }
func (realSysProxy) Snapshot() (sysproxy.Saved, error) { return sysproxy.Snapshot() }
func (realSysProxy) Restore(s sysproxy.Saved) error    { return sysproxy.Restore(s) }

type realSysDNS struct{}

func (realSysDNS) Supported() bool                 { return sysdns.Supported() }
func (realSysDNS) Snapshot() (sysdns.Saved, error) { return sysdns.Snapshot() }
func (realSysDNS) Set(servers []string) error      { return sysdns.Set(servers) }
func (realSysDNS) Restore(s sysdns.Saved, ours []string) error {
	return sysdns.Restore(s, ours)
}

type realHelper struct{}

func (realHelper) Installed(ctx context.Context) bool { return helper.Installed(ctx) }
func (realHelper) Install(ctx context.Context, o helper.InstallOptions) error {
	return helper.Install(ctx, o)
}
func (realHelper) Uninstall(ctx context.Context) error { return helper.Uninstall(ctx) }

type realProfile struct{}

func (realProfile) Compose(in profile.Input) (profile.Output, error) { return profile.Compose(in) }
func (realProfile) Diff(a, b []byte) []model.DiffLine                { return profile.Diff(a, b) }
func (realProfile) Fetch(ctx context.Context, client *http.Client, url, userAgent string) (profile.Fetched, error) {
	return profile.Fetch(ctx, client, url, userAgent)
}
func (realProfile) CheckYAML(data []byte) error { return profile.CheckYAML(data) }

// freeTCPPort asks the kernel for an unused loopback port.
// tcpPortInUse reports whether another program already listens on port
// (on 127.0.0.1, and on all interfaces when lan is set). Mihomo does not
// fail when its mixed port is taken; it just logs, so we must check first.
func tcpPortInUse(port int, lan bool) bool {
	addrs := []string{fmt.Sprintf("127.0.0.1:%d", port)}
	if lan {
		addrs = append(addrs, fmt.Sprintf("0.0.0.0:%d", port))
	}
	for _, a := range addrs {
		l, err := net.Listen("tcp", a)
		if err != nil {
			return true
		}
		l.Close()
	}
	return false
}

func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// realDeps wires the real packages. The controller client and the manager
// are created by the caller because the manager's readiness check uses the
// client.
func realDeps(ctrl *controller.Client, mgr *core.Manager, store *profile.Store, loc *locate.Locator) deps {
	return deps{
		ctrl:       ctrl,
		mgr:        mgr,
		core:       realCore{},
		sys:        realSysProxy{},
		dns:        realSysDNS{},
		helper:     realHelper{},
		loc:        loc,
		store:      store,
		prof:       realProfile{},
		netinfo:    netinfo.Get,
		httpDirect: netx.Direct,
		httpVia:    netx.ViaPort,
		freePort:   freeTCPPort,
		portInUse:  tcpPortInUse,
		otherTUN:   netinfo.OtherTUN,
		v6Route:    netinfo.IPv6Route,
		vpnDetect:  atrust.Detect,
		now:        time.Now,
		fileIcon:   procicon.Load,
	}
}
