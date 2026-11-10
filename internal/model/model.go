// Package model holds the plain data types shared by the service layer and
// the UI. It has no dependencies and no behaviour: every type here is a
// snapshot value that is safe to copy and read from any goroutine.
//
// The types here are shared by every package: add fields freely, but never
// rename or remove one (settings files and exports depend on them).
package model

import "time"

// CoreState is the lifecycle state of the Mihomo process.
type CoreState string

const (
	CoreStopped  CoreState = "stopped"
	CoreStarting CoreState = "starting"
	CoreRunning  CoreState = "running"
	CoreError    CoreState = "error"   // exited unexpectedly / failed to start
	CoreMissing  CoreState = "missing" // binary not found or not executable
)

// Mode is Mihomo's routing mode.
type Mode string

const (
	ModeRule   Mode = "rule"
	ModeGlobal Mode = "global"
	ModeDirect Mode = "direct"
)

// Status is the top-level runtime state shown in the sidebar, overview and tray.
type Status struct {
	Core        CoreState
	CoreError   string // human-readable summary when Core == CoreError / CoreMissing
	CoreVersion string // e.g. "v1.19.33"
	StartedAt   time.Time

	SystemProxy  bool // the system proxy currently points at Lanflow and Lanflow set it
	TUN          bool // TUN is enabled in the running config
	TUNAvailable bool // the privileged helper is installed and reachable
	// HelperReinstall: a helper is installed but of another version or
	// protocol than this app ships, so TUN needs a reinstall.
	HelperReinstall bool
	Mode            Mode
	MixedPort       int
	AllowLAN        bool
	LANAddress      string // e.g. "192.168.1.23", empty if unknown

	Up, Down           int64 // current speed, bytes per second
	TotalUp, TotalDown int64 // since core start, bytes
	Memory             int64 // core memory, bytes
	Connections        int   // active connections

	CurrentSubscription string // subscription ID in use, "" if none
	HasSubscriptions    bool
	PendingRecovery     bool // a previous run left the system proxy pointing at us
	// ShowProxyHint asks the overview to show the one-time bubble next to
	// the system proxy switch (docs/design.md §8.1 「打开以让系统流量走代理」):
	// the core runs, the system proxy is off and the hint was never dismissed.
	ShowProxyHint bool

	// ATrust is what the aTrust detection found (aTrust 兼容).
	ATrust ATrustState

	// ImportPhase is where a running subscription import is (docs/design.md
	// §6.1 下载 → 校验 → 完成): "" when none runs, ImportDownloading while
	// the link is fetched, ImportValidating while the core checks the config.
	ImportPhase string
}

// Import phases reported in Status.ImportPhase.
const (
	ImportDownloading = "download"
	ImportValidating  = "validate"
)

// Proxy is one outbound (node or group) as Mihomo reports it.
type Proxy struct {
	Name     string
	Type     string // "Shadowsocks", "Vmess", "Selector", "URLTest", "Direct", ...
	UDP      bool
	Delay    int // ms; 0 = not tested, -1 = timeout / failed
	Testing  bool
	Provider string // proxy-provider name, "" for inline
}

// GroupType is the normalized group kind.
type GroupType string

const (
	GroupSelect      GroupType = "select"
	GroupURLTest     GroupType = "url-test"
	GroupFallback    GroupType = "fallback"
	GroupLoadBalance GroupType = "load-balance"
	GroupRelay       GroupType = "relay"
)

// ProxyGroup is a policy group in config order.
type ProxyGroup struct {
	Name   string
	Type   GroupType
	Now    string   // currently selected member ("" for load-balance)
	All    []string // members in order (nodes or other groups)
	Hidden bool
	// Icon is the group's `icon` field from the config: an http(s) URL, a
	// data: URL or inline SVG; "" when the group has none. API.IconData
	// turns it into image bytes.
	Icon string
}

