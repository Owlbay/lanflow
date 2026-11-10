package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lanflow/internal/applog"
	"lanflow/internal/model"
)

func TestCoreExitRecorded(t *testing.T) {
	h := newHarness(t, nil)
	h.s.goBackground(h.s.watchCrashes)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.mgr.crash(errors.New("Mihomo 内核意外退出（exit status 2），最近日志：\nlevel=fatal msg=\"bad config\"\nline two"))
	var rec model.ErrorRecord
	if !waitFor(func() bool {
		for _, r := range h.s.elog.Recent(0) {
			if r.Kind == model.ErrorKindCoreExit {
				rec = r
				return true
			}
		}
		return false
	}) {
		t.Fatalf("no core-exit record: %+v", h.s.elog.Recent(0))
	}
	if rec.ExitCode == nil || *rec.ExitCode != 2 || !strings.Contains(rec.Message, "exit status 2") || !strings.Contains(rec.Detail, "bad config") {
		t.Fatalf("record = %+v", rec)
	}
	if strings.Contains(rec.Message, "最近日志") {
		t.Fatalf("log tail left in the message: %q", rec.Message)
	}
}

func TestErrorLogLinesRecorded(t *testing.T) {
	h := newHarness(t, nil)
	h.s.notify(Notice{Kind: "error", Title: "订阅更新失败", Detail: "https://a.example/s?token=abc 超时"})
	rs := h.s.elog.Recent(0)
	if len(rs) != 1 || rs[0].Kind != model.ErrorKindError || strings.Contains(rs[0].Message, "token=abc") {
		t.Fatalf("records = %+v", rs)
	}
	if _, err := os.Stat(filepath.Join(h.dir, applog.ErrorsFileName)); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundPanicRecorded(t *testing.T) {
	h := newHarness(t, nil)
	func() {
		defer func() { _ = recover() }()
		defer h.s.elog.CatchPanic("background")
		panic("loop broke")
	}()
	if rs := h.s.elog.Recent(0); len(rs) != 1 || rs[0].Kind != model.ErrorKindPanic {
		t.Fatalf("records = %+v", rs)
	}
	// the next start offers to report it
	s2, err := newServiceWith(h.s.opt, h.s.d)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Shutdown()
	var found bool
	for _, n := range s2.TakeNotices() {
		if n.Kind == "crash-report" && strings.Contains(n.Detail, "loop broke") {
			found = true
		}
	}
	if !found {
		t.Fatal("no crash-report notice at the next start")
	}
}
