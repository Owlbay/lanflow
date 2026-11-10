package app

import (
	"lanflow/internal/model"
	"lanflow/internal/seqring"
)

// Incremental reads of the large lists (model/page.go): the window in its
// own process keeps a copy and asks only for what changed.

func newConnVersions() *seqring.Versions[model.Connection] {
	return seqring.NewVersions(func(c model.Connection) string { return c.ID }, model.SameConnection)
}

// connDelta answers ConnectionsSince from the versions of conns.
func connDelta(v *seqring.Versions[model.Connection], conns []model.Connection, since uint64) model.ConnDelta {
	if v == nil {
		return model.ConnDelta{Full: true, Changed: copyConns(conns)}
	}
	d := v.Since(conns, since)
	return model.ConnDelta{Cursor: d.Cursor, Full: d.Full, Changed: copyConns(d.Changed), Removed: d.Removed}
}

// AppLogsSince implements API.AppLogsSince.
func (s *Service) AppLogsSince(since uint64, limit int) model.LogPage {
	return s.alog.Since(since, limit)
}
