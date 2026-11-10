package applog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"lanflow/internal/model"
)

// Local error collection for feedback reports (docs/feedback-api.md):
// errors.jsonl in the log directory holds one JSON object per line
// (model.ErrorRecord) for every Error level entry of the run log, every
// unexpected exit of the core and every Go panic. Messages pass Redact
// before they are written; connection details, domain lists and
// subscription links are never recorded on purpose. The file rotates to
// errors.jsonl.1 at maxErrorRecords lines or maxErrorBytes, whichever
// comes first.

// ErrorsFileName is the error log under the log directory.
const ErrorsFileName = "errors.jsonl"

// CrashFileName holds the previous run's crash record until it is
// reported or dismissed; CrashOutputName is where the Go runtime writes a
// fatal error it cannot recover from (debug.SetCrashOutput).
const (
	CrashFileName   = "last-crash.json"
	CrashOutputName = "crash.txt"
)

var (
	maxErrorRecords       = 500
	maxErrorBytes   int64 = 512 << 10
)

const (
	maxMessage = 2 << 10
	maxDetail  = 16 << 10
)

// ErrorLog appends error records to errors.jsonl. A nil *ErrorLog drops
// everything.
type ErrorLog struct {
	mu       sync.Mutex
	dir      string
	path     string
	version  string
	platform string
	lines    int
	size     int64
	now      func() time.Time
	onAdd    func(model.ErrorRecord)
}

// OpenErrors opens dir/errors.jsonl; version is stamped on every record.
func OpenErrors(dir, version string) *ErrorLog {
	e := &ErrorLog{
		dir:      dir,
		path:     filepath.Join(dir, ErrorsFileName),
		version:  version,
		platform: runtime.GOOS + "/" + runtime.GOARCH,
		now:      time.Now,
	}
	_ = os.MkdirAll(dir, 0o700)
	if data, err := os.ReadFile(e.path); err == nil {
		e.size = int64(len(data))
		e.lines = bytes.Count(data, []byte("\n"))
	}
	return e
}

// Path is the file ("" for nil).
func (e *ErrorLog) Path() string {
	if e == nil {
		return ""
	}
	return e.path
}

// SetClock replaces time.Now (tests).
func (e *ErrorLog) SetClock(now func() time.Time) {
	if e != nil {
		e.mu.Lock()
		e.now = now
		e.mu.Unlock()
	}
}

// OnAdd sets fn, called (outside the lock) after every record.
func (e *ErrorLog) OnAdd(fn func(model.ErrorRecord)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.onAdd = fn
	e.mu.Unlock()
}

// Add records one event of kind; message and detail are redacted and
// shortened. It returns the record as written.
func (e *ErrorLog) Add(kind, message, detail string) model.ErrorRecord {
	return e.add(model.ErrorRecord{Kind: kind, Message: message, Detail: detail})
}

// AddExit records an unexpected exit of the core with its exit code
// (nil when unknown) and the last lines of its log.
func (e *ErrorLog) AddExit(message string, code *int, logTail string) model.ErrorRecord {
	return e.add(model.ErrorRecord{Kind: model.ErrorKindCoreExit, Message: message, Detail: logTail, ExitCode: code})
}

