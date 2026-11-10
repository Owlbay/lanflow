package model

// Leak-check item names (LeakItem.Name) of the structured checks. The DNS
// and IPv6 items keep their bare names; these are the keys the UI matches
// on when it needs to (the displayed name is translated by the service).
const (
	LeakItemWebRTC = "WebRTC" // STUN mapped address vs. proxy exit / real IP
	LeakItemIPType = "iptype" // ASN / organisation and data-centre vs. ISP
	LeakItemDNSCmp = "dnscmp" // core vs. system vs. public resolver answers
)

// LeakRow is one line of a LeakItem's structured detail: a short label
// ("出口", "系统解析器"…) and its value, shown as a compact table under
// the summary.
type LeakRow struct {
	Label string
	Value string
}

// IP kinds (classifyIP in internal/app): what an address's network looks like.
const (
	IPKindDatacenter  = "datacenter"  // cloud / hosting / CDN: easy to spot as a proxy
	IPKindResidential = "residential" // a consumer ISP or mobile carrier
	IPKindUnknown     = "unknown"     // no ASN data, or neither list matched
)
