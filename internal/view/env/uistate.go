package env

import (
	"log"
	"sync"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/model"
)

// UIStateSaver saves the remembered presentation state (API.UIState) for
// every page through one writer: a page hands it a change under a key,
// a later change under the same key replaces the earlier one, and after
// delay without new changes one goroutine reads the state, applies every
// pending change and saves it once. Toggling a few groups open in a row
// is then one write, and two pages saving at once cannot undo each
// other's fields, as two read-modify-write jobs racing would.
type UIStateSaver struct {
	api   app.API
	delay time.Duration

	mu      sync.Mutex
	pending map[string]func(*model.UIState)
	order   []string // keys in the order first changed, applied so
	timer   *time.Timer
	kick    chan struct{}
	done    chan struct{}
	closed  bool
}

// NewUIStateSaver starts a saver writing to api delay after the last change.
func NewUIStateSaver(api app.API, delay time.Duration) *UIStateSaver {
	s := &UIStateSaver{api: api, delay: delay, pending: map[string]func(*model.UIState){}, kick: make(chan struct{}, 1), done: make(chan struct{})}
	go s.loop()
	return s
}

// Save queues apply under key, replacing a pending change of that key.
func (s *UIStateSaver) Save(key string, apply func(*model.UIState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if _, ok := s.pending[key]; !ok {
		s.order = append(s.order, key)
	}
	s.pending[key] = apply
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(s.delay, s.signal)
}

func (s *UIStateSaver) signal() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// loop is the one writer.
func (s *UIStateSaver) loop() {
	defer close(s.done)
	for range s.kick {
		s.write()
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			s.write() // what came in while the last write ran
			return
		}
	}
}

// write applies the pending changes to the stored state and saves it.
func (s *UIStateSaver) write() {
	s.mu.Lock()
	pending, order := s.pending, s.order
	s.pending, s.order = map[string]func(*model.UIState){}, nil
	s.mu.Unlock()
	if len(order) == 0 {
		return
	}
	// Applied under the settings lock, so a page saving through the API
	// directly (or another window) is never overwritten by a stale copy.
	err := s.api.UpdateUIState(func(u *model.UIState) {
		for _, k := range order {
			pending[k](u)
		}
	})
	if err != nil {
		log.Printf("lanflow: save UI state: %v", err)
	}
}

// Close writes what is pending at once and stops the writer.
func (s *UIStateSaver) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
	s.signal()
	<-s.done
}

// SaveUIState saves a change of the UI state: through e.UIState, the
// shell's debounced single writer, or at once with Run where an Env has
// none (tests).
func (e *Env) SaveUIState(key string, apply func(*model.UIState)) {
	if e.UIState != nil {
		e.UIState.Save(key, apply)
		return
	}
	e.Run(func() error {
		u := e.API.UIState()
		apply(&u)
		return e.API.SaveUIState(u)
	}, "")
}
