package model

import "time"

// StatsDimension names one way of grouping the traffic statistics
// (API.TrafficStats; 流量统计).
type StatsDimension string

const (
	StatsDevice  StatsDimension = "device"  // source IP (127.0.0.1 = 本机, LAN addresses = other devices)
	StatsDomain  StatsDimension = "domain"  // registrable domain (eTLD+1)
	StatsHost    StatsDimension = "host"    // full host name
	StatsProcess StatsDimension = "process" // process name
	StatsRule    StatsDimension = "rule"    // rule type + payload
	StatsProxy   StatsDimension = "proxy"   // proxy group and final node
	StatsCountry StatsDimension = "country" // destination country (ISO code), "" when unknown
	StatsInbound StatsDimension = "inbound" // inbound type (HTTP, Socks5, Tun…)
)

// StatsDimensions lists every dimension in storage order.
var StatsDimensions = []StatsDimension{StatsDevice, StatsDomain, StatsHost, StatsProcess, StatsRule, StatsProxy, StatsCountry, StatsInbound}

// Range keys API.TrafficStats accepts.
const (
	StatsToday = "today"
	StatsWeek  = "week"
	StatsMonth = "month"
	StatsAll   = "all"
)

// StatsOtherKey is the key of the remainder bucket that holds what fell
// off a day's per-dimension cap (shown as 「其他」).
const StatsOtherKey = "\x00other"

// StatsBucket is one row of a dimension: everything with the same key.
type StatsBucket struct {
	Key      string // the grouping value (IP, domain, process name, "Rule,Payload", "Group/Node", country code, inbound type)
	Detail   string // a secondary value: process path, registrable domain of a host, final node of a proxy group…
	Requests int64  // connections seen
	Upload   int64  // bytes
	Download int64
	LastSeen time.Time
}

// Total is Upload + Download.
func (b StatsBucket) Total() int64 { return b.Upload + b.Download }

// StatsPoint is one bar of the time series.
type StatsPoint struct {
	Time     time.Time // start of the hour or day
	Upload   int64
	Download int64
	Requests int64
}

// StatsTotals sums a range.
type StatsTotals struct {
	Upload, Download int64
	Requests         int64
	Devices          int // distinct source addresses
}

// TrafficStats is the 流量统计 of one range (API.TrafficStats).
type TrafficStats struct {
	Range  string    // StatsToday, StatsWeek, StatsMonth or StatsAll
	Since  time.Time // start of the range (zero for StatsAll without data)
	Totals StatsTotals
	// Series is hourly for StatsToday (24 points, the whole day) and daily
	// otherwise (the last 30 days, oldest first).
	Series []StatsPoint
	Hourly bool
	// Buckets per dimension, biggest total first.
	Buckets map[StatsDimension][]StatsBucket
	// Days is how many days of history exist on disk / in memory.
	Days int
}
