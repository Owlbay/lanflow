// Package app is the service layer between the UI and everything else
// (core process, controller API, profiles, platform integration, geo).
//
// The UI depends only on API and package model; the real implementation is
// Service (service.go) and a fake lives in package app/fake for UI work and
// tests.
package app

import "lanflow/internal/model"

// Topic names what changed, so views can refresh selectively.
type Topic string

const (
	TopicStatus   Topic = "status"   // Status() changed (core state, switches, mode, speeds)
	TopicTraffic  Topic = "traffic"  // TrafficHistory() got a new sample (every second while running)
	TopicProxies  Topic = "proxies"  // Proxies() changed (selection, delays, testing flags)
	TopicSubs     Topic = "subs"     // Subscriptions() changed
	TopicConns    Topic = "conns"    // Connections()/ClosedConnections() changed (≈ every second)
	TopicLogs     Topic = "logs"     // new log entries
	TopicRules    Topic = "rules"    // Rules()/CustomRules()/RuleProviders() changed
	TopicExits    Topic = "exits"    // ExitRoutes()/You() changed
	TopicNet      Topic = "net"      // NetInfo()/Sites()/Direct() changed
	TopicSettings Topic = "settings" // Settings()/Overrides() changed
	TopicNotice   Topic = "notice"   // a Notice is available via TakeNotices()

	TopicCoreDownload Topic = "coredl"    // CoreDownload() changed (progress of DownloadCore / InstallCore / UseCore)
	TopicCores        Topic = "cores"     // InstalledCores() changed (a version installed, removed or switched to)
	TopicProbe        Topic = "probe"     // Probe() changed (检测 page: IP 纯净度 / 解锁检测 / 地区探查 progress)
	TopicStats        Topic = "stats"     // TrafficStats() changed (new bytes accounted, or a reset)
	TopicProcIcons    Topic = "procicons" // ProcessIcon() has an icon it did not have before
)

// Notice is a message the service wants the UI to show (toast or dialog),
// for events the UI did not initiate: auto-update failures, core crashes,
// recovery prompts.
type Notice struct {
	Kind    string // "toast", "error", "core-crash", "invalid-config", "recovery"
	Title   string
	Detail  string
	Subject string // e.g. subscription ID
	// System asks the UI to also show the notice as a system notification,
	// so it reaches the user while the window is hidden. Only for core
	// crashes and failed subscription auto-updates (docs/design.md §9).
	System bool
}

