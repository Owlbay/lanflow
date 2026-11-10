package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/model"
)

// statsConn builds a connection for the stats tests.
func statsConn(id, host, src string, up, down int64) model.Connection {
	return model.Connection{ID: id, Host: host + ":443", DstIP: "1.1.1.1", Network: "tcp", Type: "HTTP", Process: "Safari",
		ProcessPath: "/Applications/Safari.app", Rule: "DomainSuffix", RulePayload: "example.com", Chains: []string{"节点选择", "东京 03"},
		SourceIP: src, Upload: up, Download: down}
}

func bucketOf(st model.TrafficStats, dim model.StatsDimension, key string) (model.StatsBucket, bool) {
	for _, b := range st.Buckets[dim] {
		if b.Key == key {
			return b, true
		}
	}
	return model.StatsBucket{}, false
}

// TestStatsDeltaAccounting: bytes count once across ticks, open
// connections count as they grow, a connection that disappears keeps
// what it had, and the dimensions key as documented.
func TestStatsDeltaAccounting(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 10, 30, 0, 0, time.Local)
	now := t0
	s := newStatsStore(t.TempDir(), func() time.Time { return now }, func(ip string) string { return "US" })

	if !s.record([]model.Connection{statsConn("a", "www.google.com", "127.0.0.1", 100, 1000), statsConn("b", "api.github.com", "192.168.1.8", 0, 500)}, now) {
		t.Fatal("first tick should change something")
	}
	st := s.snapshot(model.StatsToday)
	if st.Totals.Upload != 100 || st.Totals.Download != 1500 || st.Totals.Requests != 2 || st.Totals.Devices != 2 {
		t.Fatalf("after tick 1: %+v", st.Totals)
	}

	// Tick 2: a grew, b vanished (its bytes stay), nothing counted twice.
	now = t0.Add(time.Second)
	s.record([]model.Connection{statsConn("a", "www.google.com", "127.0.0.1", 150, 1200)}, now)
	st = s.snapshot(model.StatsToday)
	if st.Totals.Upload != 150 || st.Totals.Download != 1700 || st.Totals.Requests != 2 {
		t.Fatalf("after tick 2: %+v", st.Totals)
	}
	// Tick 3: no growth → no change.
	now = t0.Add(2 * time.Second)
	if s.record([]model.Connection{statsConn("a", "www.google.com", "127.0.0.1", 150, 1200)}, now) {
		t.Fatal("an unchanged snapshot must not count as a change")
	}
	// A counter going backwards is clamped, not subtracted.
	s.record([]model.Connection{statsConn("a", "www.google.com", "127.0.0.1", 140, 1200)}, now)
	if st = s.snapshot(model.StatsToday); st.Totals.Upload != 150 {
		t.Fatalf("negative delta leaked: %+v", st.Totals)
	}
	// Tick 4: a new connection is one more request; same ID later is not.
	now = t0.Add(3 * time.Second)
	s.record([]model.Connection{statsConn("a", "www.google.com", "127.0.0.1", 140, 1200), statsConn("c", "rr3---sn-i3b7.googlevideo.com", "127.0.0.1", 10, 20)}, now)
	st = s.snapshot(model.StatsToday)
	if st.Totals.Requests != 3 || st.Totals.Devices != 2 {
		t.Fatalf("after tick 4: %+v", st.Totals)
	}

	// Dimensions.
	if b, ok := bucketOf(st, model.StatsDevice, "127.0.0.1"); !ok || b.Requests != 2 || b.Upload != 160 || b.Download != 1220 || !b.LastSeen.Equal(now) {
		t.Errorf("device bucket: %+v %v", b, ok)
	}
	if b, ok := bucketOf(st, model.StatsDomain, "googlevideo.com"); !ok || b.Requests != 1 || b.Download != 20 {
		t.Errorf("domain bucket (eTLD+1): %+v %v", b, ok)
	}
	if b, ok := bucketOf(st, model.StatsHost, "rr3---sn-i3b7.googlevideo.com"); !ok || b.Detail != "googlevideo.com" {
		t.Errorf("host bucket: %+v %v", b, ok)
	}
	if b, ok := bucketOf(st, model.StatsProcess, "Safari"); !ok || b.Requests != 3 || b.Detail != "/Applications/Safari.app" {
		t.Errorf("process bucket: %+v %v", b, ok)
	}
	if b, ok := bucketOf(st, model.StatsRule, "DomainSuffix,example.com"); !ok || b.Requests != 3 {
		t.Errorf("rule bucket: %+v %v", b, ok)
	}
	if b, ok := bucketOf(st, model.StatsProxy, "节点选择/东京 03"); !ok || b.Detail != "东京 03" {
		t.Errorf("proxy bucket: %+v %v", b, ok)
	}
	if b, ok := bucketOf(st, model.StatsCountry, "US"); !ok || b.Requests != 3 {
		t.Errorf("country bucket: %+v %v", b, ok)
	}
	if b, ok := bucketOf(st, model.StatsInbound, "HTTP"); !ok || b.Download != 1720 {
		t.Errorf("inbound bucket: %+v %v", b, ok)
	}
	if st.Buckets[model.StatsDomain][0].Key != "google.com" {
		t.Errorf("buckets should come biggest first: %+v", st.Buckets[model.StatsDomain])
	}
	// Hourly series: everything landed in hour 10.
	if !st.Hourly || len(st.Series) != 24 || st.Series[10].Download != 1720 || st.Series[10].Requests != 3 || st.Series[9].Download != 0 {
		t.Errorf("hourly series: %+v", st.Series[10])
	}
	if !st.Since.Equal(time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local)) {
		t.Errorf("since = %v", st.Since)
	}
}

