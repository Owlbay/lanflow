package app

import (
	"errors"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"

	"lanflow/internal/applog"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Local error collection (docs/feedback-api.md): errors.jsonl in the log
// directory records Error level lines of the run log (applog.Logger
// forwards them), unexpected exits of the core (handleCrash) and panics
// (goBackground, CatchPanic). The previous run's crash is offered for a
// report at the next start with a "crash-report" notice.

func (s *Service) version() string {
	if s.opt.Version != "" {
		return s.opt.Version
	}
	return "dev"
}

// openErrorLog opens errors.jsonl and queues the crash-report notice when
// the previous run crashed. Called by NewService once state exists.
func (s *Service) openErrorLog() {
	s.elog = applog.OpenErrors(s.opt.LogDir, s.version())
	s.alog.SetErrors(s.elog)
	s.elog.OnAdd(s.errorRecorded)
	if s.opt.CaptureCrashes {
		s.elog.CaptureCrashOutput()
	}
	if r, ok := s.elog.TakeCrash(); ok {
		s.fb.mu.Lock()
		s.fb.lastCrash = &r
		s.fb.mu.Unlock()
		s.alog.Unrecorded(applog.Warn, "the previous run crashed: %s", r.Message)
		s.state.notices = append(s.state.notices, Notice{
			Kind:   "crash-report",
			Title:  i18n.T("上次异常退出，是否反馈？"),
			Detail: r.Message,
		})
	}
}

// CatchPanic is deferred by main at the top of the main goroutine: a
// panic is recorded in errors.jsonl (and offered for a report at the next
// start), then raised again so the app exits as it would have.
func (s *Service) CatchPanic(where string) {
	if v := recover(); v != nil {
		s.elog.RecordPanic(where, v, debug.Stack())
		panic(v)
	}
}

var exitStatusRe = regexp.MustCompile(`exit status (-?\d+)`)

// recordCoreExit records an unexpected exit of the core: the reason with
// the exit code, and the core's last log lines (core.exitError puts them
// after "最近日志：").
func (s *Service) recordCoreExit(cause error) {
	text := i18n.T("内核意外退出")
	if cause != nil {
		text = cause.Error()
	}
	var code *int
	var ee *exec.ExitError
	if errors.As(cause, &ee) {
		c := ee.ExitCode()
		code = &c
	} else if m := exitStatusRe.FindStringSubmatch(text); m != nil {
		if c, err := strconv.Atoi(m[1]); err == nil {
			code = &c
		}
	} else if strings.Contains(text, "退出码 0") {
		c := 0
		code = &c
	}
	msg, tail := text, ""
	if i := strings.Index(text, "最近日志："); i >= 0 {
		msg, tail = strings.TrimRight(text[:i], "，, \n"), strings.TrimLeft(text[i+len("最近日志："):], "\n")
	} else if first, rest, ok := strings.Cut(text, "\n"); ok {
		msg, tail = first, rest
	}
	s.elog.AddExit(msg, code, tail)
}

// errorRecorded is called after every new error record, possibly by code
// holding s.mu (an Error line logged under the lock): the automatic report
// reads the settings on its own goroutine.
func (s *Service) errorRecorded(model.ErrorRecord) {
	s.goBackground(s.kickAutoReport)
}
