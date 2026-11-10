package model

import "time"

// 检测 page (API.Probe*): what an exit looks like from the outside (IP
// 纯净度), which services it unlocks (解锁检测) and how the nodes of one
// region compare (地区探查). Every probe runs on demand only; results are
// snapshots the UI reads from ProbeState.

// ProbeDirect is the ProbeTarget of the direct connection (no node).
const ProbeDirect = "DIRECT"

// IP kinds beyond IPKindDatacenter / IPKindResidential (iptype.go):
// a cellular carrier, as an online source reports it.
const IPKindMobile = "mobile"

// Native-IP verdicts (IPReport.Native): is the address used where its
// block is registered, or announced somewhere else (广播 IP)?
const (
	NativeYes       = "native"
	NativeBroadcast = "broadcast"
	NativeUnknown   = ""
)

// RiskInfo is what a free online IP-reputation source said about an
// address. Source names the site ("ip-api.com", "ippure.com"); a field
// is only meaningful when the source reports it (Has*).
type RiskInfo struct {
	Source string
	// ip-api.com flags
	HasFlags               bool
	Proxy, Hosting, Mobile bool
	// ippure.com: fraud score 0-100 (higher = riskier), residential and
	// broadcast verdicts
	HasScore      bool
	Score         int
	Residential   bool
	Broadcast     bool
	HasBroadcast  bool
	RegionOfBlock string // ISO country the block is registered in, when told
}

// IPReport is the IP 纯净度 result of one exit.
type IPReport struct {
	Target    string // node name or ProbeDirect
	Running   bool
	Error     string // why the probe failed, "" when it did not
	IP        string
	Place     *Place // where the address is seen from (Cloudflare trace loc + colo, else the local database)
	ASN       string // "AS13335"
	Org       string // organisation (short name)
	Kind      string // IPKindDatacenter, IPKindResidential, IPKindMobile or IPKindUnknown
	Native    string // NativeYes, NativeBroadcast or NativeUnknown
	NativeWhy string // the comparison the verdict rests on, translated
	Colo      string // Cloudflare colo that answered
	DBCountry string // the local database's country for the IP ("" when unknown)
	Risk      []RiskInfo
	RiskError string   // why the online sources could not be read, "" when they were not asked or answered
	Sources   []string // every source consulted, in order ("Cloudflare trace", "GeoLite2-ASN", "ip-api.com", "ippure.com")
	CheckedAt time.Time
}

// Unlock statuses (UnlockResult.Status).
const (
	UnlockPending = "pending" // not checked yet / running
	UnlockYes     = "yes"     // 可用
	UnlockNo      = "no"      // 不可用
	UnlockPartial = "partial" // Netflix: 仅自制剧
	UnlockError   = "error"   // the check itself failed (timeout…)
)

// UnlockResult is one service's verdict through an exit.
type UnlockResult struct {
	Service string // "Netflix", "ChatGPT"…
	Status  string // UnlockYes…
	Region  string // ISO country the service places the exit in, "" when it did not say
	Note    string // short explanation, translated
	Millis  int    // how long the check took, 0 when it did not run
}

// UnlockReport is the 解锁检测 result of one exit.
type UnlockReport struct {
	Target    string
	Running   bool
	Done      int // results in
	Results   []UnlockResult
	Error     string
	CheckedAt time.Time
}

// ProbeRegion groups the nodes whose names place them in one country.
type ProbeRegion struct {
	Country string   // ISO code
	Name    string   // Chinese name
	Nodes   []string // node names in config order
}

// RegionNode is one node's row of the 地区探查 table.
type RegionNode struct {
	Node    string
	Delay   int  // ms; 0 = not tested, -1 = timeout
	Testing bool // delay test running
	Probed  bool // exit measured
	Exit    EndpointInfo
	Error   string         // why the exit could not be measured
	Unlock  []UnlockResult // nil unless the probe ran with unlock checks
}

// RegionReport is the state of the last 地区探查.
type RegionReport struct {
	Country    string
	Running    bool
	WithUnlock bool
	Done       int // nodes finished
	Nodes      []RegionNode
	Error      string
	CheckedAt  time.Time
}

// ProbeState is everything the 检测 page reads (API.Probe, TopicProbe).
type ProbeState struct {
	IP     map[string]IPReport     // by target
	Unlock map[string]UnlockReport // by target
	Region RegionReport
}