// TestStatsDomains checks the registrable-domain mapping.
func TestStatsDomains(t *testing.T) {
	for host, want := range map[string]string{
		"www.google.com": "google.com", "rr3---sn-i3b7kn7s.googlevideo.com": "googlevideo.com", "www.bbc.co.uk": "bbc.co.uk",
		"a.b.example.com.cn": "example.com.cn", "s3.ap-northeast-1.amazonaws.com": "amazonaws.com", "1.1.1.1": "1.1.1.1",
		"localhost": "localhost", "printer.local": "printer.local", "x.y.internal": "y.internal", "": "",
	} {
		if got := registrableDomain(host); got != want {
			t.Errorf("registrableDomain(%q) = %q, want %q", host, got, want)
		}
	}
	if statsHost("[::1]:53") != "::1" || statsHost("WWW.A.com:443") != "www.a.com" || statsHost("noport") != "noport" {
		t.Error("statsHost")
	}
}

// TestStatsBucketCap: a day keeps at most statsBucketCap buckets per
// dimension on disk, the rest folded into 「其他」 with nothing lost.
func TestStatsBucketCap(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	s := newStatsStore(dir, func() time.Time { return now }, nil)
	var conns []model.Connection
	const n = 2*statsBucketCap + 200
	var sum int64
	for i := 0; i < n; i++ {
		c := statsConn(fmt.Sprint("c", i), fmt.Sprintf("h%d.example.com", i), "127.0.0.1", int64(i), int64(2*i))
		sum += int64(3 * i)
		conns = append(conns, c)
	}
	s.record(conns, now)
	s.mu.Lock()
	inMem := len(s.days[now.Format(statsDayLayout)].dims[model.StatsHost])
	s.mu.Unlock()
	if inMem > 2*statsBucketCap+1 {
		t.Errorf("in-memory buckets = %d, want at most %d", inMem, 2*statsBucketCap+1)
	}
	if err := s.flush(); err != nil {
		t.Fatal(err)
	}
	fresh := newStatsStore(dir, func() time.Time { return now }, nil)
	if err := fresh.load(); err != nil {
		t.Fatal(err)
	}
	st := fresh.snapshot(model.StatsToday)
	hosts := st.Buckets[model.StatsHost]
	if len(hosts) != statsBucketCap+1 {
		t.Fatalf("host buckets after the cap = %d, want %d", len(hosts), statsBucketCap+1)
	}
	var total, reqs int64
	var other *model.StatsBucket
	for i := range hosts {
		total += hosts[i].Total()
		reqs += hosts[i].Requests
		if hosts[i].Key == model.StatsOtherKey {
			other = &hosts[i]
		}
	}
	if total != sum || reqs != n {
		t.Errorf("bytes/requests lost in the fold: %d/%d, want %d/%d", total, reqs, sum, n)
	}
	if other == nil || other.Requests < 200 {
		t.Errorf("no remainder bucket: %+v", other)
	}
	// The biggest keys survived.
	biggest := hosts[0].Key
	if biggest == model.StatsOtherKey {
		biggest = hosts[1].Key
	}
	if biggest != fmt.Sprintf("h%d.example.com", n-1) {
		t.Errorf("biggest bucket = %q", biggest)
	}
	if st.Totals.Devices != 1 {
		t.Errorf("devices = %d", st.Totals.Devices)
	}
}

