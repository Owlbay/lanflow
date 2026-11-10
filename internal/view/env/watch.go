package env

import (
	"sync/atomic"

	"lanflow/internal/app"
)

// Stale tells a page when values it keeps between builds must be read
// again: it is set when one of the topics it watches changes (from any
// goroutine) and starts set, so the first build reads them.
type Stale struct{ v atomic.Bool }

// Watch returns a Stale set by changes of topics on api.
func Watch(api app.API, topics ...app.Topic) *Stale {
	s := &Stale{}
	s.v.Store(true)
	if api != nil {
		api.OnChange(func(t app.Topic) {
			for _, x := range topics {
				if t == x {
					s.v.Store(true)
					return
				}
			}
		})
	}
	return s
}

// Take reports whether the values must be read again and clears it.
func (s *Stale) Take() bool { return s.v.Swap(false) }

// Mark sets it, as for a change the topics do not announce.
func (s *Stale) Mark() { s.v.Store(true) }
