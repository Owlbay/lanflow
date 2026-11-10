package app

import (
	"context"
	"strings"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/model"
)

const (
	trafficHistory  = 60
	logRing         = 1000
	closedRing      = 500
	connInterval    = time.Second
	refreshInterval = 10 * time.Second
	// In the background (the window hidden or minimized, SetBackground)
	// nothing shows the connections, so the core is asked for them less
	// often: the statistics count deltas and lose nothing, the tray's
	// speed comes from the traffic stream; the proxies and rules are
	// re-read every refreshTicksHidden ticks of the refresh loop.
	connIntervalHidden = 3 * time.Second
	refreshTicksHidden = 6
)

// startStreams opens the controller WebSocket streams and the periodic
// refreshers for the running core. Each runs until stopStreams.
func (s *Service) startStreams() {
	ctx, cancel := context.WithCancel(s.rootCtx)
	logCtx, logCancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.streamCancel != nil {
		s.streamCancel()
	}
	s.streamCancel = cancel
	s.logsCancel = logCancel
	level := s.state.settings.LogLevel
	s.mu.Unlock()

	run := func(fn func(context.Context)) {
		s.streamWG.Add(1)
		go func() {
			defer s.streamWG.Done()
			fn(ctx)
		}()
	}
	run(func(ctx context.Context) { _ = s.d.ctrl.StreamTraffic(ctx, s.onTraffic) })
	run(func(ctx context.Context) { _ = s.d.ctrl.StreamMemory(ctx, s.onMemory) })
	run(s.refreshLoop)
	s.streamWG.Add(1)
	go func() {
		defer s.streamWG.Done()
		s.runLogStream(logCtx, level)
	}()
	s.startConnStream()
}

// connInterval is how often the core reports the connections now.
func (s *Service) connInterval() time.Duration {
	if s.background.Load() {
		return connIntervalHidden
	}
	return connInterval
}

// startConnStream opens the connections stream at the current interval,
// closing the previous one. Call with the streams started.
func (s *Service) startConnStream() {
	s.mu.Lock()
	if s.streamCancel == nil {
		s.mu.Unlock()
		return
	}
	if s.connsCancel != nil {
		s.connsCancel()
	}
	ctx, cancel := context.WithCancel(s.rootCtx)
	s.connsCancel = cancel
	s.mu.Unlock()
	interval := s.connInterval()
	s.streamWG.Add(1)
	go func() {
		defer s.streamWG.Done()
		_ = s.d.ctrl.StreamConnections(ctx, interval, s.onConnections)
	}()
}

// SetBackground tells the service whether the window is out of sight
// (hidden to the tray, minimized): in the background the connections
// stream slows to connIntervalHidden and the proxies and rules refresh
// less often. The UI calls it from the window's show, hide, minimize
// and restore events; the fake backend has no such method.
func (s *Service) SetBackground(background bool) {
	if s.background.Swap(background) == background {
		return
	}
	s.startConnStream()
}

// restartLogStream reopens the log stream at a new level.
func (s *Service) restartLogStream(level string) {
	s.mu.Lock()
	if s.streamCancel == nil || s.logsCancel == nil {
		s.mu.Unlock()
		return
	}
	s.logsCancel()
	parent := s.rootCtx
	logCtx, logCancel := context.WithCancel(parent)
	s.logsCancel = logCancel
	s.mu.Unlock()
	s.streamWG.Add(1)
	go func() {
		defer s.streamWG.Done()
		s.runLogStream(logCtx, level)
	}()
}

func (s *Service) runLogStream(ctx context.Context, level string) {
	if level == "silent" {
		<-ctx.Done()
		return
	}
	if level == "" {
		level = "info"
	}
	_ = s.d.ctrl.StreamLogs(ctx, level, s.onLog)
}

// stopStreams cancels the streams and waits for them to return.
func (s *Service) stopStreams() {
	s.mu.Lock()
	cancel := s.streamCancel
	logCancel := s.logsCancel
	connCancel := s.connsCancel
	s.streamCancel, s.logsCancel, s.connsCancel = nil, nil, nil
	s.mu.Unlock()
	if logCancel != nil {
		logCancel()
	}
	if connCancel != nil {
		connCancel()
	}
	if cancel != nil {
		cancel()
	}
	s.streamWG.Wait()
}

// refreshLoop refreshes proxies and rules every 10 s while running, every
// minute in the background.
func (s *Service) refreshLoop(ctx context.Context) {
	t := time.NewTicker(refreshInterval)
	defer t.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ticks++
			if s.background.Load() && ticks%refreshTicksHidden != 0 {
				continue
			}
			s.refreshProxies()
			s.refreshRules()
		}
	}
}

// ---- frame handlers ----

func (s *Service) onTraffic(t controller.Traffic) {
	s.mu.Lock()
	s.state.up = appendRing(s.state.up, t.Up, trafficHistory)
	s.state.down = appendRing(s.state.down, t.Down, trafficHistory)
	s.state.st.Up, s.state.st.Down = t.Up, t.Down
	s.mu.Unlock()
	s.em.emit(TopicTraffic, TopicStatus)
}

func appendRing(h []int64, v int64, n int) []int64 {
	h = append(h, v)
	if len(h) > n {
		h = h[len(h)-n:]
	}
	return h
}

