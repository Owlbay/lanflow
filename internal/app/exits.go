package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

const (
	probeDebounce = 3 * time.Second
	probeTimeout  = 20 * time.Second
)

// routeStat summarises the connections of one first-level group.
type routeStat struct {
	speed  int64
	count  int
	newest model.Connection
	hasNew bool
}

// buildRoutes groups the active connections by first-level group (the
// outermost chain entry), resolves each group to its final node and attaches
// the known exit info. The main group is always first.
func buildRoutes(conns []model.Connection, snap model.ProxySnapshot, mode model.Mode, exits map[string]model.ExitInfo) []model.ExitRoute {
	if mode == model.ModeDirect {
		return nil
	}
	main := snap.MainGroup
	if mode == model.ModeGlobal {
		main = "GLOBAL"
	}
	if main == "" {
		return nil
	}
	isGroup := map[string]bool{}
	for _, g := range snap.Groups {
		isGroup[g.Name] = true
	}
	order := []string{main}
	stats := map[string]*routeStat{main: {}}
	for _, c := range conns {
		if len(c.Chains) == 0 {
			continue
		}
		g := c.Chains[0]
		if g == "DIRECT" || g == "REJECT" || g == probeGroup {
			continue
		}
		if mode == model.ModeGlobal {
			g = "GLOBAL"
		}
		st, ok := stats[g]
		if !ok {
			st = &routeStat{}
			stats[g] = st
			order = append(order, g)
		}
		st.speed += c.UpSpeed + c.DownSpeed
		st.count++
		if !st.hasNew || c.Start.After(st.newest.Start) {
			st.newest, st.hasNew = c, true
		}
	}
	routes := make([]model.ExitRoute, 0, len(order))
	for _, g := range order {
		st := stats[g]
		node := resolveNode(snap, g)
		if st.hasNew && len(st.newest.Chains) > 0 {
			// the connection knows the real final node (last chain entry)
			node = st.newest.Chains[len(st.newest.Chains)-1]
		}
		info, ok := exits[node]
		if !ok {
			info = model.ExitInfo{Node: node, Source: model.LocateUnknown}
		}
		routes = append(routes, model.ExitRoute{Group: g, Node: node, Exit: info, Speed: st.speed, Connections: st.count, Main: g == main})
	}
	return routes
}

// chainHops counts the nodes (non-group entries) of a chain.
func chainHops(chain []string, snap model.ProxySnapshot) int {
	isGroup := map[string]bool{}
	for _, g := range snap.Groups {
		isGroup[g.Name] = true
	}
	n := 0
	for _, c := range chain {
		if !isGroup[c] {
			n++
		}
	}
	return n
}

