package model

import "time"

// HTTPS 选择性解密 (docs/capture.md). The settings live in their own file
// (<DataDir>/capture/settings.json), not in Settings: they never sync and
// never reach backups.

// CaptureSettings is what the user chose to decrypt.
type CaptureSettings struct {
	// Domains: "example.com" (exact), "*.example.com" (subdomains only),
	// "+.example.com" (the domain and its subdomains).
	Domains []string `json:",omitempty"`
	// Apps select by program (M2); each gets its own outbound credentials
	// so a request is attributed to the app exactly.
	Apps []CaptureApp `json:",omitempty"`
	// Bypass is the user's skip list, in the same syntax as Domains or an
	// IP-CIDR; it comes on top of the built-in list (CaptureBuiltinBypass).
	Bypass []string `json:",omitempty"`
	// AllowPrivate lists selections (patterns as in Domains) that may be
	// decrypted although their destination is an intranet address or name:
	// the built-in private-network entries (CapturePrivateBypass) do not
	// apply to them. The user's own Bypass still does.
	AllowPrivate []string `json:",omitempty"`
	// DisabledBuiltin lists built-in bypass entries the user turned off.
	DisabledBuiltin []string `json:",omitempty"`
	// Ports are the destination ports decrypted, "/"-separated; "" = 80/443/8443.
	Ports string `json:",omitempty"`
	// AutoOffMin turns decryption off after that many minutes; 0 = never
	// (the UI asks first), negative = the default (30).
	AutoOffMin int
	// NoRecord pauses the request list: the 抓包 page stops following
	// connections and the agent relays without decrypting or storing.
	NoRecord bool `json:",omitempty"`
	// Acknowledged is set once the first-run explanation was read.
	Acknowledged bool `json:",omitempty"`
	// LastDisabled is when decryption was last turned off; seven days
	// later the root's trust and key are removed (docs/capture.md §6).
	LastDisabled time.Time `json:",omitempty"`
}

// CaptureApp is one program selection (M2).
type CaptureApp struct {
	Name string // shown in the UI
	// Kind is "path" (Match is a PROCESS-PATH-WILDCARD, e.g.
	// "/Applications/Foo.app/*" so helpers are covered) or "name"
	// (PROCESS-NAME).
	Kind  string
	Match string
}

// Capture defaults.
const (
	CaptureDefaultPorts   = "80/443/8443"
	CaptureDefaultAutoOff = 30
)

// CaptureBuiltinBypass is the built-in skip list (docs/capture.md §4.2):
// entries can be turned off one by one (DisabledBuiltin) but not removed.
var CaptureBuiltinBypass = []string{
	// system updates and certificates
	"+.apple.com", "+.icloud.com", "+.mzstatic.com", "+.apple-cloudkit.com", "+.push.apple.com",
	"+.windowsupdate.com", "+.update.microsoft.com", "+.delivery.mp.microsoft.com", "+.digicert.com",
	"ocsp.*", "crl.*",
	// identity and password managers
	"login.microsoftonline.com", "+.okta.com", "+.1password.com", "+.bitwarden.com", "+.lastpass.com",
	// banks and payment
	"+.alipay.com", "+.95516.com", "+.paypal.com", "+.stripe.com",
	// private networks (CapturePrivateBypass)
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "fc00::/7", "fe80::/10",
	"+.local", "+.lan", "+.corp",
}

// CapturePrivateBypass is the private-network part of the built-in list:
// a selection listed in CaptureSettings.AllowPrivate is exempt from it.
var CapturePrivateBypass = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "fc00::/7", "fe80::/10",
	"+.local", "+.lan", "+.corp",
}

// IsCapturePrivate reports whether a built-in bypass entry is a
// private-network one.
func IsCapturePrivate(entry string) bool {
	for _, p := range CapturePrivateBypass {
		if p == entry {
			return true
		}
	}
	return false
}

// Reasons of a target check (CaptureCheck.Reason), stable for scripts.
const (
	CaptureOK                  = "OK"
	CaptureReasonDisabled      = "CAPTURE_DISABLED"
	CaptureReasonPaused        = "CAPTURE_PAUSED"
	CaptureReasonNotMatched    = "TARGET_NOT_MATCHED"
	CaptureReasonPortNotIn     = "PORT_NOT_SELECTED"
	CaptureReasonUserBypass    = "USER_BYPASS"
	CaptureReasonBuiltinBypass = "BUILTIN_BYPASS"
	CaptureReasonPrivate       = "PRIVATE_DESTINATION_EXCLUDED"
	CaptureReasonNotEntering   = "TRAFFIC_NOT_ENTERING_CAPTURE"
	CaptureReasonDNS           = "DNS_FAILED"
	CaptureReasonUpConnect     = "UPSTREAM_CONNECT_FAILED"
	CaptureReasonUpTLS         = "UPSTREAM_TLS_FAILED"
	CaptureReasonClientTLS     = "CLIENT_TLS_REJECTED"
	CaptureReasonNoTraffic     = "NO_TRAFFIC_YET"
	CaptureReasonCA            = "CA_NOT_TRUSTED"
)

// CaptureCheckStep is one stage of a target check.
type CaptureCheckStep struct {
	Stage  string // 配置 / 运行状态 / 匹配 / 跳过列表 / DNS / 流量入口 / 上游连接 / 记录
	OK     bool
	Skip   bool // not run (an earlier stage decided)
	Detail string
}

// CaptureCheck explains whether a target would be captured and, if not,
// why (CheckCaptureTarget).
type CaptureCheck struct {
	Host   string
	Port   int
	Scheme string
	// Reason is the first blocking reason (Capture* constants), CaptureOK
	// when nothing blocks; Advice says what to do about it.
	Reason  string
	Summary string
	Advice  string
	Steps   []CaptureCheckStep
	// Addrs are the addresses the name resolved to (system resolver).
	Addrs []string `json:",omitempty"`
}

