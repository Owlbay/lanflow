// Package applog is Lanflow's own run log (as opposed to the core's):
// lanflow.log in the log directory, rotated at 5 MB with one old file
// kept, plus the most recent entries in memory for the 日志 page.
//
// Every line is redacted before it is stored: http(s) URLs keep only their
// scheme and host (subscription links carry tokens in the path or query),
// and token=, password=, secret= style values are masked.
package applog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"lanflow/internal/model"
)

// FileName is the log file under the log directory.
const FileName = "lanflow.log"

const (
	maxSize    = 5 << 20
	maxEntries = 1000
)

// Levels, matching the core's log levels.
const (
	Debug = "debug"
	Info  = "info"
	Warn  = "warning"
	Error = "error"
)

// Logger writes the app log. The zero value and a nil *Logger are usable
// and keep entries in memory only.
type Logger struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	size    int64
	// entries is a ring of the newest maxEntries entries: head is the
	// oldest, n how many are held (allocated on the first entry).
	entries []model.LogEntry
	head, n int
	onWrite func()
	now     func() time.Time
}

// Open opens (creating) dir/lanflow.log for appending. When the file cannot
// be opened the logger still keeps entries in memory and err says why.
func Open(dir string) (*Logger, error) {
	l := &Logger{path: filepath.Join(dir, FileName), now: time.Now}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return l, err
	}
	return l, l.open()
}

func (l *Logger) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

// Path is the log file ("" for an in-memory logger).
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// OnWrite sets fn, called (outside the lock) after every entry.
func (l *Logger) OnWrite(fn func()) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.onWrite = fn
	l.mu.Unlock()
}

// Logf records one entry at level.
func (l *Logger) Logf(level, format string, args ...any) {
	if l == nil {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	l.add(level, msg)
}

func (l *Logger) Infof(format string, args ...any)  { l.Logf(Info, format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.Logf(Warn, format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.Logf(Error, format, args...) }
func (l *Logger) Debugf(format string, args ...any) { l.Logf(Debug, format, args...) }

func (l *Logger) add(level, msg string) {
	msg = Redact(strings.TrimRight(msg, "\n"))
	// keep one entry per line in the file; the UI shows it on one line too
	oneLine := strings.ReplaceAll(msg, "\n", " ⏎ ")
	l.mu.Lock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	l.pushLocked(model.LogEntry{Time: now, Level: level, Payload: oneLine})
	if l.f != nil {
		line := fmt.Sprintf("%s %-7s %s\n", now.Format("2006-01-02 15:04:05.000"), strings.ToUpper(level), oneLine)
		if l.size > 0 && l.size+int64(len(line)) > maxSize {
			l.rotateLocked()
		}
		if l.f != nil {
			n, _ := l.f.WriteString(line)
			l.size += int64(n)
		}
	}
	fn := l.onWrite
	l.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// pushLocked appends e to the ring, dropping the oldest entry when full.
func (l *Logger) pushLocked(e model.LogEntry) {
	if l.entries == nil {
		l.entries = make([]model.LogEntry, maxEntries)
	}
	if l.n < maxEntries {
		l.entries[(l.head+l.n)%maxEntries] = e
		l.n++
		return
	}
	l.entries[l.head] = e
	l.head = (l.head + 1) % maxEntries
}

func (l *Logger) rotateLocked() {
	l.f.Close()
	l.f = nil
	_ = os.Rename(l.path, l.path+".1")
	_ = l.open()
}

// Entries returns the entries in memory, oldest first.
func (l *Logger) Entries() []model.LogEntry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n == 0 {
		return nil
	}
	out := make([]model.LogEntry, l.n)
	for i := range out {
		out[i] = l.entries[(l.head+i)%maxEntries]
	}
	return out
}

// Clear forgets the entries in memory (the file is kept).
func (l *Logger) Clear() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.entries, l.head, l.n = nil, 0, 0
	l.mu.Unlock()
}

// Close closes the file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Writer adapts the logger for the standard log package: each write is
// one info entry ("lanflow: " prefixes and timestamps are dropped).
func (l *Logger) Writer() io.Writer { return stdWriter{l} }

type stdWriter struct{ l *Logger }

var stdPrefix = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)? )?(lanflow: )?`)

func (w stdWriter) Write(p []byte) (int, error) {
	msg := stdPrefix.ReplaceAllString(strings.TrimRight(string(p), "\n"), "")
	level := Info
	if lower := strings.ToLower(msg); strings.Contains(lower, "error") || strings.Contains(lower, "fail") || strings.Contains(msg, "失败") {
		level = Warn
	}
	w.l.add(level, msg)
	return len(p), nil
}

var (
	urlRe    = regexp.MustCompile(`(?i)\b((?:https?|wss?)://)([^/\s"'<>?#]+)([^\s"'<>]*)`)
	secretRe = regexp.MustCompile(`(?i)\b(token|access_token|key|secret|password|passwd|pass|auth|uuid|psk|sid)(\s*[=:]\s*)("?)([^\s&"',;]+)`)
)

// Redact masks what must not end up in a log or a bug report.
func Redact(s string) string {
	s = urlRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := urlRe.FindStringSubmatch(m)
		host := sub[2]
		if i := strings.LastIndex(host, "@"); i >= 0 { // user:pass@host
			host = "***@" + host[i+1:]
		}
		if sub[3] == "" || sub[3] == "/" {
			return sub[1] + host + sub[3]
		}
		return sub[1] + host + "/***"
	})
	return secretRe.ReplaceAllString(s, "$1$2$3***")
}
