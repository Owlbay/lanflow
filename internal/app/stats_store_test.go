package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lanflow/internal/model"
)

// loadedDay fills a day with n buckets in every dimension, as a busy day
// of browsing would (distinct hosts, domains, processes, rules, nodes).
func loadedDay(s *statsStore, at time.Time, n int) {
	conns := make([]model.Connection, 0, n)
	for i := 0; i < n; i++ {
		c := statsConn(fmt.Sprintf("c%d", i), fmt.Sprintf("cdn%d.example%d.com", i%7, i), fmt.Sprintf("192.168.1.%d", i%40), int64(1000+i*37), int64(50000+i*911))
		c.Process = fmt.Sprintf("App %d", i%60)
		c.ProcessPath = fmt.Sprintf("/Applications/App %d.app", i%60)
		c.RulePayload = fmt.Sprintf("example%d.com", i)
		c.Chains = []string{"节点选择", fmt.Sprintf("节点 %02d", i%30)}
		c.DstIP = fmt.Sprintf("104.16.%d.%d", i%200, i%250)
		conns = append(conns, c)
	}
	s.record(conns, at)
}

// legacyBytes encodes a day as the first version did: plain JSON with
// RFC 3339 times.
func legacyBytes(t *testing.T, d *statsDay, now time.Time) []byte {
	t.Helper()
	type legacyBucket struct {
		Key    string    `json:"k"`
		Detail string    `json:"dt,omitempty"`
		Req    int64     `json:"n"`
		Up     int64     `json:"u"`
		Down   int64     `json:"d"`
		Last   time.Time `json:"t"`
	}
	type legacyFile struct {
		Date  string                    `json:"date"`
		Hours [24]statsPointFile        `json:"hours"`
		Dims  map[string][]legacyBucket `json:"dims"`
		Saved time.Time                 `json:"saved"`
	}
	f := d.toFile(now)
	out := legacyFile{Date: f.Date, Hours: f.Hours, Dims: map[string][]legacyBucket{}, Saved: now}
	for dim, bs := range f.Dims {
		for _, b := range bs {
			out.Dims[dim] = append(out.Dims[dim], legacyBucket{b.Key, b.Detail, b.Req, b.Up, b.Down, time.Time(b.Last)})
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func statsFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestStatsLegacyMigration: a first-version plain day file is read, and
// the next flush rewrites it compressed and removes the plain file, with
// nothing lost (times to the second).
func TestStatsLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.Local)
	clock := func() time.Time { return now }
	src := newStatsStore(dir, clock, func(string) string { return "US" })
	loadedDay(src, now.Add(-time.Hour), 300)
	day := src.days[now.Format(statsDayLayout)]
	legacy := legacyBytes(t, day, now)
	if err := os.WriteFile(filepath.Join(dir, day.date+statsLegacyExt), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	want := src.snapshot(model.StatsToday)

	s := newStatsStore(dir, clock, nil)
	if err := s.load(); err != nil {
		t.Fatal(err)
	}
	got := s.snapshot(model.StatsToday)
	if got.Totals != want.Totals || len(got.Buckets[model.StatsHost]) != len(want.Buckets[model.StatsHost]) {
		t.Fatalf("legacy file read wrong: %+v vs %+v", got.Totals, want.Totals)
	}
	if b, _ := bucketOf(got, model.StatsProcess, "App 1"); !b.LastSeen.Equal(now.Add(-time.Hour)) {
		t.Errorf("legacy time lost: %v", b.LastSeen)
	}
	if err := s.flush(); err != nil {
		t.Fatal(err)
	}
	if names := statsFiles(t, dir); len(names) != 1 || names[0] != day.date+statsFileExt {
		t.Fatalf("after migration: %v", names)
	}
	// Round trip of the new file.
	again := newStatsStore(dir, clock, nil)
	if err := again.load(); err != nil {
		t.Fatal(err)
	}
	back := again.snapshot(model.StatsToday)
	if back.Totals != want.Totals {
		t.Fatalf("round trip: %+v vs %+v", back.Totals, want.Totals)
	}
	if b, _ := bucketOf(back, model.StatsProcess, "App 1"); !b.LastSeen.Equal(now.Add(-time.Hour)) {
		t.Errorf("time lost in the new file: %v", b.LastSeen)
	}
	packed, _ := os.Stat(filepath.Join(dir, day.date+statsFileExt))
	t.Logf("a day of %d buckets per dimension: plain %d bytes, compressed %d bytes (%.1fx smaller)", 300, len(legacy), packed.Size(), float64(len(legacy))/float64(packed.Size()))
	if packed.Size() > int64(len(legacy))/4 {
		t.Errorf("compressed day should be at most a quarter of the plain one: %d vs %d", packed.Size(), len(legacy))
	}
}

// TestStatsMonthCompaction: the days of a month that ended fold into one
// month file at the next flush, their day files removed; a day of the
// current month keeps its own file; expired days leave the month file,
// which goes when empty; reset removes everything.
func TestStatsMonthCompaction(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	clock := func() time.Time { return now }
	s := newStatsStore(dir, clock, nil)
	d1, d2 := now.AddDate(0, 0, -2), now.AddDate(0, 0, -1)
	s.record([]model.Connection{statsConn("a", "a.com", "127.0.0.1", 1, 2)}, d1)
	s.record([]model.Connection{statsConn("b", "b.com", "127.0.0.1", 3, 4)}, d2)
	s.record([]model.Connection{statsConn("c", "c.com", "127.0.0.1", 5, 6)}, now)
	if err := s.flush(); err != nil {
		t.Fatal(err)
	}
	if names := statsFiles(t, dir); len(names) != 3 {
		t.Fatalf("three day files of the current month expected: %v", names)
	}
	// November: October's days fold into 2026-10.json.gz.
	now = time.Date(2026, 11, 2, 8, 0, 0, 0, time.Local)
	s.record([]model.Connection{statsConn("d", "d.com", "127.0.0.1", 7, 8)}, now)
	if err := s.flush(); err != nil {
		t.Fatal(err)
	}
	names := statsFiles(t, dir)
	if len(names) != 2 || names[0] != "2026-10"+statsFileExt || names[1] != "2026-11-02"+statsFileExt {
		t.Fatalf("after the month ended: %v", names)
	}
	fresh := newStatsStore(dir, clock, nil)
	if err := fresh.load(); err != nil {
		t.Fatal(err)
	}
	st := fresh.snapshot(model.StatsAll)
	if st.Totals.Upload != 16 || st.Totals.Download != 20 || st.Totals.Requests != 4 || st.Days != 4 {
		t.Fatalf("after compaction: %+v days %d", st.Totals, st.Days)
	}
	if b, ok := bucketOf(st, model.StatsHost, "a.com"); !ok || !b.LastSeen.Equal(d1) {
		t.Errorf("a day of the month file: %+v %v", b, ok)
	}
	// A clean flush writes nothing.
	before, _ := os.Stat(filepath.Join(dir, "2026-10"+statsFileExt))
	time.Sleep(10 * time.Millisecond)
	if err := fresh.flush(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(filepath.Join(dir, "2026-10"+statsFileExt)); !after.ModTime().Equal(before.ModTime()) {
		t.Error("a clean month was rewritten")
	}
	// Retention: once d1 expires the month file is rewritten without it.
	now = d1.AddDate(0, 0, statsRetentionDays)
	if err := fresh.flush(); err != nil {
		t.Fatal(err)
	}
	again := newStatsStore(dir, clock, nil)
	if err := again.load(); err != nil {
		t.Fatal(err)
	}
	if st := again.snapshot(model.StatsAll); st.Days != 3 || st.Totals.Upload != 15 {
		t.Errorf("after d1 expired: days %d totals %+v", st.Days, st.Totals)
	}
	// Expiry found on load: a month file holding only expired days goes
	// (October has d2 and the 30th left; both expire 90 days after the 30th).
	now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local).AddDate(0, 0, statsRetentionDays)
	late := newStatsStore(dir, clock, nil)
	if err := late.load(); err != nil {
		t.Fatal(err)
	}
	if err := late.flush(); err != nil {
		t.Fatal(err)
	}
	// November's day, now in a past month too, has folded into its month file.
	if names := statsFiles(t, dir); len(names) != 1 || names[0] != "2026-11"+statsFileExt {
		t.Errorf("October should be gone once its days expired: %v", names)
	}
	if st := late.snapshot(model.StatsAll); st.Days != 1 || st.Totals.Upload != 7 {
		t.Errorf("only November's day should remain: days %d totals %+v", st.Days, st.Totals)
	}
	if err := late.reset(); err != nil {
		t.Fatal(err)
	}
	if names := statsFiles(t, dir); len(names) != 0 {
		t.Errorf("reset left %v", names)
	}
}

// TestStatsTimeEncoding: times marshal as Unix seconds and read back
// from both forms; zero stays zero.
func TestStatsTimeEncoding(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 30, 15, 0, time.Local)
	b, err := json.Marshal(statsTime(at))
	if err != nil || string(b) != "1794367815" && string(b) != fmt.Sprint(at.Unix()) {
		t.Fatalf("marshal: %s %v", b, err)
	}
	var back statsTime
	if err := json.Unmarshal(b, &back); err != nil || !time.Time(back).Equal(at) {
		t.Errorf("unix round trip: %v %v", time.Time(back), err)
	}
	if err := json.Unmarshal([]byte(`"2026-11-10T09:30:15+08:00"`), &back); err != nil || time.Time(back).Unix() != time.Date(2026, 11, 10, 1, 30, 15, 0, time.UTC).Unix() {
		t.Errorf("rfc3339: %v %v", time.Time(back), err)
	}
	var zero statsTime
	if b, _ := json.Marshal(zero); string(b) != "0" {
		t.Errorf("zero marshals as %s", b)
	}
	if err := json.Unmarshal([]byte("0"), &back); err != nil || !time.Time(back).IsZero() {
		t.Errorf("zero round trip: %v", time.Time(back))
	}
}
