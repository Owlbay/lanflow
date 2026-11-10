package app

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"lanflow/internal/model"
)

// Persistence of the 流量统计 under <DataDir>/stats:
//
//   - <YYYY-MM-DD>.json.gz holds one day of the current month, written
//     atomically when the day is dirty (about once a minute while traffic
//     flows); gzip over short-keyed JSON with Unix-second times is 6–10×
//     smaller than the plain JSON of the first version.
//   - <YYYY-MM>.json.gz holds every day of a past month in one file: a
//     flush folds the day files of a month that ended into it (the
//     strings the days share compress together), so the directory stays
//     at a handful of files rather than one per day.
//   - <YYYY-MM-DD>.json is the first version's plain file; it is read as
//     before and rewritten in the new form on the next flush, which then
//     removes it.
//
// Days older than statsRetentionDays are deleted when the store loads
// and when it flushes; a month file whose days expire is rewritten
// without them, and removed when none is left.

const (
	statsFileExt     = ".json.gz"
	statsLegacyExt   = ".json"
	statsTmpExt      = ".tmp"
	statsMonthLayout = "2006-01"
)

// statsTime marshals as Unix seconds (the first version wrote RFC 3339
// strings, which decode too). Zero marshals as 0.
type statsTime time.Time

func (t statsTime) MarshalJSON() ([]byte, error) {
	tt := time.Time(t)
	if tt.IsZero() {
		return []byte("0"), nil
	}
	return strconv.AppendInt(nil, tt.Unix(), 10), nil
}

func (t *statsTime) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*t = statsTime{}
		return nil
	}
	if b[0] == '"' {
		var tt time.Time
		if err := json.Unmarshal(b, &tt); err != nil {
			return err
		}
		*t = statsTime(tt)
		return nil
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return err
	}
	if n == 0 {
		*t = statsTime{}
		return nil
	}
	*t = statsTime(time.Unix(n, 0))
	return nil
}

// statsDayFile is the on-disk form of a statsDay.
type statsDayFile struct {
	Date  string                       `json:"date"`
	Hours [24]statsPointFile           `json:"hours"`
	Dims  map[string][]statsBucketFile `json:"dims"`
	Saved statsTime                    `json:"saved"`
}

// statsMonthFile is the on-disk form of a past month: its days.
type statsMonthFile struct {
	Month string         `json:"month"`
	Days  []statsDayFile `json:"days"`
	Saved statsTime      `json:"saved"`
}

// statsAnyFile reads either form.
type statsAnyFile struct {
	statsDayFile
	Month string         `json:"month"`
	Days  []statsDayFile `json:"days"`
}

type statsPointFile struct {
	Up   int64 `json:"u,omitempty"`
	Down int64 `json:"d,omitempty"`
	Req  int64 `json:"n,omitempty"`
}

type statsBucketFile struct {
	Key    string    `json:"k"`
	Detail string    `json:"dt,omitempty"`
	Req    int64     `json:"n"`
	Up     int64     `json:"u"`
	Down   int64     `json:"d"`
	Last   statsTime `json:"t"`
}

// toFile folds the day to the cap and converts it to its file form.
func (d *statsDay) toFile(now time.Time) statsDayFile {
	f := statsDayFile{Date: d.date, Dims: make(map[string][]statsBucketFile, len(d.dims)), Saved: statsTime(now)}
	for i, h := range d.hours {
		f.Hours[i] = statsPointFile{Up: h.Upload, Down: h.Download, Req: h.Requests}
	}
	for dim, m := range d.dims {
		foldBuckets(m, statsBucketCap)
		bs := make([]*model.StatsBucket, 0, len(m))
		for _, b := range m {
			bs = append(bs, b)
		}
		sortBuckets(bs)
		out := make([]statsBucketFile, len(bs))
		for i, b := range bs {
			out[i] = statsBucketFile{Key: b.Key, Detail: b.Detail, Req: b.Requests, Up: b.Upload, Down: b.Download, Last: statsTime(b.LastSeen)}
		}
		f.Dims[string(dim)] = out
	}
	return f
}

// fromFile builds a day from its file form.
func (f *statsDayFile) day() *statsDay {
	d := newStatsDay(f.Date)
	for i, h := range f.Hours {
		d.hours[i] = model.StatsPoint{Upload: h.Up, Download: h.Down, Requests: h.Req}
	}
	for dim, bs := range f.Dims {
		m := make(map[string]*model.StatsBucket, len(bs))
		for _, b := range bs {
			m[b.Key] = &model.StatsBucket{Key: b.Key, Detail: b.Detail, Requests: b.Req, Upload: b.Up, Download: b.Down, LastSeen: time.Time(b.Last)}
		}
		d.dims[model.StatsDimension(dim)] = m
	}
	return d
}

