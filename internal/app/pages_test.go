package app

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"lanflow/internal/controller"
	"lanflow/internal/model"
)

// The incremental reads give, applied to a client's copy, exactly what the
// full reads give.
func TestIncrementalReads(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	var logs []model.LogEntry
	var logCur uint64
	readLogs := func() {
		for {
			p := h.s.LogsSince(logCur, 3)
			if p.Reset {
				logs = nil
			}
			logs = append(logs, p.Entries...)
			logCur = p.Cursor
			if !p.More {
				return
			}
		}
	}
	for i := range 5 {
		h.ctrl.logsC <- controller.Log{Type: "INFO", Payload: fmt.Sprint("line ", i)}
	}
	if !waitFor(func() bool { return len(h.s.Logs()) == 5 }) {
		t.Fatal("logs not recorded")
	}
	readLogs()
	if !reflect.DeepEqual(logs, h.s.Logs()) {
		t.Fatalf("logs %v, want %v", logs, h.s.Logs())
	}
	h.ctrl.logsC <- controller.Log{Type: "ERROR", Payload: "six"}
	if !waitFor(func() bool { return len(h.s.Logs()) == 6 }) {
		t.Fatal("log not recorded")
	}
	if p := h.s.LogsSince(logCur, 0); p.Reset || len(p.Entries) != 1 || p.Entries[0].Payload != "six" {
		t.Fatalf("only the new line: %+v", p)
	}
	h.s.ClearLogs()
	readLogs()
	if len(logs) != 0 {
		t.Fatalf("cleared logs still held: %v", logs)
	}

	// Active connections: the delta carries only what moved.
	conn := func(id string, up int64) controller.Connection {
		return controller.Connection{ID: id, Upload: up, Chains: []string{"东京 03", "节点选择"}}
	}
	var active []model.Connection
	var connCur uint64
	readConns := func() model.ConnDelta {
		d := h.s.ConnectionsSince(connCur)
		active = applyDelta(active, d)
		connCur = d.Cursor
		return d
	}
	h.ctrl.connsC <- controller.Connections{Connections: []controller.Connection{conn("a", 1), conn("b", 1)}}
	if !waitFor(func() bool { return len(h.s.Connections()) == 2 }) {
		t.Fatal("connections not applied")
	}
	if d := readConns(); !d.Full {
		t.Fatalf("first read should be full: %+v", d)
	}
	h.ctrl.connsC <- controller.Connections{Connections: []controller.Connection{conn("a", 1), conn("b", 9), conn("c", 1)}}
	if !waitFor(func() bool { return len(h.s.Connections()) == 3 }) {
		t.Fatal("connections not applied")
	}
	d := readConns()
	ids := func(cs []model.Connection) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.ID)
		}
		slices.Sort(out)
		return out
	}
	if d.Full || !slices.Equal(ids(d.Changed), []string{"b", "c"}) {
		t.Fatalf("delta: %+v", d)
	}
	if !sameByID(active, h.s.Connections()) {
		t.Fatalf("copy %v differs from %v", active, h.s.Connections())
	}
	h.ctrl.connsC <- controller.Connections{Connections: []controller.Connection{conn("c", 1)}}
	if !waitFor(func() bool { return len(h.s.Connections()) == 1 }) {
		t.Fatal("connections not applied")
	}
	d = readConns()
	if slices.Sort(d.Removed); !slices.Equal(d.Removed, []string{"a", "b"}) {
		t.Fatalf("removed: %+v", d)
	}
	if !sameByID(active, h.s.Connections()) {
		t.Fatalf("copy %v differs from %v", active, h.s.Connections())
	}
	// Closed connections arrive page by page, oldest first.
	p := h.s.ClosedConnectionsSince(0, 1)
	if !p.Reset || !p.More || len(p.Items) != 1 {
		t.Fatalf("closed page 1: %+v", p)
	}
	p2 := h.s.ClosedConnectionsSince(p.Cursor, 0)
	got := append(p.Items, p2.Items...)
	slices.Reverse(got)
	if p2.Reset || p2.More || !sameByID(got, h.s.ClosedConnections()) {
		t.Fatalf("closed pages %+v %+v vs %v", p, p2, h.s.ClosedConnections())
	}
	// Lanflow's own log.
	ap := h.s.AppLogsSince(0, 0)
	if !reflect.DeepEqual(ap.Entries, h.s.AppLogs()) || !ap.Reset {
		t.Fatalf("app logs: %d vs %d", len(ap.Entries), len(h.s.AppLogs()))
	}
}

func applyDelta(list []model.Connection, d model.ConnDelta) []model.Connection {
	if d.Full {
		return slices.Clone(d.Changed)
	}
	list = slices.DeleteFunc(list, func(c model.Connection) bool { return slices.Contains(d.Removed, c.ID) })
	for _, c := range d.Changed {
		if i := slices.IndexFunc(list, func(x model.Connection) bool { return x.ID == c.ID }); i >= 0 {
			list[i] = c
		} else {
			list = append(list, c)
		}
	}
	return list
}

func sameByID(a, b []model.Connection) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]model.Connection{}
	for _, c := range a {
		m[c.ID] = c
	}
	for _, c := range b {
		if x, ok := m[c.ID]; !ok || !model.SameConnection(x, c) {
			return false
		}
	}
	return true
}