// TestStatsPersistenceAndRetention: a flush writes one file per day
// atomically, a new store loads it back, expired days are deleted on
// load and on flush, and reset removes everything.
func TestStatsPersistenceAndRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.Local)
	clock := func() time.Time { return now }
	s := newStatsStore(dir, clock, nil)
	old := now.AddDate(0, 0, -statsRetentionDays) // just outside the window
	edge := now.AddDate(0, 0, 1-statsRetentionDays)
	s.record([]model.Connection{statsConn("a", "a.com", "127.0.0.1", 1, 2)}, old)
	s.record([]model.Connection{statsConn("b", "b.com", "127.0.0.1", 3, 4)}, edge)
	s.record([]model.Connection{statsConn("c", "c.com", "10.0.0.2", 5, 6)}, now)
	if err := s.flush(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	// The edge day is in a past month: it lives in that month's file.
	if len(names) != 2 || names[0] != edge.Format(statsMonthLayout)+statsFileExt || names[1] != now.Format(statsDayLayout)+statsFileExt {
		t.Fatalf("files after flush: %v", names)
	}
	// A flush with nothing dirty writes nothing.
	before, _ := os.Stat(filepath.Join(dir, now.Format(statsDayLayout)+statsFileExt))
	time.Sleep(10 * time.Millisecond)
	if err := s.flush(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(dir, now.Format(statsDayLayout)+statsFileExt))
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a clean day was rewritten")
	}

	// Round trip.
	fresh := newStatsStore(dir, clock, nil)
	if err := fresh.load(); err != nil {
		t.Fatal(err)
	}
	st := fresh.snapshot(model.StatsAll)
	if st.Totals.Upload != 8 || st.Totals.Download != 10 || st.Totals.Requests != 2 || st.Totals.Devices != 2 || st.Days != 2 {
		t.Fatalf("round trip: %+v days %d", st.Totals, st.Days)
	}
	if b, ok := bucketOf(st, model.StatsProcess, "Safari"); !ok || !b.LastSeen.Equal(now) || b.Detail != "/Applications/Safari.app" {
		t.Errorf("bucket fields lost: %+v", b)
	}
	if !st.Since.Equal(startOfDay(edge)) {
		t.Errorf("since of 全部 = %v, want %v", st.Since, startOfDay(edge))
	}

	// A day on disk expires on load (a first-version file too); a stray
	// file is ignored, a broken one skipped.
	_ = os.WriteFile(filepath.Join(dir, old.Format(statsDayLayout)+statsLegacyExt), []byte(`{"date":"`+old.Format(statsDayLayout)+`"}`), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "2026-11-09"+statsFileExt), []byte("{broken"), 0o600)
	again := newStatsStore(dir, clock, nil)
	if err := again.load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, old.Format(statsDayLayout)+statsLegacyExt)); !os.IsNotExist(err) {
		t.Error("expired file not deleted on load")
	}
	if again.snapshot(model.StatsAll).Days != 2 {
		t.Errorf("days after load = %d", again.snapshot(model.StatsAll).Days)
	}
	// Time passes: the edge day expires on the next flush.
	now = now.AddDate(0, 0, 1)
	if err := again.flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, edge.Format(statsMonthLayout)+statsFileExt)); !os.IsNotExist(err) {
		t.Error("expired day's month file not deleted on flush")
	}
	if again.snapshot(model.StatsAll).Days != 1 {
		t.Errorf("days after expiry = %d", again.snapshot(model.StatsAll).Days)
	}

	// Reset: memory and files gone; an open connection counts only its growth afterwards.
	again.record([]model.Connection{statsConn("c", "c.com", "10.0.0.2", 50, 60)}, now)
	if err := again.reset(); err != nil {
		t.Fatal(err)
	}
	if st := again.snapshot(model.StatsAll); st.Totals.Requests != 0 || st.Days != 0 {
		t.Errorf("after reset: %+v", st.Totals)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 || entries[0].Name() != "notes.txt" {
		t.Errorf("files after reset: %v", entries)
	}
	again.record([]model.Connection{statsConn("c", "c.com", "10.0.0.2", 55, 70)}, now)
	if st := again.snapshot(model.StatsToday); st.Totals.Upload != 5 || st.Totals.Download != 10 || st.Totals.Requests != 0 {
		t.Errorf("after reset only the growth counts: %+v", st.Totals)
	}
}