// ProxySnapshot is the whole proxy view.
type ProxySnapshot struct {
	Groups    []ProxyGroup     // config order; GLOBAL last (UI shows it only in global mode)
	Proxies   map[string]Proxy // by name, includes groups, DIRECT, REJECT
	MainGroup string           // the user's "main" group (first select group by default)
}

// Subscription is one imported profile.
type Subscription struct {
	ID              string
	Name            string
	Source          string // "url" or "file"
	URL             string // for Source == "url"
	FilePath        string // original file for Source == "file"
	Current         bool
	Upload          int64 // from subscription-userinfo, bytes; 0 if unknown
	Download        int64
	Total           int64 // 0 = unknown
	Expire          time.Time
	UpdatedAt       time.Time
	LastError       string // last update error, "" if OK
	AutoUpdateHours int    // 0 = off
	UserAgent       string // "" = default
	ViaProxy        bool   // download through the running core
	Updating        bool
	// HomePage is the provider's site from the profile-web-page-url
	// response header, "" when the server sends none.
	HomePage string
	// TimeoutSec is the download timeout in seconds; 0 = the default (30).
	TimeoutSec int
	// Overrides lists the IDs of the overrides (model.Override) this
	// subscription applies, besides the global ones; see API.OverrideList.
	Overrides []string
}

// Connection is one tracked connection.
type Connection struct {
	ID                 string
	Host               string // host or IP as requested, with port, e.g. "www.google.com:443"
	DstIP              string
	Network            string // "tcp" / "udp"
	Type               string // inbound type: "HTTP", "Socks5", "Tun", ...
	Process            string // "" if unknown
	ProcessPath        string
	Rule               string   // e.g. "DomainSuffix"
	RulePayload        string   // e.g. "google.com"
	Chains             []string // outermost first as displayed: ["节点选择", "东京 03"]
	SourceIP           string
	SourcePort         string
	RemoteDestination  string // the IP the core actually connected to (proxy server = entry)
	Upload, Download   int64  // totals, bytes
	UpSpeed, DownSpeed int64  // bytes per second (computed from deltas)
	Start              time.Time
	ClosedAt           time.Time // zero for active
}

// LogEntry is one core log line.
type LogEntry struct {
	Time    time.Time
	Level   string // "debug", "info", "warning", "error"
	Payload string
}

// Rule is one active rule as Mihomo reports it.
type Rule struct {
	Index    int // 0-based index in the runtime rule list
	Type     string
	Payload  string
	Proxy    string // target policy
	Size     int    // entries for GEOSITE/GEOIP/RULE-SET, -1 if not applicable
	Disabled bool
	HitCount int64
	HitAt    time.Time
	Custom   bool // came from Lanflow's custom rules
}

// CustomRule is a user rule kept by Lanflow and placed before subscription rules.
type CustomRule struct {
	ID      string
	Type    string // DOMAIN-SUFFIX, DOMAIN, DOMAIN-KEYWORD, IP-CIDR, PROCESS-NAME
	Payload string
	Target  string // group name, "DIRECT" or "REJECT"
}

// RuleProvider is a rule-provider.
type RuleProvider struct {
	Name        string
	Behavior    string // "domain", "ipcidr", "classical"
	Format      string
	Count       int
	VehicleType string // "HTTP", "File", "Inline"
	UpdatedAt   time.Time
	Updating    bool
}

// ProxyProvider is a proxy-provider (代理集合, docs/design.md §5.2).
type ProxyProvider struct {
	Name        string
	VehicleType string   // "HTTP", "File", "Inline", "Compatible"
	Proxies     []string // member node names in provider order
	TestURL     string   // health-check URL ("" = none configured)
	UpdatedAt   time.Time
	// Subscription usage from the provider's subscription-userinfo header;
	// Total == 0 means unknown, Expire.IsZero() means none.
	Upload, Download, Total int64
	Expire                  time.Time
	Updating                bool // re-download in progress
	Checking                bool // health check in progress
}