// API is everything the UI may call.
//
// Reads are cheap: they return cached snapshots and never block on I/O, so
// views may call them on every build. Actions may block (network, process
// start, privilege prompts): the UI must call them off the UI thread and
// publish results with Window.Update. Every action returns a user-facing
// error message in Chinese (the error's Error() text is shown as is).
type API interface {
	// ---- reads ----
	Status() model.Status
	TrafficHistory() (up, down []int64) // last 60 samples, oldest first, bytes/s
	Proxies() model.ProxySnapshot
	Subscriptions() []model.Subscription
	Connections() []model.Connection       // active
	ClosedConnections() []model.Connection // most recent first, at most 500
	Logs() []model.LogEntry                // at most 1000, oldest first
	Rules() []model.Rule                   // runtime rules incl. custom ones first
	CustomRules() []model.CustomRule
	RuleProviders() []model.RuleProvider
	ExitRoutes() []model.ExitRoute // main group first
	You() *model.EndpointInfo      // the user's direct public endpoint, nil until known
	NetInfo() model.NetInfo
	Sites() []model.SiteCheck
	Settings() model.Settings
	Overrides() model.Overrides
	TakeNotices() []Notice // returns and clears pending notices
	ConfigDir() string
	LogDir() string

	// OnChange registers fn, called from any goroutine after state changes.
	// Calls are coalesced; fn must be quick (e.g. schedule Window.Update).
	OnChange(fn func(Topic)) (off func())

	// ---- core & switches ----
	StartCore() error
	StopCore() error // also turns the system proxy off if Lanflow set it
	RestartCore() error
	SetSystemProxy(on bool) error // on starts the core first if needed
	SetTUN(on bool) error         // requires the helper; returns ErrHelperRequired if missing
	TUNHelperInstalled() bool
	InstallTUNHelper() error   // prompts for administrator rights
	UninstallTUNHelper() error // prompts for administrator rights
	SetMode(m model.Mode) error
	ResolveRecovery(restore bool) error // answer to a "recovery" notice: true = turn system proxy off

	// ---- proxies ----
	SelectProxy(group, name string) error
	TestGroupDelay(group string) error // returns when done; Proxies() shows Testing meanwhile
	TestProxyDelay(name string) error
	TestAllDelays() error
	SetMainGroup(group string) error
	ProxyProviders() []model.ProxyProvider // proxy-providers (代理集合), by name; changes emit TopicProxies
	UpdateProxyProvider(name string) error // re-downloads the provider; ProxyProviders() shows Updating meanwhile
	HealthCheckProvider(name string) error // tests every member; ProxyProviders() shows Checking meanwhile
	// IconData returns the image bytes (PNG/JPEG/SVG…) of a proxy group's
	// icon reference (model.ProxyGroup.Icon: http(s) URL, data: URL or
	// inline SVG). Cheap: a URL not cached yet is fetched in the background
	// (into CacheDir) and TopicProxies fires when it lands; ok is false
	// until then, or for good when it cannot be loaded.
	IconData(ref string) (data []byte, ok bool)

	// ---- UI state ----
	UIState() model.UIState            // remembered presentation state (cheap read)
	SaveUIState(u model.UIState) error // persists it; never touches the core
	// UpdateUIState changes the remembered state under the settings lock
	// (fn gets a private copy) and persists it: the race-free form of
	// UIState + edit + SaveUIState. fn must not block or call the API.
	UpdateUIState(fn func(*model.UIState)) error

	// ---- subscriptions ----
	ImportSubscriptionURL(url, name, userAgent string, viaProxy, use bool) (model.Subscription, error)
	ImportSubscriptionFile(path string, use bool) (model.Subscription, error)
	UpdateSubscription(id string) error
	UpdateAllSubscriptions() error
	UseSubscription(id string) error             // validates before switching; error leaves the old one
	EditSubscription(s model.Subscription) error // name, URL, auto update, UA, via proxy
	DeleteSubscription(id string) error          // refuses the current one
	SubscriptionRaw(id string) (string, error)

	// ---- connections & logs ----
	CloseConnection(id string) error
	CloseAllConnections() error
	ClearLogs()
	// ProcessIcon returns the icon of the application behind a
	// connection's ProcessPath (model.Connection.ProcessPath) as PNG bytes,
	// nil when none is known: cheap, the icon is drawn in the background
	// (into CacheDir) the first time and TopicProcIcons fires when it lands.
	ProcessIcon(path string) []byte

	// ---- traffic statistics (流量统计) ----
	// TrafficStats is a cheap read of the statistics of one range
	// (model.StatsToday, StatsWeek, StatsMonth or StatsAll): totals, a
	// time series and the buckets of every dimension (TopicStats).
	TrafficStats(rangeKey string) model.TrafficStats
	// ResetTrafficStats forgets every statistic, on disk too.
	ResetTrafficStats() error

	// ---- rules ----
	SaveCustomRule(r model.CustomRule) (model.CustomRule, error) // empty ID = new, placed first
	DeleteCustomRule(id string) error
	MoveCustomRule(id string, delta int) error // -1 = up
	SetRuleDisabled(index int, disabled bool) error
	TestRule(host string) (model.RuleMatch, error)
	UpdateRuleProvider(name string) error

	// ---- exits & network ----
	ProbeExits() error // measure all current exits via Cloudflare trace
	RefreshNetwork() error
	TestSites() error
	DNSQuery(name, qtype string) (model.DNSResult, error)
	FlushDNSCache() error
	FlushFakeIPCache() error
	// LookupIP resolves an IP's country (and network) offline from the
	// bundled GeoIP/ASN databases; cheap, safe on the UI thread. Fields
	// stay empty when unknown; Place is nil without a country.
	LookupIP(ip string) model.EndpointInfo
	// DismissProxyHint hides the first-run bubble next to the system proxy
	// switch for good (Status().ShowProxyHint; it also clears when the
	// system proxy is turned on).
	DismissProxyHint() error
	// LeakCheck is a cheap read of the 泄漏检查 state: the items of the last
	// RunLeakCheck and whether one is running (TopicNet).
	LeakCheck() model.LeakResult
	// RunLeakCheck tests whether DNS requests and IPv6 traffic bypass the
	// proxy (docs/design.md §5.7 泄漏检查), on demand only; results land in
	// LeakCheck(). Needs the running core.
	RunLeakCheck() error
	// RunLeakCheckWith is RunLeakCheck with a domain of the user's choice
	// added to the DNS comparison ("" = the test host only).
	RunLeakCheckWith(domain string) error

	// ---- 检测 page (probe.go) ----
	// Probe is a cheap read of the 检测 page state: the IP 纯净度 and
	// 解锁检测 reports by target (a node name or model.ProbeDirect) and the
	// last 地区探查 (TopicProbe).
	Probe() model.ProbeState
	// ProbeRegions groups the current nodes by the country their names
	// suggest, most nodes first. Cheap read (cached per node).
	ProbeRegions() []model.ProbeRegion
	// ProbeIP measures target's exit in the background (Cloudflare trace +
	// bundled databases); with online it also asks the free, keyless
	// ip-api.com and ippure.com through the same exit. Returns at once; the
	// report lands in Probe().IP[target].
	ProbeIP(target string, online bool) error
	// ProbeUnlock runs the service unlock checks through target in the
	// background; results arrive one by one in Probe().Unlock[target].
	ProbeUnlock(target string) error
	// ProbeRegion tests every node of country (ProbeRegions): delay, exit
	// and, with withUnlock, the unlock checks; Probe().Region follows along.
	ProbeRegion(country string, withUnlock bool) error

	// ---- settings ----
	// EffectiveDNS returns the DNS configuration in force: Settings().DNS
	// when it takes over, else the current subscription's dns section after
	// the overrides (docs/design.md §5.8 跟随订阅). Cheap read (TopicSettings,
	// TopicSubs).
	EffectiveDNS() model.EffectiveDNS
	// EffectiveSniffer is EffectiveDNS for the sniffer section.
	EffectiveSniffer() model.EffectiveSniffer
	// GeoDataInfo reports when the geo database files in the core's work
	// directory last changed, picking up the core's own scheduled updates
	// (docs/design.md:526 外部资源 · 上次更新). Cheap read.
	GeoDataInfo() model.GeoDataInfo
	SaveSettings(s model.Settings) error // applies what changed; reloads the core when needed
	ApplyDNS(d model.DNSSettings) error
	ApplySniffer(s model.SnifferSettings) error
	// SaveOverride sets the text of the override entry key (an override ID;
	// "global" and a subscription's ID name the legacy entries, created on
	// demand) after validating the composed config; "" removes it.
	SaveOverride(key, text string) error
	PreviewOverride(key, text string) ([]model.DiffLine, error)

	// ---- override list (model.Override, docs/design.md §6.9) ----
	// OverrideList is the ordered list; cheap read (TopicSettings).
	OverrideList() []model.Override
	// SaveOverrideEntry adds (empty ID) or updates an entry: name, enabled,
	// global, source, URL and text. A URL entry without text is downloaded
	// first. When the entry takes part in a subscription's config, that
	// config is validated with the core before anything is stored, and the
	// running core reloads when it is affected.
	SaveOverrideEntry(o model.Override) (model.Override, error)
	DeleteOverrideEntry(id string) error
	MoveOverrideEntry(id string, delta int) error // -1 = up
	// UpdateOverrideEntry re-downloads a URL entry, validates and applies it.
	UpdateOverrideEntry(id string) error
	RuntimeConfig() ([]model.RuntimeLine, error)
	UpdateGeoData() error

	// ---- core versions (cores*.go) ----
	// CoreReleases lists the newest releases of channel
	// (model.CoreChannelStable / CoreChannelAlpha) for this OS/arch, newest
	// first, from GitHub; the list is cached for about an hour unless
	// refresh. Blocks on the network.
	CoreReleases(channel string, refresh bool) ([]model.CoreRelease, error)
	// InstalledCores is a cheap read: the bundled core first, then every
	// installed version, newest first (TopicCores).
	InstalledCores() []model.InstalledCore
	// InstallCore downloads version of channel ("" = the newest) into the
	// data directory and verifies it (mihomo -v, and -t against the
	// current runtime config) without switching to it; progress is
	// published through CoreDownload / TopicCoreDownload.
	InstallCore(channel, version string) error
	// UseCore switches the running core to the installed version ("" =
	// bundled) with a fast stop/start on the same config, controller and
	// ports; the system proxy is left alone. On failure the previous core
	// is restored and started again.
	UseCore(version string) error
	// RemoveCore deletes an installed version; the active and the bundled
	// core are refused.
	RemoveCore(version string) error
	// CheckCoreUpdate looks for a newer version in Settings.CoreChannel.
	CheckCoreUpdate() (model.CoreUpdateInfo, error)

	// ---- core download / update ----
	// CoreDownload is a cheap read of the download progress (TopicCoreDownload).
	CoreDownload() model.CoreDownload
	// DownloadCore is InstallCore(stable, version) followed by UseCore:
	// it downloads the official Mihomo release version ("" = the pinned
	// release), verifies it and switches to it. On failure the previous
	// core stays in use.
	DownloadCore(version string) error

	// ---- settings export / import ----
	// ExportSettings writes settings, overrides and custom rules to path as
	// JSON; subscription URLs (which carry tokens) only when includeSubs.
	ExportSettings(path string, includeSubs bool) error
	// ImportSettings validates the file and applies it; the returned summary
	// says what was imported (shown as a toast).
	ImportSettings(path string) (summary string, err error)

	// ---- configuration sync (configsync.go, docs/config-sync.md) ----
	// ConfigSyncStatus is a cheap read of the sync state: the folder, the
	// last load or refusal (with the line), conflict copies and links this
	// machine's keychain lacks (TopicSettings).
	ConfigSyncStatus() model.ConfigSyncStatus
	// SetConfigSync turns sync on (model.SyncICloud, or model.SyncFolder
	// with dir) or off (model.SyncOff). When the folder already has a
	// different lanflow.yaml, choice picks model.SyncUseFile or
	// model.SyncUseLocal; "" returns ErrConfigSyncChoice and changes nothing.
	SetConfigSync(mode, dir, choice string) error
	// ResolveConfigConflict keeps the conflict copy name (keepCopy: it is
	// loaded and becomes lanflow.yaml) or the current file; the copy not
	// kept moves to <DataDir>/sync-backups.
	ResolveConfigConflict(name string, keepCopy bool) error
	// ApplyConfigFile loads a lanflow.yaml (path absolute; "" = the sync
	// file) the way an external change of the sync file loads: validated
	// with the core, nothing stored on failure, errors with the line.
	// dryRun only returns what would change. The diff shows links as
	// keychain references.
	ApplyConfigFile(path string, dryRun bool) (model.ConfigApplyResult, error)

	// ---- aTrust 兼容 (atrustcfg.go) ----
	// ATrustReference is a commented YAML template of the aTrust lists
	// with generic example values, for the user to copy and fill in.
	ATrustReference() string
	// ImportATrustConfig reads path (Lanflow's YAML/JSON, or a JavaScript
	// override script such as atrust-coexist.js), validates
	// and deduplicates the lists and returns them with a summary for the
	// user to review; nothing is saved until SaveSettings.
	ImportATrustConfig(path string) (model.ATrustConfig, string, error)
	// ExportATrustConfig writes Settings().ATrust to path as YAML.
	ExportATrustConfig(path string) error

	// ---- GitHub download mirror (mirror.go) ----
	// TestMirrors HEAD-requests a GitHub link through every MirrorPresets
	// entry, directly, and returns the round trips (Ms = -1: failed). Blocks.
	TestMirrors() []model.MirrorTest

	// ---- WebDAV backup (backup.go) ----
	WebDAVInfo() model.WebDAVInfo                          // cheap read: password store, busy, last backup (TopicSettings)
	SetWebDAVPassword(pw string) error                     // keychain, or the 0600 file fallback; "" removes it
	TestWebDAV() error                                     // connects, signs in, creates the remote directory
	BackupNow() error                                      // uploads lanflow-backup-<time>.json and prunes beyond Keep
	ListBackups() ([]model.WebDAVBackup, error)            // newest first
	RestoreBackup(name string) (summary string, err error) // download + ImportSettings
	DeleteBackup(name string) error

	// ---- Lanflow's own log & diagnostics ----
	// AppLogs is Lanflow's own run log (actions, notices, errors), at most
	// 1000 entries, oldest first; new entries emit TopicLogs. The file is
	// lanflow.log in LogDir().
	AppLogs() []model.LogEntry
	ClearAppLogs() // the in-memory entries; the file is kept
	// ExportDiagnostics writes a zip with the logs, the runtime config,
	// the settings and the system's network state to path, with servers,
	// credentials and tokens removed.
	ExportDiagnostics(path string) error

	// ---- 反馈问题 (feedback.go, docs/feedback-api.md) ----
	// FeedbackInfo is a cheap read: the endpoint, whether a token is set,
	// how many error records errors.jsonl holds and the previous run's
	// crash until DismissLastCrash (TopicSettings).
	FeedbackInfo() model.FeedbackInfo
	// SetFeedbackToken keeps the endpoint's bearer token in the system
	// keychain (the obfuscated file fallback without one); "" removes it.
	SetFeedbackToken(token string) error
	// DismissLastCrash forgets the previous run's crash (FeedbackInfo).
	DismissLastCrash()
	// BuildFeedback assembles a report for the user to look through: a
	// new ID, the description, the recent error records and, as chosen,
	// the redacted diagnostics bundle and the recent part of lanflow.log.
	// Nothing leaves the machine; the report is kept for SendFeedback and
	// SaveFeedback.
	BuildFeedback(opts model.FeedbackOptions) (model.FeedbackReport, error)
	// SendFeedback posts the report built as id, exactly as previewed, to
	// {Settings.Feedback.Endpoint}/v1/reports, retrying with backoff.
	// Window only: the user confirms what is sent.
	SendFeedback(id string) (model.FeedbackSent, error)
	// SaveFeedback writes the report built as id to path as a zip, for
	// sending by hand.
	SaveFeedback(id, path string) error

	// ---- HTTPS 选择性解密 (capture.go, docs/capture.md) ----
	// CaptureStatus is a cheap read: switch, auto-off time, root
	// certificate state, selection (TopicCapture).
	CaptureStatus() model.CaptureStatus
	// CaptureFlows is a cheap read of the records (no headers or bodies),
	// oldest first; at most 5000.
	CaptureFlows() []model.CaptureFlow
	// CaptureFlow fetches one record with headers and bodies from the
	// capture agent.
	CaptureFlow(id int64) (model.CaptureFlowDetail, error)
	// SetCapture turns decryption on (needs a trusted root, a selection and
	// the first-run acknowledgement) or off; off reloads the core and
	// closes the decrypted connections.
	SetCapture(on bool) error
	// ExtendCapture pushes the automatic turn-off 30 minutes further.
	ExtendCapture() error
	// SaveCaptureSettings stores the selection; a running capture follows.
	SaveCaptureSettings(s model.CaptureSettings) error
	// AddCaptureDomain adds a host to the selection (and turns on).
	AddCaptureDomain(host string, enable bool) error
	// AddCaptureApp adds the program behind a connection (and turns on).
	AddCaptureApp(processPath, process string, enable bool) error
	ClearCaptureFlows() error
	// InstallCACert creates the root if needed and asks the system to
	// trust it (the system shows its own prompt).
	InstallCACert() error
	// RegenerateCA replaces the root: new root trusted, old one removed.
	RegenerateCA() error
	// RemoveCACert turns decryption off and removes the root's trust,
	// key and certificate.
	RemoveCACert() error
	// ExportCACert writes the root certificate (never the key) as PEM.
	ExportCACert(path string) error
	// ExportHAR writes the records as HAR 1.2; bodies only with withBodies,
	// credentials masked either way (docs/capture.md §7.4).
	ExportHAR(path string, withBodies bool) error

	// Shutdown restores the system proxy if Lanflow set it and stops the
	// core. Called once when the app quits.
	Shutdown()
}
