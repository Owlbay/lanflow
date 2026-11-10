package app

import (
	"io"
	"strconv"
	"strings"

	"lanflow/internal/applog"
	"lanflow/internal/model"
)

// Lanflow's own run log (internal/applog): every notice the UI shows and
// the outcome of every action that changes the core, the system proxy,
// TUN, the system DNS or the subscriptions, so a problem can be traced
// after the toast is gone. Lines are written in English; error texts are
// kept as the user saw them.

// AppLogs implements API.AppLogs.
func (s *Service) AppLogs() []model.LogEntry { return s.alog.Entries() }

// ClearAppLogs implements API.ClearAppLogs (the file is kept).
func (s *Service) ClearAppLogs() {
	s.alog.Clear()
	s.em.emit(TopicLogs)
}

// AppLogWriter is where main sends the standard log package's output.
func (s *Service) AppLogWriter() io.Writer { return s.alog.Writer() }

// logOp records the outcome of an action.
func (s *Service) logOp(what string, err error) {
	if err != nil {
		s.alog.Errorf("%s: failed: %v", what, err)
		return
	}
	s.alog.Infof("%s: ok", what)
}

// logNotice records a notice as the user sees it.
func (s *Service) logNotice(n Notice) {
	msg := strings.TrimSpace(n.Title)
	if n.Detail != "" {
		if msg != "" {
			msg += " — "
		}
		msg += n.Detail
	}
	switch n.Kind {
	case "core-crash": // recorded in errors.jsonl by handleCrash, with the exit code
		s.alog.Unrecorded(applog.Error, "notice [%s] %s", n.Kind, msg)
	case "error", "invalid-config":
		s.alog.Errorf("notice [%s] %s", n.Kind, msg)
	case "recovery":
		s.alog.Warnf("notice [%s] %s", n.Kind, msg)
	default:
		s.alog.Infof("notice [%s] %s", n.Kind, msg)
	}
}

// subLabel names a subscription for the log.
func (s *Service) subLabel(id string) string {
	if sub, ok := s.d.store.Get(id); ok {
		return strconv.Quote(sub.Name) + " (" + id + ")"
	}
	return id
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
