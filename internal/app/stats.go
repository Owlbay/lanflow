package app

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// 流量统计 (traffic statistics): the connection stream's byte counters are
// turned into per-day aggregates by device, domain, host, process, rule,
// proxy, destination country and inbound type (stats_store.go persists
// them under <DataDir>/stats, one JSON file per day).
//
// Accounting is by deltas: every snapshot of the active connections is
// compared with the previous one and only the bytes that arrived since
// are added, so open connections count as they grow and no byte is
// counted twice. A connection that disappears from the snapshot has had
// its bytes counted up to the last sample it appeared in.

const (
	statsBucketCap     = 500 // buckets kept per dimension per day; the rest folds into StatsOtherKey
	statsRetentionDays = 90
	statsSeriesDays    = 30
	statsFlushInterval = time.Minute
	statsIPCacheMax    = 4096
	statsDayLayout     = "2006-01-02"
)

// statsSample is the last byte totals seen for one connection ID.
type statsSample struct{ up, down int64 }

// statsDay is one day of aggregates.
type statsDay struct {
	date  string // statsDayLayout, local time
	hours [24]model.StatsPoint
	dims  map[model.StatsDimension]map[string]*model.StatsBucket
	dirty bool
	file  string // the day's own file on disk, "" when it lives in its month's file or nowhere yet
}

func newStatsDay(date string) *statsDay {
	return &statsDay{date: date, dims: map[model.StatsDimension]map[string]*model.StatsBucket{}}
}

// statsStore accumulates and serves the statistics. Its mutex is its own:
// it is never held with Service.mu.
type statsStore struct {
	mu      sync.Mutex
	dir     string
	now     func() time.Time
	country func(ip string) string // ISO code of a destination IP, "" when unknown

	days    map[string]*statsDay
	prev    map[string]statsSample // by connection ID
	ips     map[string]string      // destination IP → country code
	gen     uint64                 // bumps on every change; the snapshot cache keys on it
	cache   map[string]cachedStats
	rewrite map[string]bool // past months whose file must be written again (days expired)
}

type cachedStats struct {
	gen  uint64
	hour string // the local day-hour the snapshot was built in
	st   model.TrafficStats
}

// newStatsStore returns an empty store over dir; call load to read what a
// previous run wrote.
func newStatsStore(dir string, now func() time.Time, country func(ip string) string) *statsStore {
	if now == nil {
		now = time.Now
	}
	if country == nil {
		country = func(string) string { return "" }
	}
	return &statsStore{dir: dir, now: now, country: country, days: map[string]*statsDay{}, prev: map[string]statsSample{},
		ips: map[string]string{}, cache: map[string]cachedStats{}, rewrite: map[string]bool{}}
}

// record accounts one snapshot of the active connections taken at now and
// reports whether anything changed.
func (s *statsStore) record(conns []model.Connection, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]statsSample, len(conns))
	changed := false
	var day *statsDay
	hour := now.Hour()
	for _, c := range conns {
		p, seen := s.prev[c.ID]
		next[c.ID] = statsSample{up: c.Upload, down: c.Download}
		du, dd := c.Upload-p.up, c.Download-p.down
		if du < 0 {
			du = 0
		}
		if dd < 0 {
			dd = 0
		}
		var n int64
		if !seen {
			n = 1
		}
		if n == 0 && du == 0 && dd == 0 {
			continue
		}
		if day == nil {
			day = s.dayLocked(now)
		}
		changed = true
		pt := &day.hours[hour]
		pt.Upload += du
		pt.Download += dd
		pt.Requests += n
		for _, k := range s.keysOf(c) {
			s.addLocked(day, k.dim, k.key, k.detail, n, du, dd, now)
		}
	}
	s.prev = next
	if changed {
		day.dirty = true
		s.gen++
	}
	return changed
}

// statsKey is one bucket a connection belongs to.
type statsKey struct {
	dim         model.StatsDimension
	key, detail string
}

// keysOf maps a connection to its bucket in every dimension.
func (s *statsStore) keysOf(c model.Connection) []statsKey {
	host := statsHost(c.Host)
	domain := registrableDomain(host)
	group, node := "", ""
	if len(c.Chains) > 0 {
		group, node = c.Chains[0], c.Chains[len(c.Chains)-1]
	}
	proxyKey := group
	if node != group {
		proxyKey = group + "/" + node
	}
	return []statsKey{
		{model.StatsDevice, c.SourceIP, ""},
		{model.StatsDomain, domain, ""},
		{model.StatsHost, host, domain},
		{model.StatsProcess, c.Process, c.ProcessPath},
		{model.StatsRule, c.Rule + "," + c.RulePayload, c.RulePayload},
		{model.StatsProxy, proxyKey, node},
		{model.StatsCountry, s.countryLocked(c.DstIP), ""},
		{model.StatsInbound, c.Type, ""},
	}
}