// UIState is small presentation state the UI asks the service to remember
// across restarts (persisted with the settings; never affects the core).
type UIState struct {
	OpenGroups map[string]bool // proxy group name → expanded; absent = default
	ProxyGrid  bool            // the 代理 page shows nodes as a compact grid
	// ProxySort is the 代理 page's node order: "" (subscription order),
	// "delay" or "name".
	ProxySort          string
	ProxyHideTimeout   bool // the 代理 page hides timed-out nodes
	ProxyProvidersOpen bool // the 代理 page's 代理集合 section is expanded
	// NetTab is the 网络 page's tab: "overview", "connectivity" or "dns";
	// ProbeTab the 检测 page's: "leak", "ip", "unlock" or "region".
	// "" or an unknown key = the first tab.
	NetTab    string
	ProbeTab  string
	LogLevel  string // the 日志 page's lowest level shown ("debug"…"error"); "" = default
	LogSource string // the 日志 page's source: "core" or "app"; "" = default
}

// RuleMatch is the result of testing an address against the rules.
type RuleMatch struct {
	Host      string
	RuleIndex int    // index in runtime rules, -1 if MATCH fallback not reported
	Rule      string // e.g. "DOMAIN-SUFFIX,openai.com"
	Custom    bool
	Policy    string // first hop policy (group or DIRECT/REJECT)
	Chain     []string
}

// Place is a geographic place.
type Place struct {
	Country string // ISO 3166-1 alpha-2
	City    string // display name (Chinese); country name when the city is unknown
	Lat     float64
	Lon     float64
}

// LocateSource tells how an exit location was obtained.
type LocateSource string

const (
	LocateProbe   LocateSource = "probe" // measured via Cloudflare trace
	LocateName    LocateSource = "name"  // guessed from the node name
	LocateUnknown LocateSource = "unknown"
)

// PathKind classifies the path from the user to the exit.
type PathKind string

const (
	PathUnknown    PathKind = ""
	PathDirect     PathKind = "direct"     // entry and exit in the same place
	PathRelay      PathKind = "relay"      // entry and exit differ: provider-side relay
	PathChain      PathKind = "chain"      // client-side relay / dialer-proxy
	PathSuspicious PathKind = "suspicious" // IP country vs colo or latency mismatch
)

// EndpointInfo describes an IP endpoint.
type EndpointInfo struct {
	IP    string
	Place *Place
	ISP   string // organisation from the ASN database
	ASN   string // e.g. "AS4812"
}

// ExitInfo is where a node really exits.
type ExitInfo struct {
	Node       string
	Place      *Place // nil when unknown
	Source     LocateSource
	Exit       EndpointInfo // exit IP etc. (probe only)
	Colo       string       // Cloudflare colo, e.g. "NRT" (probe only)
	Entry      *EndpointInfo
	Path       PathKind
	PathNote   string // e.g. "名称为台湾，实测出口在香港"
	ChainHops  int    // for PathChain
	MeasuredAt time.Time
}

// ExitRoute is one line on the overview map: a first-level group with traffic.
type ExitRoute struct {
	Group       string
	Node        string // final node after resolving nested groups
	Exit        ExitInfo
	Speed       int64 // bytes/s, up+down of connections through this group
	Connections int
	Main        bool // the main group
}

// NetInfo is local network information.
type NetInfo struct {
	Interface string // "Wi-Fi (en0)"
	SSID      string
	LANIP     string
	Gateway   string
	DNS       []string
	IPv6      string // public IPv6 or "" if none
	// PublicIPStatus is the state of the direct public endpoint lookup that
	// fills You(): "fetching", "ok" or "failed" ("" before the first try).
	PublicIPStatus string
	PublicIPError  string // why the last lookup failed, for PublicIPStatus == "failed"
}

// SiteCheck is a connectivity test result.
type SiteCheck struct {
	Name     string
	URL      string
	ViaProxy int // ms; 0 = not tested, -1 = timeout, -2 = testing
	Direct   int
}

