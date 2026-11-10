package app

import (
	"sync"
	"time"
)

// coalesceWindow is the minimum spacing between two callbacks for one topic.
const coalesceWindow = 100 * time.Millisecond

// emitter fans out change notifications to OnChange listeners, delivering
// at most one callback per topic per coalesceWindow: the first emission of
// a topic is delivered at once; emissions during the window are merged into
// one trailing callback when the window ends.
type emitter struct {
	mu        sync.Mutex
	listeners map[int]func(Topic)
	next      int
	pending   map[Topic]bool // emission arrived during the window
	timers    map[Topic]*time.Timer
	closed    bool
}

func newEmitter() *emitter {
	return &emitter{listeners: map[int]func(Topic){}, pending: map[Topic]bool{}, timers: map[Topic]*time.Timer{}}
}

func (e *emitter) add(fn func(Topic)) func() {
	e.mu.Lock()
	id := e.next
	e.next++
	e.listeners[id] = fn
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		delete(e.listeners, id)
		e.mu.Unlock()
	}
}

// emit records a change of each topic and delivers it, coalesced.
func (e *emitter) emit(topics ...Topic) {
	var fire []Topic
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	for _, t := range topics {
		if _, inWindow := e.timers[t]; inWindow {
			e.pending[t] = true
			continue
		}
		e.timers[t] = time.AfterFunc(coalesceWindow, func() { e.windowEnd(t) })
		fire = append(fire, t)
	}
	fns := e.snapshot()
	e.mu.Unlock()
	for _, t := range fire {
		for _, fn := range fns {
			fn(t)
		}
	}
}

func (e *emitter) windowEnd(t Topic) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	if !e.pending[t] {
		delete(e.timers, t)
		e.mu.Unlock()
		return
	}
	delete(e.pending, t)
	e.timers[t] = time.AfterFunc(coalesceWindow, func() { e.windowEnd(t) })
	fns := e.snapshot()
	e.mu.Unlock()
	for _, fn := range fns {
		fn(t)
	}
}

// snapshot copies the listeners; call with mu held.
func (e *emitter) snapshot() []func(Topic) {
	fns := make([]func(Topic), 0, len(e.listeners))
	for _, fn := range e.listeners {
		fns = append(fns, fn)
	}
	return fns
}

// close stops pending timers; later emissions are dropped.
func (e *emitter) close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for t, tm := range e.timers {
		tm.Stop()
		delete(e.timers, t)
	}
}
