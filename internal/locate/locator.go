package locate

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"lanflow/internal/geo"
	"lanflow/internal/model"
	"lanflow/internal/netx"
)

// cacheTTL is how long a probe result stays valid.
const cacheTTL = 24 * time.Hour

// probeTimeout bounds one trace request through a node.
const probeTimeout = 8 * time.Second

// cacheEntry is a probe result as stored in the JSON cache: the ExitInfo
// plus the probe's wall-clock duration, used for the latency sanity check.
type cacheEntry struct {
	model.ExitInfo
	ProbeRTT time.Duration `json:",omitempty"`
}

type locator struct {
	cachePath string
	mmdbPath  string
	asnPath   string
	traceURL  string

	now func() time.Time

	// Injection points for tests.
	proxyClient  func(port int, timeout time.Duration) *http.Client
	directClient func(timeout time.Duration) *http.Client
	resolve      func(ip string) model.EndpointInfo

	mu     sync.Mutex // guards cache
	loaded bool
	cache  map[string]cacheEntry

	probeMu sync.Mutex // one probe at a time

	dbOnce sync.Once
	dbs    databases
}

func newLocator(cachePath, mmdbPath, asnPath string) *locator {
	l := &locator{
		cachePath:    cachePath,
		mmdbPath:     mmdbPath,
		asnPath:      asnPath,
		traceURL:     TraceURL,
		now:          time.Now,
		proxyClient:  netx.ViaPort,
		directClient: netx.Direct,
	}
	l.resolve = l.lookup
	return l
}

// guess answers offline: a valid cached probe, else the node-name guess,
// else unknown.
func (l *locator) guess(node string) model.ExitInfo {
	if e, ok := l.cached(node); ok {
		return e.ExitInfo
	}
	if p, ok := geo.FromName(node); ok {
		return model.ExitInfo{Node: node, Place: &p, Source: model.LocateName}
	}
	return model.ExitInfo{Node: node, Source: model.LocateUnknown}
}

// cached returns the cache entry for node if it is still valid.
func (l *locator) cached(node string) (cacheEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loadLocked()
	e, ok := l.cache[node]
	if !ok || !l.valid(e) {
		return cacheEntry{}, false
	}
	return e, true
}

func (l *locator) valid(e cacheEntry) bool {
	if e.MeasuredAt.IsZero() {
		return false
	}
	age := l.now().Sub(e.MeasuredAt)
	return age >= 0 && age < cacheTTL
}

// loadLocked reads the cache file once; a missing or corrupt file yields
// an empty cache. Caller holds l.mu.
func (l *locator) loadLocked() {
	if l.loaded {
		return
	}
	l.loaded = true
	l.cache = map[string]cacheEntry{}
	if l.cachePath == "" {
		return
	}
	data, err := os.ReadFile(l.cachePath)
	if err != nil {
		return
	}
	var m map[string]cacheEntry
	if json.Unmarshal(data, &m) == nil && m != nil {
		l.cache = m
	}
}

// store saves a probe result and rewrites the cache file atomically,
// dropping expired entries on the way.
func (l *locator) store(e cacheEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loadLocked()
	for k, v := range l.cache {
		if !l.valid(v) {
			delete(l.cache, k)
		}
	}
	l.cache[e.Node] = e
	if l.cachePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(l.cache, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(l.cachePath, data)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
