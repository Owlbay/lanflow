package rpc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"lanflow/internal/applog"
)

// AuditFile is the audit log under Lanflow's log directory.
const AuditFile = "audit.log"

const (
	auditMaxSize = 2 << 20 // rotated to audit.log.1 beyond this
	auditKeep    = 200     // entries kept in memory for AuditLog
)

// AuditEntry is one line of audit.log: an agent's write (or refused
// call), parameters redacted.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	Client string    `json:"client"`
	Method string    `json:"method"`
	Params string    `json:"params,omitempty"`
	Result string    `json:"result"` // "ok", or the error
}

type auditLog struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	size    int64
	entries []AuditEntry
}

func openAudit(dir string) *auditLog {
	a := &auditLog{}
	if dir == "" {
		return a
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return a
	}
	a.path = filepath.Join(dir, AuditFile)
	a.open()
	return a
}

func (a *auditLog) open() {
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	if st, err := f.Stat(); err == nil {
		a.size = st.Size()
	}
	a.f = f
}

// add records a call; args are the call's parameters (redacted here).
func (a *auditLog) add(id Identity, method string, args []reflect.Value, err error) {
	a.addSummary(id.String(), method, summarize(method, args), err)
}

func (a *auditLog) addSummary(client, method, params string, err error) {
	e := AuditEntry{Time: time.Now(), Client: client, Method: method, Params: params, Result: "ok"}
	if err != nil {
		e.Result = applog.Redact(err.Error())
	}
	b, _ := json.Marshal(e)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	if len(a.entries) > auditKeep {
		a.entries = append([]AuditEntry(nil), a.entries[len(a.entries)-auditKeep:]...)
	}
	if a.f == nil {
		return
	}
	if a.size+int64(len(b))+1 > auditMaxSize {
		_ = a.f.Close()
		_ = os.Rename(a.path, a.path+".1")
		a.f, a.size = nil, 0
		a.open()
		if a.f == nil {
			return
		}
	}
	n, _ := a.f.Write(append(b, '\n'))
	a.size += int64(n)
}

// recent returns the newest n entries, oldest first.
func (a *auditLog) recent(n int) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n <= 0 || n > len(a.entries) {
		n = len(a.entries)
	}
	return append([]AuditEntry(nil), a.entries[len(a.entries)-n:]...)
}

func (a *auditLog) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f != nil {
		_ = a.f.Close()
		a.f = nil
	}
}