// encode folds the day to the cap and marshals it, compressed.
func (d *statsDay) encode(now time.Time) ([]byte, error) {
	return gzipJSON(d.toFile(now))
}

// encodeMonth marshals the days of one month, compressed.
func encodeMonth(month string, days []*statsDay, now time.Time) ([]byte, error) {
	sort.Slice(days, func(i, j int) bool { return days[i].date < days[j].date })
	f := statsMonthFile{Month: month, Days: make([]statsDayFile, len(days)), Saved: statsTime(now)}
	for i, d := range days {
		f.Days[i] = d.toFile(now)
	}
	return gzipJSON(f)
}

// gzipJSON marshals v as JSON and compresses it.
func gzipJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	enc := json.NewEncoder(zw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeStatsFile parses a day or a month file, plain or compressed, and
// returns its days.
func decodeStatsFile(data []byte) ([]*statsDay, error) {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if data, err = io.ReadAll(zr); err != nil {
			return nil, err
		}
	}
	var f statsAnyFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if len(f.Days) > 0 {
		days := make([]*statsDay, 0, len(f.Days))
		for i := range f.Days {
			days = append(days, f.Days[i].day())
		}
		return days, nil
	}
	return []*statsDay{f.statsDayFile.day()}, nil
}

// decodeStatsDay parses a single day file (the first version's form or
// the compressed one).
func decodeStatsDay(data []byte) (*statsDay, error) {
	days, err := decodeStatsFile(data)
	if err != nil {
		return nil, err
	}
	return days[0], nil
}

// dayFile is the path of a day's own file.
func (s *statsStore) dayFile(date string) string { return filepath.Join(s.dir, date+statsFileExt) }

// monthFile is the path of a month's file.
func (s *statsStore) monthFile(month string) string {
	return filepath.Join(s.dir, month+statsFileExt)
}

// monthOf is the month key of a day key.
func monthOf(date string) string {
	if len(date) >= len(statsMonthLayout) {
		return date[:len(statsMonthLayout)]
	}
	return date
}

// retainedSince is the oldest day key kept as of now.
func retainedSince(now time.Time) string {
	return startOfDay(now).AddDate(0, 0, 1-statsRetentionDays).Format(statsDayLayout)
}

// statsEntry classifies a file of the stats directory.
type statsEntry struct {
	path  string
	key   string // day or month key
	month bool   // a month file
	old   bool   // the first version's plain day file
}

// classify names the stats file name, or ok false for anything else.
func classifyStatsFile(name string) (e statsEntry, ok bool) {
	switch {
	case strings.HasSuffix(name, statsFileExt):
		e.key = strings.TrimSuffix(name, statsFileExt)
	case strings.HasSuffix(name, statsLegacyExt):
		e.key = strings.TrimSuffix(name, statsLegacyExt)
		e.old = true
	default:
		return e, false
	}
	if _, err := time.Parse(statsDayLayout, e.key); err == nil {
		return e, true
	}
	if _, err := time.Parse(statsMonthLayout, e.key); err == nil && !e.old {
		e.month = true
		return e, true
	}
	return e, false
}

