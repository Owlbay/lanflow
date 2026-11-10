package model

// Incremental reads of the large lists (API.ConnectionsSince,
// ClosedConnectionsSince, LogsSince, AppLogsSince), for the window in its
// own process: it keeps a copy and asks only for what it has not seen
// (docs/service-split.md §3 大数据). Cursor is opaque: pass back the one of
// the previous page, 0 for a first read.

// LogPage is a page of log entries.
type LogPage struct {
	Entries []LogEntry // oldest first
	Cursor  uint64
	Reset   bool // the copy held cannot be continued: replace it with Entries
	More    bool // more entries follow: read again with Cursor
}

// ConnPage is a page of closed connections.
type ConnPage struct {
	Items  []Connection // oldest first (ClosedConnections lists newest first)
	Cursor uint64
	Reset  bool
	More   bool
}

// ConnDelta is what changed in the active connections.
type ConnDelta struct {
	Cursor  uint64
	Full    bool         // Changed is the whole list: replace the copy held
	Changed []Connection // new or changed connections, in list order
	Removed []string     // IDs of the connections gone since
}

// SameConnection reports whether two snapshots of a connection show the
// same thing (for ConnectionsSince).
func SameConnection(a, b Connection) bool {
	if a.Upload != b.Upload || a.Download != b.Download || a.UpSpeed != b.UpSpeed || a.DownSpeed != b.DownSpeed ||
		a.Host != b.Host || a.DstIP != b.DstIP || a.Network != b.Network || a.Type != b.Type ||
		a.Process != b.Process || a.ProcessPath != b.ProcessPath || a.Rule != b.Rule || a.RulePayload != b.RulePayload ||
		a.SourceIP != b.SourceIP || a.SourcePort != b.SourcePort || a.RemoteDestination != b.RemoteDestination ||
		a.CaptureLeg != b.CaptureLeg || !a.Start.Equal(b.Start) || !a.ClosedAt.Equal(b.ClosedAt) || len(a.Chains) != len(b.Chains) {
		return false
	}
	for i := range a.Chains {
		if a.Chains[i] != b.Chains[i] {
			return false
		}
	}
	return true
}
