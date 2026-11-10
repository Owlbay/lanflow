package fake

import (
	"slices"

	"lanflow/internal/model"
	"lanflow/internal/seqring"
)

// The incremental reads (model/page.go) over the fake's lists.

func (f *Fake) ConnectionsSince(since uint64) model.ConnDelta {
	conns := f.Connections()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connVer == nil {
		f.connVer = seqring.NewVersions(func(c model.Connection) string { return c.ID }, model.SameConnection)
	}
	f.connVer.Update(conns)
	d := f.connVer.Since(conns, since)
	return model.ConnDelta{Cursor: d.Cursor, Full: d.Full, Changed: d.Changed, Removed: d.Removed}
}

func (f *Fake) ClosedConnectionsSince(since uint64, limit int) model.ConnPage {
	f.mu.Lock()
	defer f.mu.Unlock()
	oldest := slices.Clone(f.closed)
	slices.Reverse(oldest)
	p := seqring.SliceSince(oldest, f.closedTotal, 0, since, limit)
	return model.ConnPage{Items: p.Items, Cursor: p.Cursor, Reset: p.Reset, More: p.More}
}

func (f *Fake) LogsSince(since uint64, limit int) model.LogPage {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := seqring.SliceSince(f.logs, f.logTotal, f.logEpoch, since, limit)
	return model.LogPage{Entries: p.Items, Cursor: p.Cursor, Reset: p.Reset, More: p.More}
}

// AppLogsSince numbers the four sample entries 1–4; clearing them is a
// fifth number.
func (f *Fake) AppLogsSince(since uint64, limit int) model.LogPage {
	entries := f.AppLogs()
	total, epoch := uint64(len(entries)), uint64(0)
	if entries == nil {
		total, epoch = 5, 5
	}
	p := seqring.SliceSince(entries, total, epoch, since, limit)
	return model.LogPage{Entries: p.Items, Cursor: p.Cursor, Reset: p.Reset, More: p.More}
}
