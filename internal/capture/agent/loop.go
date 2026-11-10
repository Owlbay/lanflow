package agent

import (
	"bufio"
	"bytes"
	"net"
	"sync"
	"time"
)

// loopGuard is the agent's loop breaker (docs/capture.md §5.2 step 4).
// A subscription that routes the return listener back into the decrypting
// outbound sends the agent its own upstream connection again: the bytes
// that arrive are exactly the first flight the agent just wrote upstream.
// For TLS that flight carries a 32-byte random no other client repeats, so
// a single match is a loop; plaintext HTTP can legitimately repeat, so it
// takes two matches of the same prefix. A per-target rate limit backs it
// up (loopMax accepts within loopWindow; measured loop: about 300 nested
// connections per second, docs/capture.md §11 M0).
type loopGuard struct {
	mu     sync.Mutex
	own    map[string]*ownEntry
	rate   map[string][]time.Time
	window time.Duration
	max    int
	ttl    time.Duration
}

type ownEntry struct {
	at   time.Time
	hits int
}

const (
	loopPrefix = 64
	loopMinLen = 16
)

func newLoopGuard(window time.Duration, max int) *loopGuard {
	if window <= 0 {
		window = time.Second
	}
	if max <= 0 {
		max = 100
	}
	return &loopGuard{own: map[string]*ownEntry{}, rate: map[string][]time.Time{}, window: window, max: max, ttl: 10 * time.Second}
}

func prefixKey(b []byte) string {
	if len(b) > loopPrefix {
		b = b[:loopPrefix]
	}
	return string(b)
}

// markOwn records the first flight the agent is about to write upstream.
func (g *loopGuard) markOwn(b []byte) {
	if len(b) < loopMinLen {
		return
	}
	k := prefixKey(b)
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if e, ok := g.own[k]; ok && now.Sub(e.at) < g.ttl {
		e.at = now
		return
	}
	g.own[k] = &ownEntry{at: now}
	if len(g.own) > 4096 {
		for k, e := range g.own {
			if now.Sub(e.at) > g.ttl {
				delete(g.own, k)
			}
		}
	}
}

// isOwn reports whether the first bytes of an accepted connection are a
// flight the agent wrote itself (tls: a ClientHello).
func (g *loopGuard) isOwn(br *bufio.Reader, tls bool) bool {
	n := br.Buffered()
	if n < loopMinLen {
		return false
	}
	b, _ := br.Peek(min(n, loopPrefix))
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for k, e := range g.own {
		if now.Sub(e.at) > g.ttl {
			continue
		}
		kb := []byte(k)
		m := min(len(kb), len(b))
		if m < loopMinLen || !bytes.Equal(kb[:m], b[:m]) {
			continue
		}
		if tls {
			return len(b) >= 44 && m >= 44 // header + version + random compared
		}
		e.hits++
		return e.hits >= 2
	}
	return false
}

// tooFast counts an accept for target and reports a rate beyond max.
func (g *loopGuard) tooFast(target string) bool {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	ts := g.rate[target]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) > g.window {
		i++
	}
	ts = append(ts[i:], now)
	g.rate[target] = ts
	if len(g.rate) > 4096 {
		for k, v := range g.rate {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > g.window {
				delete(g.rate, k)
			}
		}
	}
	return len(ts) > g.max
}

// firstWrite remembers the first Write on a connection with the guard.
type firstWrite struct {
	net.Conn
	g    *loopGuard
	once sync.Once
}

func (f *firstWrite) Write(b []byte) (int, error) {
	f.once.Do(func() { f.g.markOwn(b) })
	return f.Conn.Write(b)
}

// pinTable implements the temporary skip list of certificate pinning
// (docs/capture.md §5.4): two explicit certificate alerts from the client
// for the same (selector, SNI) within five minutes skip it for an hour.
type pinTable struct {
	mu    sync.Mutex
	fails map[string][]time.Time
	skip  map[string]time.Time
}

const (
	pinWindow = 5 * time.Minute
	pinSkip   = time.Hour
)

func newPinTable() *pinTable {
	return &pinTable{fails: map[string][]time.Time{}, skip: map[string]time.Time{}}
}

func (p *pinTable) skipped(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.skip[key]
	if ok && time.Now().After(t) {
		delete(p.skip, key)
		return false
	}
	return ok
}

// fail counts a rejection; it returns true when key is now skipped.
func (p *pinTable) fail(key string) bool {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	var keep []time.Time
	for _, t := range p.fails[key] {
		if now.Sub(t) < pinWindow {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	p.fails[key] = keep
	if len(keep) >= 2 {
		p.skip[key] = now.Add(pinSkip)
		delete(p.fails, key)
		return true
	}
	return false
}

func (p *pinTable) list() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for k := range p.skip {
		out = append(out, k)
	}
	return out
}