// DNSResult is a DNS query result through the core.
type DNSResult struct {
	Name    string
	Type    string
	Answers []string
	Status  string // "NOERROR", "NXDOMAIN", ...
	FakeIP  bool   // AppAnswers (or, without them, Answers) are fake-ip addresses
	// AppAnswers is what applications on this machine get for the same
	// query: through the core's DNS listener when the config has one, or
	// through the system resolver while TUN hijacks DNS. Empty when
	// applications do not resolve through the core (system proxy mode:
	// the proxy receives the host name and resolves it itself).
	AppAnswers []string
	AppSource  string // "listener", "system" or ""
}

// KV is an ordered key/value pair.
type KV struct{ Key, Value string }

// DNSSettings is the DNS section Lanflow writes when TakeOver is true.
type DNSSettings struct {
	TakeOver              bool
	EnhancedMode          string // "fake-ip" or "redir-host"
	Nameserver            []string
	ProxyServerNameserver []string
	DirectNameserver      []string
	Policy                []KV // nameserver-policy
	FakeIPFilter          []string
	RespectRules          bool
	UseSystemHosts        bool
	Hosts                 []KV
}

// SnifferSettings is the sniffer section Lanflow writes when TakeOver is true.
type SnifferSettings struct {
	Enable              bool // always applied (hot via PATCH /configs sniffing)
	TakeOver            bool
	HTTP, TLS, QUIC     bool
	HTTPPorts           string // "80, 8080-8880"
	TLSPorts            string
	QUICPorts           string
	OverrideDestination bool
	ParsePureIP         bool
	ForceDomain         []string
	SkipDomain          []string
	SkipDstAddress      []string
}

// GeoSettings configures the geo databases (geox-url etc.).
type GeoSettings struct {
	GeoIP, GeoSite, MMDB, ASN string // URLs
	Mode                      string // "db" (geodata-mode false) or "dat"
	AutoUpdate                bool
	IntervalHours             int
	UpdatedAt                 time.Time
}

// GeoDataInfo describes the geo database files in the core's work
// directory (API.GeoDataInfo), so 外部资源 · 上次更新 can show when they
// really changed, whether by 立即更新 or by the core's own auto-update.
type GeoDataInfo struct {
	UpdatedAt time.Time // newest modification time among the files; zero when none exists
	Bundled   bool      // every present file is still the copy shipped with the app
	Files     int       // database files present
}

// Effective-settings sources (EffectiveDNS.Source, EffectiveSniffer.Source).
const (
	EffectiveLanflow      = "lanflow"      // Lanflow's own settings (由澜流接管)
	EffectiveSubscription = "subscription" // the current subscription's section, after overrides
	EffectiveDefault      = "default"      // no section in the subscription: Mihomo's defaults
)

// EffectiveDNS is the DNS configuration the core runs (API.EffectiveDNS):
// Lanflow's own settings when DNS.TakeOver, else the current subscription's
// dns section (after the overrides) parsed into DNSSettings, or Mihomo's
// defaults when the subscription has none.
type EffectiveDNS struct {
	Settings DNSSettings
	Source   string // EffectiveLanflow, EffectiveSubscription or EffectiveDefault
	// Enabled is dns.enable: Mihomo's built-in resolver is off unless the
	// config turns it on, in which case the core resolves through the system.
	Enabled bool
	Listen  string // dns.listen, "" when the core serves no DNS
	IPv6    bool   // dns.ipv6: whether the resolver answers AAAA queries
}

// EffectiveSniffer is the sniffer configuration the core runs
// (API.EffectiveSniffer); see EffectiveDNS for Source.
type EffectiveSniffer struct {
	Settings SnifferSettings
	Source   string
}

