package app

import (
	"sync"
	"time"
)

// feat2State is the in-memory state of the Wi-Fi rules and the WebDAV
// backup, kept beside the Service (kept out of the Service struct,
// so the state lives in a side table keyed by the service and is
// allocated on first use). Its mutex is independent of Service.mu and is
// never held across I/O.
type feat2State struct {
	mu sync.Mutex

	// Wi-Fi rules (wifi.go)
	lastSSID string // the SSID the rules last ran for
	wifiInit bool

	// WebDAV backup (backup.go)
	password    string
	pwLoaded    bool
	pwLoading   bool   // a background load (WebDAVInfo) is running
	storeKind   string // model.PasswordStoreKeychain / PasswordStoreFile, "" until known
	busy        bool
	lastError   string
	lastAttempt time.Time // last automatic backup attempt, successful or not

	opMu sync.Mutex // one backup / restore / listing at a time
}

var (
	feat2Mu     sync.Mutex
	feat2States = map[*Service]*feat2State{}
)

// feat2 returns the state of s, creating it on first use.
func feat2(s *Service) *feat2State {
	feat2Mu.Lock()
	defer feat2Mu.Unlock()
	st := feat2States[s]
	if st == nil {
		st = &feat2State{}
		feat2States[s] = st
	}
	return st
}

// feat2Tick is called by the system watcher every few seconds: it runs
// the Wi-Fi rules when the SSID changed and starts an automatic backup
// when one is due.
func (s *Service) feat2Tick() {
	s.wifiCheck()
	s.webdavTick()
}