func (e *ErrorLog) add(r model.ErrorRecord) model.ErrorRecord {
	if e == nil {
		return r
	}
	r.Message = clip(strings.ReplaceAll(Redact(strings.TrimSpace(r.Message)), "\n", " ⏎ "), maxMessage)
	r.Detail = clip(Redact(strings.TrimRight(r.Detail, "\n")), maxDetail)
	e.mu.Lock()
	if r.Time.IsZero() {
		r.Time = e.now()
	}
	r.Version, r.Platform = e.version, e.platform
	line, err := json.Marshal(r)
	if err == nil {
		line = append(line, '\n')
		if e.lines >= maxErrorRecords || (e.size > 0 && e.size+int64(len(line)) > maxErrorBytes) {
			_ = os.Rename(e.path, e.path+".1")
			e.lines, e.size = 0, 0
		}
		if f, err := os.OpenFile(e.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			n, _ := f.Write(line)
			f.Close()
			e.size += int64(n)
			e.lines++
		}
	}
	fn := e.onAdd
	e.mu.Unlock()
	if fn != nil {
		fn(r)
	}
	return r
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// cut at a rune boundary
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// Recent returns at most n records, oldest first, from errors.jsonl and
// the rotated file before it. n <= 0 returns all.
func (e *ErrorLog) Recent(n int) []model.ErrorRecord {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []model.ErrorRecord
	for _, p := range []string{e.path + ".1", e.path} {
		out = append(out, readRecords(p)...)
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// Since returns the records after t, oldest first.
func (e *ErrorLog) Since(t time.Time) []model.ErrorRecord {
	var out []model.ErrorRecord
	for _, r := range e.Recent(0) {
		if r.Time.After(t) {
			out = append(out, r)
		}
	}
	return out
}

// Count is how many records the current file holds.
func (e *ErrorLog) Count() int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lines
}

func readRecords(path string) []model.ErrorRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []model.ErrorRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r model.ErrorRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Kind != "" {
			out = append(out, r)
		}
	}
	return out
}

// ---- panics ----

// RecordPanic records a recovered panic value with the stack of the
// panicking goroutine, and keeps it as the crash the next start asks
// about (CrashFileName). where names the goroutine ("main", a loop).
func (e *ErrorLog) RecordPanic(where string, v any, stack []byte) model.ErrorRecord {
	msg := fmt.Sprintf("panic in %s: %v", where, v)
	r := e.Add(model.ErrorKindPanic, msg, string(stack))
	if e != nil {
		if data, err := json.Marshal(r); err == nil {
			p := filepath.Join(e.dir, CrashFileName)
			if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
				_, _ = f.Write(data)
				_ = f.Sync()
				f.Close()
			}
		}
	}
	return r
}

// CatchPanic is deferred at the top of a goroutine: a panic is recorded
// and then raised again, so the program exits exactly as it would have.
//
//	defer elog.CatchPanic("main")
func (e *ErrorLog) CatchPanic(where string) {
	if v := recover(); v != nil {
		e.RecordPanic(where, v, debug.Stack())
		panic(v)
	}
}

// CaptureCrashOutput makes the Go runtime write fatal errors (unrecovered
// panics, concurrent map writes, out of memory) to dir/crash.txt, after
// turning what the previous run left there into a panic record. It is
// process-wide: main calls it once.
func (e *ErrorLog) CaptureCrashOutput() {
	if e == nil {
		return
	}
	p := filepath.Join(e.dir, CrashOutputName)
	if data, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(data)) > 0 {
		// A panic CatchPanic saw is recorded already; the runtime's copy of
		// it only replaces a missing record.
		if _, err := os.Stat(filepath.Join(e.dir, CrashFileName)); err != nil {
			msg, detail := splitCrash(string(data))
			r := e.Add(model.ErrorKindPanic, msg, detail)
			if js, err := json.Marshal(r); err == nil {
				_ = os.WriteFile(filepath.Join(e.dir, CrashFileName), js, 0o600)
			}
		}
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		f.Close()
		return
	}
	f.Close() // SetCrashOutput keeps its own duplicate
}

// splitCrash takes the "panic: …" / "fatal error: …" line of a crash
// report as the message; the rest is the detail.
func splitCrash(s string) (msg, detail string) {
	s = strings.TrimSpace(s)
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "panic:") || strings.HasPrefix(l, "fatal error:") {
			return l, s
		}
	}
	first, _, _ := strings.Cut(s, "\n")
	return first, s
}

// TakeCrash returns the previous run's crash record and forgets it; ok
// is false when the previous run did not crash.
func (e *ErrorLog) TakeCrash() (r model.ErrorRecord, ok bool) {
	if e == nil {
		return r, false
	}
	p := filepath.Join(e.dir, CrashFileName)
	data, err := os.ReadFile(p)
	if err != nil {
		return r, false
	}
	_ = os.Remove(p)
	if json.Unmarshal(data, &r) != nil || r.Kind == "" {
		return r, false
	}
	return r, true
}
