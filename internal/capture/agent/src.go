package agent

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/core"
)

// sourceCheck asks the core which connection the agent was handed:
// Mihomo opens the outbound connection only after its tracker exists, so
// by the time the ClientHello arrives the connection is listed with its
// source and process (docs/capture.md §4.3, §9 进程归属). The candidate is
// the oldest unclaimed connection through the selector's outbound to the
// same host and port; when several match, every one must be local and the
// process must agree for the attribution to be certain.
type sourceCheck struct {
	ctrl *controller.Client

	mu      sync.Mutex
	claimed map[string]time.Time
	last    controller.Connections
	lastAt  time.Time
	fetch   *sync.WaitGroup
}

type srcResult struct {
	found       bool
	local       bool
	host        string // the name Mihomo knows the connection by (the CONNECT may carry the IP)
	process     string
	path        string
	attribution string
}

func newSourceCheck(addr string) *sourceCheck {
	return newSourceCheckDial(func(ctx context.Context) (net.Conn, error) { return core.Dial(ctx, addr) })
}

func newSourceCheckDial(dial func(ctx context.Context) (net.Conn, error)) *sourceCheck {
	return &sourceCheck{ctrl: controller.New(dial), claimed: map[string]time.Time{}}
}

// snapshot returns a connections list no older than maxAge, coalescing
// concurrent fetches.
func (s *sourceCheck) snapshot(maxAge time.Duration) controller.Connections {
	s.mu.Lock()
	if time.Since(s.lastAt) <= maxAge {
		c := s.last
		s.mu.Unlock()
		return c
	}
	if wg := s.fetch; wg != nil {
		s.mu.Unlock()
		wg.Wait()
		s.mu.Lock()
		c := s.last
		s.mu.Unlock()
		return c
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	s.fetch = wg
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	c, err := s.ctrl.Connections(ctx)
	cancel()
	s.mu.Lock()
	if err == nil {
		s.last, s.lastAt = c, time.Now()
	}
	s.fetch = nil
	c = s.last
	s.mu.Unlock()
	wg.Done()
	return c
}

func (s *sourceCheck) find(outbound, host string, port int) srcResult {
	start := time.Now()
	for i := 0; ; i++ {
		// the first look may be cached a few milliseconds; then fresh
		age := 0 * time.Millisecond
		if i == 0 {
			age = 20 * time.Millisecond
		}
		if r, ok := s.match(s.snapshot(age), outbound, host, port, start); ok {
			return r
		}
		if i >= 3 {
			return srcResult{}
		}
		time.Sleep(time.Duration(30*(i+1)) * time.Millisecond)
	}
}

func (s *sourceCheck) match(cs controller.Connections, outbound, host string, port int, start time.Time) (srcResult, bool) {
	ps := strconv.Itoa(port)
	var cands []controller.Connection
	for _, c := range cs.Connections {
		if !slices.Contains(c.Chains, outbound) || c.Metadata.DestinationPort != ps {
			continue
		}
		if !strings.EqualFold(c.Metadata.Host, host) && c.Metadata.DestinationIP != host {
			continue
		}
		if c.Start.After(start.Add(time.Second)) {
			continue
		}
		cands = append(cands, c)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, t := range s.claimed {
		if now.Sub(t) > 2*time.Minute {
			delete(s.claimed, k)
		}
	}
	var free []controller.Connection
	for _, c := range cands {
		if _, ok := s.claimed[c.ID]; !ok {
			free = append(free, c)
		}
	}
	if len(free) == 0 {
		return srcResult{}, false
	}
	slices.SortFunc(free, func(a, b controller.Connection) int { return a.Start.Compare(b.Start) })
	pick := free[0]
	s.claimed[pick.ID] = now
	r := srcResult{found: true, local: true, process: pick.Metadata.Process, path: pick.Metadata.ProcessPath, attribution: "certain",
		host: strings.ToLower(pick.Metadata.Host)}
	if r.host == "" {
		r.host = strings.ToLower(pick.Metadata.SniffHost)
	}
	for _, c := range free {
		if !isLocalSource(c.Metadata) {
			r.local = false
		}
		if c.Metadata.ProcessPath != pick.Metadata.ProcessPath || c.Metadata.Process != pick.Metadata.Process {
			r.attribution = "ambiguous"
		}
	}
	return r, true
}

// isLocalSource: loopback, or the TUN (whose sources are this machine's
// unless it is shared as a gateway, docs/capture.md §4.3).
func isLocalSource(m controller.Metadata) bool {
	if strings.EqualFold(m.Type, "tun") {
		return true
	}
	a, err := netip.ParseAddr(m.SourceIP)
	return err == nil && a.Unmap().IsLoopback()
}