// countryLocked resolves a destination IP's country through a bounded cache.
func (s *statsStore) countryLocked(ip string) string {
	if ip == "" {
		return ""
	}
	if code, ok := s.ips[ip]; ok {
		return code
	}
	code := ""
	if p := net.ParseIP(ip); p != nil {
		switch {
		case p.IsLoopback(), p.IsPrivate(), p.IsLinkLocalUnicast(), p.IsUnspecified(), isFakeIP(p):
			code = ""
		default:
			code = strings.ToUpper(s.country(ip))
		}
	}
	if len(s.ips) >= statsIPCacheMax {
		s.ips = map[string]string{}
	}
	s.ips[ip] = code
	return code
}

// isFakeIP reports the core's fake-ip range (198.18.0.0/15), whose
// addresses say nothing about the destination.
func isFakeIP(p net.IP) bool {
	v4 := p.To4()
	return v4 != nil && v4[0] == 198 && (v4[1] == 18 || v4[1] == 19)
}

// dayLocked returns (creating it) the day holding now.
func (s *statsStore) dayLocked(now time.Time) *statsDay {
	k := now.Format(statsDayLayout)
	d, ok := s.days[k]
	if !ok {
		d = newStatsDay(k)
		s.days[k] = d
	}
	return d
}

// addLocked adds to one bucket, folding the dimension down when it holds
// twice the cap (flush folds it to the cap itself).
func (s *statsStore) addLocked(day *statsDay, dim model.StatsDimension, key, detail string, n, up, down int64, now time.Time) {
	m := day.dims[dim]
	if m == nil {
		m = map[string]*model.StatsBucket{}
		day.dims[dim] = m
	}
	b, ok := m[key]
	if !ok {
		if len(m) >= 2*statsBucketCap {
			foldBuckets(m, statsBucketCap)
		}
		b = &model.StatsBucket{Key: key}
		m[key] = b
	}
	if detail != "" {
		b.Detail = detail
	}
	b.Requests += n
	b.Upload += up
	b.Download += down
	if now.After(b.LastSeen) {
		b.LastSeen = now
	}
}

// foldBuckets keeps the keep biggest buckets of m (by total bytes, then
// requests) and adds the rest to the StatsOtherKey bucket.
func foldBuckets(m map[string]*model.StatsBucket, keep int) {
	other := m[model.StatsOtherKey]
	rest := make([]*model.StatsBucket, 0, len(m))
	for k, b := range m {
		if k != model.StatsOtherKey {
			rest = append(rest, b)
		}
	}
	if len(rest) <= keep {
		return
	}
	sortBuckets(rest)
	if other == nil {
		other = &model.StatsBucket{Key: model.StatsOtherKey}
		m[model.StatsOtherKey] = other
	}
	for _, b := range rest[keep:] {
		other.Requests += b.Requests
		other.Upload += b.Upload
		other.Download += b.Download
		if b.LastSeen.After(other.LastSeen) {
			other.LastSeen = b.LastSeen
		}
		delete(m, b.Key)
	}
}

// sortBuckets orders biggest total first, then most requests, then key.
func sortBuckets(bs []*model.StatsBucket) {
	sort.Slice(bs, func(i, j int) bool {
		a, b := bs[i], bs[j]
		if a.Total() != b.Total() {
			return a.Total() > b.Total()
		}
		if a.Requests != b.Requests {
			return a.Requests > b.Requests
		}
		return a.Key < b.Key
	})
}

// ---- ranges and snapshots ----

// startOfDay is midnight before t, local time.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// rangeStart is the first instant of rangeKey as of now; zero for StatsAll.
func rangeStart(rangeKey string, now time.Time) time.Time {
	day := startOfDay(now)
	switch rangeKey {
	case model.StatsWeek:
		wd := int(day.Weekday()+6) % 7 // Monday = 0
		return day.AddDate(0, 0, -wd)
	case model.StatsMonth:
		return time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, day.Location())
	case model.StatsAll:
		return time.Time{}
	default:
		return day
	}
}

// snapshot builds (or serves from the cache) the statistics of rangeKey.
func (s *statsStore) snapshot(rangeKey string) model.TrafficStats {
	switch rangeKey {
	case model.StatsWeek, model.StatsMonth, model.StatsAll:
	default:
		rangeKey = model.StatsToday
	}
	now := s.now()
	hour := now.Format("2006-01-02T15")
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[rangeKey]; ok && c.gen == s.gen && c.hour == hour {
		return copyStats(c.st)
	}
	st := s.buildLocked(rangeKey, now)
	s.cache[rangeKey] = cachedStats{gen: s.gen, hour: hour, st: st}
	return copyStats(st)
}