// Leak-check item states (LeakItem.Status).
const (
	LeakOK      = "ok"      // nothing bypasses the proxy
	LeakWarn    = "warn"    // partly covered, or could not be confirmed
	LeakFail    = "fail"    // traffic bypasses the proxy
	LeakSkipped = "skipped" // not applicable (e.g. direct mode)
)

// LeakItem is one line of the 泄漏检查 result.
type LeakItem struct {
	Name    string // "DNS" or "IPv6"
	Status  string // LeakOK, LeakWarn, LeakFail or LeakSkipped
	Summary string // one sentence: what was found
	Detail  string // what it means and what to do, "" when nothing to add
	// Title is the displayed name when it differs from Name (translated);
	// Rows is the structured part of the detail (checks.go), nil for the
	// prose-only checks.
	Title string
	Rows  []LeakRow
}

// LeakResult is the state of the 泄漏检查 (API.LeakCheck): the items of
// the last run and whether one is in progress.
type LeakResult struct {
	Checking  bool
	Items     []LeakItem
	CheckedAt time.Time // zero before the first run
	Error     string    // why the last run could not finish, "" when it did
}

// Site is a connectivity-check target.
type Site struct{ Name, URL string }

// Settings is everything the user can set. Persisted as JSON by the service.
type Settings struct {
	// general
	OpenAtLogin     bool
	AutoSystemProxy bool // turn on the system proxy after launch
	SilentStart     bool
	CloseToTray     bool   // false = quit on close
	Appearance      string // "system", "light", "dark"
	Language        string // "zh", "en"

	// proxy
	MixedPort int
	AllowLAN  bool
	Bypass    []string // system proxy bypass list
	TUN       bool     // desired TUN state
	TUNStack  string   // "mixed" (default), "system", "gvisor"
	TUNSetDNS bool     // while TUN runs, point the system DNS at a resolver the TUN captures (macOS)
	// ATrustCompat keeps Sangfor aTrust's intranet out of Lanflow while
	// aTrust runs (route exclusion, DIRECT rules, proxy bypass); on by default.
	ATrustCompat bool
	ATrust       ATrustConfig // the user's lists for aTrust 兼容 (model/atrust.go)
	ProbeExits   bool         // allow Cloudflare trace probes
	Mode         Mode
	// HideTraySpeed turns off the live up/down speed shown beside the
	// menu bar icon (macOS, docs/design.md §7); shown by default.
	HideTraySpeed bool
	// GlobalShortcut is the system-wide shortcut that toggles the system
	// proxy, as a MyGo accelerator ("CmdOrCtrl+Shift+S", the default,
	// docs/design.md §9); "" turns it off.
	GlobalShortcut string
	// DisableUpdateCheck stops the daily check for a new version of the
	// app at launch (docs/design.md §9 应用自动更新); checks run by default.
	DisableUpdateCheck bool

	// core
	LogLevel string // "debug", "info", "warning", "error", "silent"
	CorePath string // "" = bundled
	// CoreVersion is the installed Mihomo version in use (model/cores.go:
	// <DataDir>/cores/<version>); "" = the core bundled with the app or
	// the custom file in CorePath. CoreChannel ("stable" / "alpha") is the
	// channel updates are looked for in; CoreAutoCheck looks daily and
	// CoreAutoInstall also installs and switches to what it finds.
	CoreVersion     string `json:",omitempty"`
	CoreChannel     string `json:",omitempty"`
	CoreAutoCheck   bool
	CoreAutoInstall bool   `json:",omitempty"`
	MainGroup       string // "" = first select group
	// DelayURL and DelayTimeoutMs are what every delay test uses (defaults:
	// https://www.gstatic.com/generate_204, 5000 ms; see API.TestGroupDelay).
	DelayURL       string
	DelayTimeoutMs int
	// DisconnectOnSwitch closes the connections going through a group when
	// its selection changes (API.SelectProxy); off by default.
	DisconnectOnSwitch bool

	DNS     DNSSettings
	Sniffer SnifferSettings
	Geo     GeoSettings
	Sites   []Site

	// UI is remembered presentation state (expanded proxy groups, grid
	// view); see API.UIState / API.SaveUIState.
	UI UIState

	// ConnHiddenColumns lists the column IDs the 连接 table hides (chosen by
	// right-clicking its header, docs/design.md §5.4); nil shows every column.
	ConnHiddenColumns []string

	// feat2.go: shortcut actions other than the system proxy (which stays
	// in GlobalShortcut), Wi-Fi subscription rules with the subscription
	// for other networks, the GitHub download mirror prefix ("" = direct)
	// and WebDAV backup.
	Shortcuts      []Shortcut     `json:",omitempty"`
	SSIDRules      []SSIDRule     `json:",omitempty"`
	SSIDDefaultSub string         `json:",omitempty"`
	DownloadMirror string         `json:",omitempty"`
	WebDAV         WebDAVSettings `json:",omitempty"`

	// Advanced holds the core options of 设置 › 高级 (advanced.go); zero
	// values are not written to the config.
	Advanced AdvancedSettings `json:",omitempty"`
}