// rebuildRoutes recomputes the exit routes from the current connections and
// proxies, filling in exit info (name guess / cached probe + path analysis)
// for nodes not seen before. Runs off the UI thread.
func (s *Service) rebuildRoutes() {
	s.mu.Lock()
	if s.state.st.Core != model.CoreRunning {
		changed := s.state.routes != nil
		s.state.routes = nil
		s.mu.Unlock()
		if changed {
			s.em.emit(TopicExits)
		}
		return
	}
	conns := s.state.conns
	snap := s.state.proxies
	mode := s.state.st.Mode
	exits := make(map[string]model.ExitInfo, len(s.state.exits)) // copy: read below without the lock
	for k, v := range s.state.exits {
		exits[k] = v
	}
	analyzed := make(map[string]string, len(s.state.exitEntry))
	for k, v := range s.state.exitEntry {
		analyzed[k] = v
	}
	s.mu.Unlock()

	routes := buildRoutes(conns, snap, mode, exits)
	// Fill in missing or stale exit info outside the lock.
	type fill struct {
		node  string
		entry string
		hops  int
	}
	var fills []fill
	byGroup := map[string]model.Connection{}
	for _, c := range conns {
		if len(c.Chains) == 0 {
			continue
		}
		if prev, ok := byGroup[c.Chains[0]]; !ok || c.Start.After(prev.Start) {
			byGroup[c.Chains[0]] = c
		}
	}
	for _, r := range routes {
		info, known := exits[r.Node]
		c, hasConn := byGroup[r.Group]
		entry, hops := "", 1
		if hasConn {
			entry, hops = c.RemoteDestination, chainHops(c.Chains, snap)
		}
		_ = info
		if !known || (entry != "" && analyzed[r.Node] != entry) {
			fills = append(fills, fill{r.Node, entry, hops})
		}
	}
	if len(fills) > 0 {
		updated := map[string]model.ExitInfo{}
		for _, f := range fills {
			info, known := exits[f.node]
			if !known || info.Source != model.LocateProbe {
				info = s.d.loc.Guess(f.node)
				info.Node = f.node
			}
			if f.entry != "" {
				s.d.loc.Analyze(&info, f.entry, f.hops)
			}
			updated[f.node] = info
		}
		s.mu.Lock()
		for n, info := range updated {
			if cur, ok := s.state.exits[n]; ok && cur.Source == model.LocateProbe && info.Source != model.LocateProbe {
				continue // a probe finished meanwhile; keep it
			}
			s.state.exits[n] = info
		}
		for _, f := range fills {
			s.state.exitEntry[f.node] = f.entry
		}
		for n, info := range s.state.exits {
			exits[n] = info
		}
		s.mu.Unlock()
		routes = buildRoutes(conns, snap, mode, exits)
	}
	s.mu.Lock()
	s.state.routes = routes
	s.mu.Unlock()
	s.em.emit(TopicExits)
}

// storeExit records a probe result and refreshes the routes.
func (s *Service) storeExit(info model.ExitInfo) {
	s.mu.Lock()
	s.state.exits[info.Node] = info
	s.mu.Unlock()
	s.rebuildRoutes()
}

// probeNode measures one node through the probe listener and analyses its path.
func (s *Service) probeNode(ctx context.Context, node string) error {
	s.mu.Lock()
	port := s.state.probePort
	running := s.state.st.Core == model.CoreRunning
	var entry string
	var hops int
	for _, c := range s.state.conns {
		if len(c.Chains) > 0 && c.Chains[len(c.Chains)-1] == node && c.RemoteDestination != "" {
			entry, hops = c.RemoteDestination, chainHops(c.Chains, s.state.proxies)
		}
	}
	s.mu.Unlock()
	if !running {
		return errCoreNotRunning
	}
	if port == 0 {
		return errors.New(i18n.T("探测端口不可用"))
	}
	sel := func(ctx context.Context, n string) error { return s.d.ctrl.SelectProxy(ctx, probeGroup, n) }
	info, err := s.d.loc.Probe(ctx, sel, port, node)
	if err != nil {
		return err
	}
	info.Node = node
	if entry != "" {
		s.d.loc.Analyze(&info, entry, hops)
	}
	s.mu.Lock()
	s.state.exitEntry[node] = entry
	s.mu.Unlock()
	s.storeExit(info)
	return nil
}