// buildLocked computes the statistics of rangeKey as of now.
func (s *statsStore) buildLocked(rangeKey string, now time.Time) model.TrafficStats {
	since := rangeStart(rangeKey, now)
	st := model.TrafficStats{Range: rangeKey, Since: since, Buckets: map[model.StatsDimension][]model.StatsBucket{}, Days: len(s.days)}
	sinceKey := ""
	if !since.IsZero() {
		sinceKey = since.Format(statsDayLayout)
	}
	merged := map[model.StatsDimension]map[string]*model.StatsBucket{}
	first := ""
	for k, d := range s.days {
		if k < sinceKey {
			continue
		}
		if first == "" || k < first {
			first = k
		}
		for _, h := range d.hours {
			st.Totals.Upload += h.Upload
			st.Totals.Download += h.Download
			st.Totals.Requests += h.Requests
		}
		for dim, m := range d.dims {
			out := merged[dim]
			if out == nil {
				out = map[string]*model.StatsBucket{}
				merged[dim] = out
			}
			for key, b := range m {
				o, ok := out[key]
				if !ok {
					cp := *b
					out[key] = &cp
					continue
				}
				o.Requests += b.Requests
				o.Upload += b.Upload
				o.Download += b.Download
				if b.LastSeen.After(o.LastSeen) {
					o.LastSeen = b.LastSeen
					if b.Detail != "" {
						o.Detail = b.Detail
					}
				}
			}
		}
	}
	if since.IsZero() && first != "" {
		if t, err := time.ParseInLocation(statsDayLayout, first, now.Location()); err == nil {
			st.Since = t
		}
	}
	for dim, m := range merged {
		bs := make([]*model.StatsBucket, 0, len(m))
		for _, b := range m {
			bs = append(bs, b)
		}
		sortBuckets(bs)
		out := make([]model.StatsBucket, len(bs))
		for i, b := range bs {
			out[i] = *b
			if dim == model.StatsDevice && b.Key != model.StatsOtherKey {
				st.Totals.Devices++
			}
		}
		st.Buckets[dim] = out
	}
	// Series: the day's hours, or the last 30 days.
	today := startOfDay(now)
	if rangeKey == model.StatsToday {
		st.Hourly = true
		st.Series = make([]model.StatsPoint, 24)
		d := s.days[today.Format(statsDayLayout)]
		for h := range st.Series {
			st.Series[h].Time = today.Add(time.Duration(h) * time.Hour)
			if d != nil {
				st.Series[h].Upload, st.Series[h].Download, st.Series[h].Requests = d.hours[h].Upload, d.hours[h].Download, d.hours[h].Requests
			}
		}
	} else {
		st.Series = make([]model.StatsPoint, statsSeriesDays)
		for i := range st.Series {
			day := today.AddDate(0, 0, i+1-statsSeriesDays)
			st.Series[i].Time = day
			if d := s.days[day.Format(statsDayLayout)]; d != nil {
				for _, h := range d.hours {
					st.Series[i].Upload += h.Upload
					st.Series[i].Download += h.Download
					st.Series[i].Requests += h.Requests
				}
			}
		}
	}
	return st
}

// copyStats returns st with its own slices, so callers may sort them.
func copyStats(st model.TrafficStats) model.TrafficStats {
	out := st
	out.Series = append([]model.StatsPoint(nil), st.Series...)
	out.Buckets = make(map[model.StatsDimension][]model.StatsBucket, len(st.Buckets))
	for dim, bs := range st.Buckets {
		out.Buckets[dim] = append([]model.StatsBucket(nil), bs...)
	}
	return out
}

// loop flushes dirty days every minute and once more when ctx ends
// (Shutdown cancels the root context before waiting for the goroutines).
func (s *statsStore) loop(ctx context.Context) {
	t := time.NewTicker(statsFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.flush()
			return
		case <-t.C:
			_ = s.flush()
		}
	}
}

// ---- Service ----

// TrafficStats is the 流量统计 of rangeKey (model.StatsToday, StatsWeek,
// StatsMonth or StatsAll); a cheap read served from a cache.
func (s *Service) TrafficStats(rangeKey string) model.TrafficStats {
	return s.stats.snapshot(rangeKey)
}

// ResetTrafficStats forgets every statistic, on disk too. Connections
// still open count again only from their bytes after the reset.
func (s *Service) ResetTrafficStats() error {
	if err := s.stats.reset(); err != nil {
		return fmt.Errorf(i18n.T("重置统计失败：%v"), err)
	}
	s.alog.Infof("traffic statistics reset")
	s.em.emit(TopicStats)
	return nil
}

// statsCountry resolves a destination IP's country offline for the 地区
// dimension; "" without the locator or a match.
func (s *Service) statsCountry(ip string) string {
	if s.d.loc == nil {
		return ""
	}
	if ep := s.d.loc.Lookup(ip); ep.Place != nil {
		return ep.Place.Country
	}
	return ""
}
