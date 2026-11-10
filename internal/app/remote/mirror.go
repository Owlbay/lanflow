package remote

import (
	"slices"
	"sync"
	"sync/atomic"

	"lanflow/internal/app"
	"lanflow/internal/model"
)

// The window's copy of the large lists (docs/service-split.md §3 大数据):
// Connections, ClosedConnections, Logs and AppLogs are kept current with
// ConnectionsSince, ClosedConnectionsSince, LogsSince and AppLogsSince
// after each "changed" event, instead of the whole list every second.

// Capacities of the copies, as the service keeps them.
const (
	mirrorClosed = 500
	mirrorLogs   = 1000
	mirrorPage   = 500 // entries per page
)

type mirror struct {
	// connsGen and logsGen count the events of the lists' topics. They
	// are atomic: events arrive on the connection's reading goroutine,
	// which must never wait for mu while a read holds it for a call.
	connsGen, logsGen atomic.Uint64

	mu sync.Mutex // the copies; held across the calls that refresh them

	conns       []model.Connection
	connsCursor uint64
	connsSeen   uint64 // connsGen when conns was last refreshed

	closed       []model.Connection // newest first
	closedCursor uint64
	closedSeen   uint64

	logs, appLogs             []model.LogEntry // oldest first
	logsCursor, appLogsCursor uint64
	logsSeen, appLogsSeen     uint64
}

func newMirror() *mirror {
	m := &mirror{}
	m.reset()
	return m
}

func (m *mirror) serves(method string) bool {
	switch method {
	case "Connections", "ClosedConnections", "Logs", "AppLogs":
		return true
	}
	return false
}

// reset forgets the copies (a new connection: maybe a new service).
func (m *mirror) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns, m.closed, m.logs, m.appLogs = nil, nil, nil, nil
	m.connsCursor, m.closedCursor, m.logsCursor, m.appLogsCursor = 0, 0, 0, 0
	// Everything is stale: the next read of each list starts over.
	m.connsSeen, m.closedSeen = m.connsGen.Add(1)-1, m.connsGen.Load()-1
	m.logsSeen, m.appLogsSeen = m.logsGen.Add(1)-1, m.logsGen.Load()-1
}

func (m *mirror) invalidate(t app.Topic) {
	switch t {
	case app.TopicConns:
		m.connsGen.Add(1)
	case app.TopicLogs:
		m.logsGen.Add(1)
	}
}

// read serves method into outs[0], fetching what changed first.
func (m *mirror) read(cl *Client, method string, outs []any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	switch method {
	case "Connections":
		if g := m.connsGen.Load(); g != m.connsSeen {
			if err = m.fetchConns(cl); err == nil {
				m.connsSeen = g
			}
		}
		*outs[0].(*[]model.Connection) = slices.Clone(m.conns)
	case "ClosedConnections":
		if g := m.connsGen.Load(); g != m.closedSeen {
			if err = m.fetchClosed(cl); err == nil {
				m.closedSeen = g
			}
		}
		*outs[0].(*[]model.Connection) = slices.Clone(m.closed)
	case "Logs":
		if g := m.logsGen.Load(); g != m.logsSeen {
			if m.logs, m.logsCursor, err = fetchLogs(cl, "LogsSince", m.logs, m.logsCursor); err == nil {
				m.logsSeen = g
			}
		}
		*outs[0].(*[]model.LogEntry) = slices.Clone(m.logs)
	case "AppLogs":
		if g := m.logsGen.Load(); g != m.appLogsSeen {
			if m.appLogs, m.appLogsCursor, err = fetchLogs(cl, "AppLogsSince", m.appLogs, m.appLogsCursor); err == nil {
				m.appLogsSeen = g
			}
		}
		*outs[0].(*[]model.LogEntry) = slices.Clone(m.appLogs)
	}
	return err
}

func (m *mirror) fetchConns(cl *Client) error {
	var d model.ConnDelta
	if err := cl.raw("ConnectionsSince", []any{m.connsCursor}, &d); err != nil {
		return err
	}
	m.conns = ApplyConnDelta(m.conns, d)
	m.connsCursor = d.Cursor
	return nil
}

func (m *mirror) fetchClosed(cl *Client) error {
	for {
		var p model.ConnPage
		if err := cl.raw("ClosedConnectionsSince", []any{m.closedCursor, mirrorPage}, &p); err != nil {
			return err
		}
		m.closed = ApplyClosedPage(m.closed, p, mirrorClosed)
		m.closedCursor = p.Cursor
		if !p.More {
			return nil
		}
	}
}

func fetchLogs(cl *Client, method string, have []model.LogEntry, cursor uint64) ([]model.LogEntry, uint64, error) {
	for {
		var p model.LogPage
		if err := cl.raw(method, []any{cursor, mirrorPage}, &p); err != nil {
			return have, cursor, err
		}
		have = ApplyLogPage(have, p, mirrorLogs)
		cursor = p.Cursor
		if !p.More {
			return have, cursor, nil
		}
	}
}

// ApplyConnDelta returns list (the active connections) updated by d.
func ApplyConnDelta(list []model.Connection, d model.ConnDelta) []model.Connection {
	if d.Full {
		return slices.Clone(d.Changed)
	}
	if len(d.Removed) > 0 {
		gone := make(map[string]bool, len(d.Removed))
		for _, id := range d.Removed {
			gone[id] = true
		}
		list = slices.DeleteFunc(list, func(c model.Connection) bool { return gone[c.ID] })
	}
	if len(d.Changed) > 0 {
		at := make(map[string]int, len(list))
		for i, c := range list {
			at[c.ID] = i
		}
		for _, c := range d.Changed {
			if i, ok := at[c.ID]; ok {
				list[i] = c
			} else {
				at[c.ID] = len(list)
				list = append(list, c)
			}
		}
	}
	return list
}

// ApplyClosedPage returns list (closed connections, newest first) with
// the page added, at most max entries.
func ApplyClosedPage(list []model.Connection, p model.ConnPage, max int) []model.Connection {
	if p.Reset {
		list = nil
	}
	if len(p.Items) == 0 {
		return list
	}
	add := slices.Clone(p.Items)
	slices.Reverse(add) // newest first
	list = append(add, list...)
	if len(list) > max {
		list = list[:max]
	}
	return list
}

// ApplyLogPage returns list (oldest first) with the page added, keeping
// the newest max entries.
func ApplyLogPage(list []model.LogEntry, p model.LogPage, max int) []model.LogEntry {
	if p.Reset {
		list = nil
	}
	list = append(list, p.Entries...)
	if len(list) > max {
		list = slices.Clone(list[len(list)-max:])
	}
	return list
}