// ProbeExits measures every current route's exit via Cloudflare trace.
func (s *Service) ProbeExits() error {
	s.mu.Lock()
	allowed := s.state.settings.ProbeExits
	running := s.state.st.Core == model.CoreRunning
	mode := s.state.st.Mode
	routes := append([]model.ExitRoute(nil), s.state.routes...)
	s.mu.Unlock()
	if !running {
		return errCoreNotRunning
	}
	if !allowed {
		return errors.New(i18n.T("「实测节点出口位置」已在设置中关闭。打开后才能实测。"))
	}
	if mode == model.ModeDirect {
		return errors.New(i18n.T("直连模式下流量不经过节点，没有出口可以实测。"))
	}
	seen := map[string]bool{}
	var firstErr error
	for _, r := range routes {
		if r.Node == "" || r.Node == "DIRECT" || r.Node == "REJECT" || seen[r.Node] {
			continue
		}
		seen[r.Node] = true
		ctx, cancel := context.WithTimeout(s.rootCtx, probeTimeout)
		err := s.probeNode(ctx, r.Node)
		cancel()
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf(i18n.T("实测「%s」失败：%v"), r.Node, err)
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return nil
}

// scheduleMainProbe probes the main group's exit 3 s after the last call
// (debounced), when probes are allowed.
func (s *Service) scheduleMainProbe() {
	s.mu.Lock()
	allowed := s.state.settings.ProbeExits
	s.mu.Unlock()
	if !allowed || s.rootCtx.Err() != nil {
		return
	}
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.probeTimer != nil {
		s.probeTimer.Stop()
	}
	s.probeTimer = time.AfterFunc(probeDebounce, func() {
		if s.rootCtx.Err() != nil {
			return
		}
		s.mu.Lock()
		snap := s.state.proxies
		mode := s.state.st.Mode
		running := s.state.st.Core == model.CoreRunning
		s.mu.Unlock()
		if !running || mode == model.ModeDirect {
			return
		}
		main := snap.MainGroup
		if mode == model.ModeGlobal {
			main = "GLOBAL"
		}
		node := resolveNode(snap, main)
		if node == "" || node == "DIRECT" || node == "REJECT" || node == main {
			return
		}
		ctx, cancel := context.WithTimeout(s.rootCtx, probeTimeout)
		defer cancel()
		_ = s.probeNode(ctx, node)
	})
}

// youBackoff spaces the retries of the direct public endpoint lookup
// (Cloudflare trace through DIRECT); the last value repeats. Tests shorten it.
var youBackoff = []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 2 * time.Minute, 5 * time.Minute}

// fetchYou looks up the user's direct public endpoint once per core start,
// retrying with backoff while the network is not there yet.
func (s *Service) fetchYou() {
	s.mu.Lock()
	done := s.state.youDone
	s.state.youDone = true
	s.mu.Unlock()
	if done {
		return
	}
	s.fetchYouLoop()
}

// refreshYou looks the direct public endpoint up again now (network change,
// wake, 「重新测试」), superseding a lookup still retrying.
func (s *Service) refreshYou() {
	s.mu.Lock()
	s.state.youDone = true
	s.mu.Unlock()
	s.goBackground(s.fetchYouLoop)
}

// fetchYouLoop tries the lookup until it succeeds, the service shuts down
// or a newer loop supersedes it. NetInfo().PublicIPStatus follows along so
// the 网络 page can show "fetching" / "failed" instead of waiting forever.
func (s *Service) fetchYouLoop() {
	if s.rootCtx.Err() != nil {
		return
	}
	s.mu.Lock()
	s.state.youGen++
	gen := s.state.youGen
	s.state.youBusy = true
	s.state.net.PublicIPStatus = "fetching"
	s.mu.Unlock()
	s.em.emit(TopicNet)
	for attempt := 0; s.rootCtx.Err() == nil; attempt++ {
		ctx, cancel := context.WithTimeout(s.rootCtx, probeTimeout)
		info, err := s.d.loc.Direct(ctx)
		cancel()
		s.mu.Lock()
		if gen != s.state.youGen {
			s.mu.Unlock()
			return // a newer lookup took over
		}
		if err == nil {
			s.state.you = &info
			s.state.youBusy = false
			s.state.net.PublicIPStatus, s.state.net.PublicIPError = "ok", ""
			s.mu.Unlock()
			s.em.emit(TopicExits, TopicNet)
			return
		}
		s.state.net.PublicIPStatus, s.state.net.PublicIPError = "failed", err.Error()
		s.mu.Unlock()
		s.em.emit(TopicNet)
		wait := youBackoff[min(attempt, len(youBackoff)-1)]
		select {
		case <-s.rootCtx.Done():
			return
		case <-time.After(wait):
		}
	}
}