// TestStatsRanges: 今天 / 本周 / 本月 / 全部 select the right days, and the
// daily series covers the last 30 days.
func TestStatsRanges(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local) // a Wednesday
	s := newStatsStore(t.TempDir(), func() time.Time { return now }, nil)
	days := map[string]time.Time{
		"today":     now,
		"monday":    time.Date(2026, 11, 9, 8, 0, 0, 0, time.Local),
		"sunday":    time.Date(2026, 11, 8, 23, 0, 0, 0, time.Local), // last week
		"lastMonth": time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local),
	}
	for name, at := range days {
		s.record([]model.Connection{statsConn(name, name+".com", "127.0.0.1", 1, 10)}, at)
	}
	want := map[string]int64{model.StatsToday: 1, model.StatsWeek: 2, model.StatsMonth: 3, model.StatsAll: 4}
	for rng, n := range want {
		st := s.snapshot(rng)
		if st.Totals.Requests != n || st.Totals.Download != 10*n {
			t.Errorf("%s: %+v, want %d requests", rng, st.Totals, n)
		}
		if st.Range != rng {
			t.Errorf("range echoed as %q", st.Range)
		}
	}
	if since := s.snapshot(model.StatsWeek).Since; !since.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, time.Local)) {
		t.Errorf("week since = %v", since)
	}
	if since := s.snapshot(model.StatsMonth).Since; !since.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)) {
		t.Errorf("month since = %v", since)
	}
	st := s.snapshot(model.StatsAll)
	if st.Hourly || len(st.Series) != statsSeriesDays || !st.Series[statsSeriesDays-1].Time.Equal(startOfDay(now)) {
		t.Fatalf("daily series: hourly=%v len=%d last=%v", st.Hourly, len(st.Series), st.Series[len(st.Series)-1].Time)
	}
	if st.Series[statsSeriesDays-1].Download != 10 || st.Series[statsSeriesDays-3].Download != 10 || st.Series[statsSeriesDays-2].Download != 0 {
		t.Errorf("daily series values wrong: %+v", st.Series[statsSeriesDays-3:])
	}
	if st.Series[statsSeriesDays-23].Download != 10 { // 2026-10-20 is 22 days before
		t.Errorf("last month's day missing from the series")
	}
	if s.snapshot("bogus").Range != model.StatsToday {
		t.Error("an unknown range should fall back to today")
	}
	// The cache serves the same answer until something changes.
	a := s.snapshot(model.StatsAll)
	s.record([]model.Connection{statsConn("later", "today.com", "127.0.0.1", 1, 11)}, now)
	if b := s.snapshot(model.StatsAll); b.Totals.Download != a.Totals.Download+11 {
		t.Errorf("stale snapshot after a change: %d vs %d", b.Totals.Download, a.Totals.Download)
	}
}

// TestStatsFromConnectionStream: the service feeds the stream's snapshots
// into the statistics and emits TopicStats.
func TestStatsFromConnectionStream(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	got := make(chan Topic, 16)
	off := h.s.OnChange(func(tp Topic) {
		if tp == TopicStats {
			select {
			case got <- tp:
			default:
			}
		}
	})
	defer off()
	c := controller.Connection{ID: "x", Upload: 10, Download: 100, Chains: []string{"东京 03", "节点选择"}, Rule: "Match",
		Metadata: controller.Metadata{Host: "www.example.com", DestinationPort: "443", SourceIP: "127.0.0.1", Type: "Socks5", Network: "tcp"}}
	h.ctrl.connsC <- controller.Connections{Connections: []controller.Connection{c}}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no TopicStats after a connection snapshot")
	}
	st := h.s.TrafficStats(model.StatsToday)
	if st.Totals.Upload != 10 || st.Totals.Download != 100 || st.Totals.Requests != 1 {
		t.Fatalf("stats from the stream: %+v", st.Totals)
	}
	if b, ok := bucketOf(st, model.StatsProxy, "节点选择/东京 03"); !ok || b.Requests != 1 {
		t.Errorf("proxy bucket from the stream: %+v %v", b, ok)
	}
	if err := h.s.ResetTrafficStats(); err != nil {
		t.Fatal(err)
	}
	if st := h.s.TrafficStats(model.StatsAll); st.Totals.Requests != 0 {
		t.Errorf("after reset: %+v", st.Totals)
	}
	h.s.Shutdown()
	if entries, _ := os.ReadDir(filepath.Join(h.s.opt.DataDir, "stats")); len(entries) != 0 {
		t.Errorf("reset statistics written back on shutdown: %v", entries)
	}
}