// load reads every retained day and deletes the expired ones. A file that
// does not parse is skipped (and left in place). Days read from the first
// version's plain files, and days of a past month still in files of their
// own, are marked dirty so that the next flush rewrites them compactly.
func (s *statsStore) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	now := s.now()
	oldest := retainedSince(now)
	curMonth := now.Format(statsMonthLayout)
	days := map[string]*statsDay{}
	for _, e := range entries {
		fe, ok := classifyStatsFile(e.Name())
		if e.IsDir() || !ok {
			continue
		}
		fe.path = filepath.Join(s.dir, e.Name())
		if !fe.month && fe.key < oldest || fe.month && fe.key < monthOf(oldest) {
			_ = os.Remove(fe.path)
			continue
		}
		data, rerr := os.ReadFile(fe.path)
		if rerr != nil {
			continue
		}
		read, derr := decodeStatsFile(data)
		if derr != nil {
			continue
		}
		dropped := false
		for _, d := range read {
			if _, perr := time.Parse(statsDayLayout, d.date); perr != nil {
				continue
			}
			if fe.month && monthOf(d.date) != fe.key || !fe.month && d.date != fe.key {
				continue // a file holding another day's data is not trusted
			}
			if d.date < oldest {
				dropped = true
				continue
			}
			if !fe.month {
				d.file = fe.path
				// Migrate the first version's files; fold the days of a
				// month that ended into its month file.
				d.dirty = fe.old || monthOf(d.date) != curMonth
			}
			if prev, ok := days[d.date]; ok {
				// The same day in two files (a month file and a stray day
				// file): the day file is the newer write.
				if prev.file == "" {
					days[d.date] = d
				}
				continue
			}
			days[d.date] = d
		}
		if fe.month && dropped {
			// Rewrite the month without the expired days (or remove it).
			for _, d := range days {
				if monthOf(d.date) == fe.key {
					d.dirty = true
				}
			}
			s.mu.Lock()
			s.rewrite[fe.key] = true
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	for k, d := range days {
		if cur, ok := s.days[k]; ok {
			// Data recorded before load finished stays on top of the file's.
			mergeStatsDay(cur, d)
			cur.dirty = true
			cur.file = d.file
			continue
		}
		s.days[k] = d
	}
	s.gen++
	s.mu.Unlock()
	return nil
}

// mergeStatsDay adds from into into.
func mergeStatsDay(into, from *statsDay) {
	for i := range into.hours {
		into.hours[i].Upload += from.hours[i].Upload
		into.hours[i].Download += from.hours[i].Download
		into.hours[i].Requests += from.hours[i].Requests
	}
	for dim, m := range from.dims {
		for key, b := range m {
			cur := into.dims[dim]
			if cur == nil {
				cur = map[string]*model.StatsBucket{}
				into.dims[dim] = cur
			}
			if o, ok := cur[key]; ok {
				o.Requests += b.Requests
				o.Upload += b.Upload
				o.Download += b.Download
				if b.LastSeen.After(o.LastSeen) {
					o.LastSeen = b.LastSeen
				}
				if o.Detail == "" {
					o.Detail = b.Detail
				}
				continue
			}
			cp := *b
			cur[key] = &cp
		}
	}
}

// flush writes what changed and drops the expired days: a dirty day of
// the current month to its own file, the days of any other month with a
// dirty day to that month's file (removing their day files). Encoding
// runs under the lock, the disk writes outside it.
func (s *statsStore) flush() error {
	now := s.now()
	oldest := retainedSince(now)
	curMonth := now.Format(statsMonthLayout)
	type pending struct {
		path string
		data []byte
	}
	var writes []pending
	var remove []string
	s.mu.Lock()
	months := map[string]bool{} // past months to rewrite
	for m := range s.rewrite {
		months[m] = true
	}
	s.rewrite = map[string]bool{}
	byMonth := map[string][]*statsDay{}
	for k, d := range s.days {
		if k < oldest {
			delete(s.days, k)
			if d.file != "" {
				remove = append(remove, d.file)
			} else {
				months[monthOf(k)] = true
			}
			s.gen++
			continue
		}
		m := monthOf(k)
		if m != curMonth {
			byMonth[m] = append(byMonth[m], d)
			if d.dirty || d.file != "" {
				months[m] = true
			}
			continue
		}
		if !d.dirty {
			continue
		}
		data, err := d.encode(now)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		d.dirty = false
		path := s.dayFile(k)
		if d.file != "" && d.file != path {
			remove = append(remove, d.file) // the first version's plain file
		}
		d.file = path
		writes = append(writes, pending{path, data})
	}
	for m := range months {
		days := byMonth[m]
		if len(days) == 0 {
			remove = append(remove, s.monthFile(m))
			continue
		}
		data, err := encodeMonth(m, days, now)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		for _, d := range days {
			d.dirty = false
			if d.file != "" {
				remove = append(remove, d.file)
				d.file = ""
			}
		}
		writes = append(writes, pending{s.monthFile(m), data})
	}
	s.mu.Unlock()
	var first error
	for _, w := range writes {
		if err := writeFileAtomic(w.path, w.data, 0o600); err != nil && first == nil {
			first = err
		}
	}
	for _, p := range remove {
		_ = os.Remove(p)
	}
	return first
}

// reset forgets everything in memory and on disk. The last byte totals of
// the open connections are kept, so only their future bytes count.
func (s *statsStore) reset() error {
	s.mu.Lock()
	s.days = map[string]*statsDay{}
	s.cache = map[string]cachedStats{}
	s.rewrite = map[string]bool{}
	s.gen++
	s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var first error
	for _, e := range entries {
		name := e.Name()
		_, ok := classifyStatsFile(strings.TrimSuffix(name, statsTmpExt))
		if e.IsDir() || !ok {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// diskSize is the size of the stats directory's files, in bytes.
func (s *statsStore) diskSize() int64 {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			n += info.Size()
		}
	}
	return n
}
