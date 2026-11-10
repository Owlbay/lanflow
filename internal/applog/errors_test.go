package applog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"lanflow/internal/model"
)

func TestErrorLogRecordsRedacted(t *testing.T) {
	dir := t.TempDir()
	e := OpenErrors(dir, "1.2.3")
	r := e.Add(model.ErrorKindError, "update https://sub.example.com/api?token=abc123 failed\nsecond line", "")
	if strings.Contains(r.Message, "abc123") || strings.Contains(r.Message, "\n") {
		t.Fatalf("message not redacted / one line: %q", r.Message)
	}
	if r.Version != "1.2.3" || r.Platform == "" || r.Time.IsZero() {
		t.Fatalf("record fields: %+v", r)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ErrorsFileName))
	if strings.Contains(string(data), "abc123") {
		t.Fatal("token on disk")
	}
	var back model.ErrorRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &back); err != nil || back.Kind != model.ErrorKindError {
		t.Fatalf("jsonl: %v %+v", err, back)
	}
	// a reopened log continues the count
	if n := OpenErrors(dir, "x").Count(); n != 1 {
		t.Fatalf("count after reopen = %d", n)
	}
}

func TestErrorLogRotates(t *testing.T) {
	defer func(n int, b int64) { maxErrorRecords, maxErrorBytes = n, b }(maxErrorRecords, maxErrorBytes)
	maxErrorRecords, maxErrorBytes = 10, 1<<20
	dir := t.TempDir()
	e := OpenErrors(dir, "v")
	for i := 0; i < 25; i++ {
		e.Add(model.ErrorKindError, "error "+string(rune('a'+i)), "")
	}
	if e.Count() != 5 {
		t.Fatalf("current file holds %d records, want 5", e.Count())
	}
	if _, err := os.Stat(filepath.Join(dir, ErrorsFileName+".1")); err != nil {
		t.Fatal("no rotated file")
	}
	rs := e.Recent(0)
	if len(rs) != 15 || rs[len(rs)-1].Message != "error y" {
		t.Fatalf("recent = %d records, last %+v", len(rs), rs[len(rs)-1])
	}
	if got := e.Recent(3); len(got) != 3 || got[0].Message != "error w" {
		t.Fatalf("recent(3) = %+v", got)
	}

	// size limit
	maxErrorRecords, maxErrorBytes = 1000, 2000
	dir = t.TempDir()
	e = OpenErrors(dir, "v")
	for i := 0; i < 40; i++ {
		e.Add(model.ErrorKindError, strings.Repeat("x", 200), "")
	}
	st, _ := os.Stat(filepath.Join(dir, ErrorsFileName))
	if st.Size() > 2000 {
		t.Fatalf("file is %d bytes, limit 2000", st.Size())
	}
}

func TestLoggerForwardsErrors(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir)
	defer l.Close()
	e := OpenErrors(dir, "v")
	l.SetErrors(e)
	var seen []model.ErrorRecord
	e.OnAdd(func(r model.ErrorRecord) { seen = append(seen, r) })
	l.Infof("fine")
	l.Warnf("hmm")
	l.Errorf("broken: password=hunter2")
	l.Unrecorded(Error, "recorded elsewhere")
	if len(seen) != 1 || !strings.Contains(seen[0].Message, "broken") || strings.Contains(seen[0].Message, "hunter2") {
		t.Fatalf("records = %+v", seen)
	}
}

func TestCatchPanicRecordsAndRepanics(t *testing.T) {
	dir := t.TempDir()
	e := OpenErrors(dir, "v")
	func() {
		defer func() {
			if v := recover(); v != "boom" {
				t.Fatalf("panic not raised again: %v", v)
			}
		}()
		defer e.CatchPanic("worker")
		panic("boom")
	}()
	rs := e.Recent(0)
	if len(rs) != 1 || rs[0].Kind != model.ErrorKindPanic || !strings.Contains(rs[0].Message, "panic in worker: boom") || !strings.Contains(rs[0].Detail, "TestCatchPanicRecordsAndRepanics") {
		t.Fatalf("records = %+v", rs)
	}
	// the next start finds it once
	e2 := OpenErrors(dir, "v")
	r, ok := e2.TakeCrash()
	if !ok || r.Kind != model.ErrorKindPanic {
		t.Fatalf("TakeCrash = %+v %v", r, ok)
	}
	if _, ok := e2.TakeCrash(); ok {
		t.Fatal("crash offered twice")
	}
}

func TestCrashOutputBecomesRecord(t *testing.T) {
	dir := t.TempDir()
	out := "goroutine 1 [running]:\nfatal error: concurrent map writes\n\ngoroutine 7 [running]:\nmain.f()\n"
	if err := os.WriteFile(filepath.Join(dir, CrashOutputName), []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	msg, detail := splitCrash(out)
	if msg != "fatal error: concurrent map writes" || !strings.Contains(detail, "main.f()") {
		t.Fatalf("split = %q / %q", msg, detail)
	}
	// The runtime still writes to stderr as well; pointing the test
	// process's crash output at a temp file is harmless.
	if runtime.GOOS == "windows" {
		t.Skip("the runtime keeps crash.txt open, so the temp dir could not be removed")
	}
	e := OpenErrors(dir, "v")
	e.CaptureCrashOutput()
	if rs := e.Recent(0); len(rs) != 1 || rs[0].Kind != model.ErrorKindPanic || rs[0].Message != msg {
		t.Fatalf("records = %+v", rs)
	}
	if r, ok := e.TakeCrash(); !ok || r.Message != msg {
		t.Fatalf("TakeCrash = %+v %v", r, ok)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, CrashOutputName)); len(data) != 0 {
		t.Fatalf("crash.txt not emptied: %q", data)
	}
}