// Overrides holds the YAML override texts.
type Overrides struct {
	Global string
	PerSub map[string]string // by subscription ID
	// List is the ordered override list (model/override.go); Global and
	// PerSub are the legacy entries of it (IDs "global" and a subscription's
	// ID), kept for settings files written by older versions.
	List []Override `json:",omitempty"`
}

// DiffLine is one line of an override preview diff.
type DiffLine struct {
	Op   byte // ' ', '+', '-'
	Text string
}

// RuntimeLine is one line of the runtime config with its source layer.
type RuntimeLine struct {
	Text   string
	Source string // "订阅", "订阅覆写", "全局覆写", "自定义规则", "澜流设置", "" = same as previous
}

// CoreDownload is the progress of a core download or update
// (API.DownloadCore), published through TopicCoreDownload.
type CoreDownload struct {
	Active  bool   // a download/update is in progress
	Phase   string // "下载", "校验", "安装", "启动"; "" when idle
	Version string // release being installed, e.g. "v1.19.33"
	Bytes   int64  // bytes received so far
	Total   int64  // archive size, 0 = unknown
	Error   string // last failure, cleared when a new download starts
	Done    string // version installed by the last successful run
}

// SettingsExport is the portable settings file written by
// API.ExportSettings and read by API.ImportSettings. Subscriptions are
// included only when the user asks, because their URLs carry tokens.
type SettingsExport struct {
	App           string                 `json:"app"`    // "lanflow"
	Format        int                    `json:"format"` // 1
	ExportedAt    time.Time              `json:"exportedAt"`
	Settings      *Settings              `json:"settings,omitempty"`
	Overrides     *Overrides             `json:"overrides,omitempty"`
	CustomRules   []CustomRule           `json:"customRules,omitempty"`
	Subscriptions []ExportedSubscription `json:"subscriptions,omitempty"`
}

// ExportedSubscription is a URL subscription in a SettingsExport.
type ExportedSubscription struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	UserAgent       string `json:"userAgent,omitempty"`
	ViaProxy        bool   `json:"viaProxy,omitempty"`
	AutoUpdateHours int    `json:"autoUpdateHours,omitempty"`
}

// ATrustState is a running Sangfor aTrust client found beside Lanflow and
// what the 兼容 mode keeps out of Lanflow. Zero = not running (or the
// setting is off).
type ATrustState struct {
	Running    bool
	Processes  []string
	Interfaces []string // aTrust's tunnel interfaces (macOS)
	CIDRs      []string // intranet ranges sent DIRECT and kept out of the TUN
	Domains    []string // intranet domain suffixes sent DIRECT, never fake-ip
	DNS        []string // aTrust's DNS servers
	Gateways   []string // addresses aTrust's own processes are connected to
	// Applied: the coexistence layer is in the running config (aTrust runs,
	// or the configured lists apply always).
	Applied bool
}