// CAState is the state of the local root certificate.
type CAState string

const (
	CANone      CAState = "none"      // no root generated (or it was removed)
	CAUntrusted CAState = "untrusted" // generated, the system does not trust it
	CATrusted   CAState = "trusted"   // a leaf it signs verifies against the system roots
	CAExpiring  CAState = "expiring"  // trusted but within 7 days of expiry: regenerate
)

// CaptureStatus is the cheap state read for the status bar and the page.
type CaptureStatus struct {
	Enabled bool
	// Paused: enabled, but the core is not in rule mode or not running.
	Paused      bool
	PauseReason string
	Domains     int
	Apps        int
	Until       time.Time // zero = no auto off
	CA          CAState
	CAExpires   time.Time
	CAName      string
	AgentUp     bool
	Flows       int
	// FindProcess is false when app selections exist but the core cannot
	// tell processes apart (find-process-mode off).
	FindProcess bool
	Error       string
	Settings    CaptureSettings
}

// Flow flags.
const (
	FlowDecrypted   = "decrypted"
	FlowPassthrough = "passthrough"
	FlowPinned      = "pinned"
	FlowECH         = "ech"
	FlowUpstreamBad = "upstream-invalid" // upstream certificate invalid: replayed and passed through
	FlowClientCert  = "client-cert"      // upstream asked for a client certificate: passed through
	FlowWebSocket   = "ws"
	FlowTruncated   = "truncated"
	FlowLoop        = "loop"
	FlowNotLocal    = "not-local"
	FlowUnknown     = "unknown-proto"
)

// CaptureFlow is one request (or, for connections not decrypted, one
// connection) as listed; no headers or bodies.
type CaptureFlow struct {
	ID          int64
	Start       time.Time
	DurationMs  int64
	Process     string `json:",omitempty"`
	ProcessPath string `json:",omitempty"`
	// Attribution is "certain" (the app's own credentials), "ambiguous"
	// or "unknown".
	Attribution string `json:",omitempty"`
	Selector    string `json:",omitempty"` // the selection that matched: a domain pattern or an app name
	Proto       string `json:",omitempty"` // "h1", "h2", "tls", "tcp"
	Method      string `json:",omitempty"`
	Scheme      string `json:",omitempty"`
	Host        string
	Port        int    `json:",omitempty"`
	Path        string `json:",omitempty"`
	Status      int    `json:",omitempty"`
	ContentType string `json:",omitempty"`
	ReqSize     int64
	RespSize    int64
	Flags       []string `json:",omitempty"`
	Error       string   `json:",omitempty"`
	Done        bool
}

// Has reports whether the flow carries flag.
func (f CaptureFlow) Has(flag string) bool {
	for _, x := range f.Flags {
		if x == flag {
			return true
		}
	}
	return false
}

// CaptureTLS describes the two handshakes of a decrypted connection.
type CaptureTLS struct {
	SNI          string   `json:",omitempty"`
	Version      string   `json:",omitempty"`
	Cipher       string   `json:",omitempty"`
	ALPN         string   `json:",omitempty"`
	ClientALPN   []string `json:",omitempty"`
	UpstreamCert []string `json:",omitempty"` // SHA-256 of each certificate of the upstream chain, leaf first
	ECH          bool     `json:",omitempty"` // the ClientHello carried an ECH extension (maybe GREASE)
}

// CaptureTiming is the timeline of a request, offsets from Start.
type CaptureTiming struct {
	UpstreamDialMs int64 `json:",omitempty"`
	UpstreamTLSMs  int64 `json:",omitempty"`
	ClientTLSMs    int64 `json:",omitempty"`
	TTFBMs         int64 `json:",omitempty"`
	EndMs          int64 `json:",omitempty"`
}

// CaptureFlowDetail is one flow with headers and bodies.
type CaptureFlowDetail struct {
	Flow       CaptureFlow
	ReqHeader  []KV `json:",omitempty"`
	RespHeader []KV `json:",omitempty"`
	ReqBody    []byte
	RespBody   []byte
	// ReqBodySize / RespBodySize are the full sizes; the bodies are cut
	// at the capture limit.
	ReqBodySize  int64
	RespBodySize int64
	TLS          CaptureTLS
	Timing       CaptureTiming
	WSFrames     []CaptureWSFrame `json:",omitempty"`
}

// CaptureWSFrame is one WebSocket frame (M3): text frames keep the first
// part of their payload.
type CaptureWSFrame struct {
	Time     time.Time
	FromPeer bool // server to client
	Opcode   int
	Size     int64
	Text     string `json:",omitempty"`
}

// ReplayRequest is a request to send again, edited or not (抓包 · 重新发送).
type ReplayRequest struct {
	Method string
	URL    string
	Header []KV
	Body   []byte
	// Direct skips the core (by default the request goes through its
	// mixed port, the route the original took).
	Direct bool
	// FollowRedirects follows 3xx responses (off: the 3xx is the answer).
	FollowRedirects bool
	TimeoutMs       int // 0 = 30 s
}

// ReplayResult is the answer to a ReplayRequest.
type ReplayResult struct {
	Status     int
	StatusText string
	Proto      string
	Header     []KV
	// Body is the response body, decoded (gzip / br / deflate / zstd) when
	// possible, cut at 4 MiB; BodySize is the size received.
	Body       []byte
	BodySize   int64
	Truncated  bool
	DurationMs int64
	TTFBMs     int64
	Via        string // "core" (mixed port) or "direct"
	Error      string `json:",omitempty"`
}