func (s *Service) onMemory(m controller.Memory) {
	s.mu.Lock()
	changed := s.state.st.Memory != m.InUse
	s.state.st.Memory = m.InUse
	s.mu.Unlock()
	if changed {
		s.em.emit(TopicStatus)
	}
}

// logLevel returns the lower-case level of a log line without allocating
// for the levels the core sends.
func logLevel(t string) string {
	switch t {
	case "info", "warning", "error", "debug", "silent":
		return t
	case "INFO", "Info":
		return "info"
	case "WARNING", "Warning":
		return "warning"
	case "ERROR", "Error":
		return "error"
	case "DEBUG", "Debug":
		return "debug"
	}
	return strings.ToLower(t)
}

func (s *Service) onLog(l controller.Log) {
	e := model.LogEntry{Time: s.d.now(), Level: logLevel(l.Type), Payload: l.Payload}
	s.mu.Lock()
	s.state.logs = append(s.state.logs, e)
	if len(s.state.logs) > logRing {
		s.state.logs = s.state.logs[len(s.state.logs)-logRing:]
	}
	s.mu.Unlock()
	s.em.emit(TopicLogs)
}

func (s *Service) onConnections(c controller.Connections) {
	now := s.d.now()
	s.mu.Lock()
	active, closed := s.state.tracker.update(c, now)
	s.state.conns = active
	if len(closed) > 0 {
		s.state.closed = append(closed, s.state.closed...)
		if len(s.state.closed) > closedRing {
			s.state.closed = s.state.closed[:closedRing]
		}
	}
	s.state.st.Connections = len(active)
	s.state.st.TotalUp, s.state.st.TotalDown = c.UploadTotal, c.DownloadTotal
	if c.Memory > 0 {
		s.state.st.Memory = c.Memory
	}
	s.mu.Unlock()
	s.em.emit(TopicConns, TopicStatus)
	if s.stats.record(active, now) {
		s.em.emit(TopicStats)
	}
	s.rebuildRoutes()
}

// ClearLogs empties the log ring.
func (s *Service) ClearLogs() {
	s.mu.Lock()
	s.state.logs = nil
	s.mu.Unlock()
	s.em.emit(TopicLogs)
}

// ---- connection tracking ----

type connSample struct {
	up, down int64
	at       time.Time
	conn     model.Connection
}

// connTracker turns successive connection snapshots into speeds and a list
// of connections that disappeared since the previous snapshot.
type connTracker struct {
	prev map[string]connSample
}

// update maps the snapshot to model connections (chains outermost first,
// speeds from the byte deltas) and returns the connections that were in
// the previous snapshot but not in this one, newest closed first.
func (t *connTracker) update(c controller.Connections, now time.Time) (active, closed []model.Connection) {
	next := make(map[string]connSample, len(c.Connections))
	active = make([]model.Connection, 0, len(c.Connections))
	for _, rc := range c.Connections {
		m := toConnection(rc)
		if p, ok := t.prev[rc.ID]; ok {
			dt := now.Sub(p.at).Seconds()
			if dt > 0 {
				m.UpSpeed = int64(float64(rc.Upload-p.up) / dt)
				m.DownSpeed = int64(float64(rc.Download-p.down) / dt)
				if m.UpSpeed < 0 {
					m.UpSpeed = 0
				}
				if m.DownSpeed < 0 {
					m.DownSpeed = 0
				}
			}
		}
		next[rc.ID] = connSample{up: rc.Upload, down: rc.Download, at: now, conn: m}
		active = append(active, m)
	}
	for id, p := range t.prev {
		if _, ok := next[id]; ok {
			continue
		}
		cc := p.conn
		cc.UpSpeed, cc.DownSpeed = 0, 0
		cc.ClosedAt = now
		closed = append(closed, cc)
	}
	t.prev = next
	return active, closed
}

// toConnection maps a controller connection to the model (chains reversed).
func toConnection(rc controller.Connection) model.Connection {
	md := rc.Metadata
	host := md.Host
	if host == "" {
		host = md.SniffHost
	}
	if host == "" {
		host = md.DestinationIP
	}
	if md.DestinationPort != "" {
		host = joinHostPort(host, md.DestinationPort)
	}
	chains := make([]string, len(rc.Chains))
	for i, c := range rc.Chains {
		chains[len(rc.Chains)-1-i] = c
	}
	return model.Connection{
		ID:                rc.ID,
		Host:              host,
		DstIP:             md.DestinationIP,
		Network:           strings.ToLower(md.Network),
		Type:              md.Type,
		Process:           md.Process,
		ProcessPath:       md.ProcessPath,
		Rule:              rc.Rule,
		RulePayload:       rc.RulePayload,
		Chains:            chains,
		SourceIP:          md.SourceIP,
		SourcePort:        md.SourcePort,
		RemoteDestination: md.RemoteDestination,
		Upload:            rc.Upload,
		Download:          rc.Download,
		Start:             rc.Start,
	}
}

func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// CloseConnection closes one connection in the core.
func (s *Service) CloseConnection(id string) error {
	if !s.running() {
		return errCoreNotRunning
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctrlTimeout)
	defer cancel()
	if err := s.d.ctrl.CloseConnection(ctx, id); err != nil {
		return err
	}
	return nil
}

// CloseAllConnections closes every connection in the core.
func (s *Service) CloseAllConnections() error {
	if !s.running() {
		return errCoreNotRunning
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctrlTimeout)
	defer cancel()
	return s.d.ctrl.CloseAllConnections(ctx)
}
