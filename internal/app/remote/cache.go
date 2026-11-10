package remote

import (
	"encoding/json"
	"reflect"
	"sync"

	"lanflow/internal/app"
	"lanflow/internal/rpc"
)

// The window reads the API on every frame (app.API: reads are cheap);
// over the local interface each read is a round trip, so the window's
// client keeps every read's result until a "changed" event of a topic the
// read depends on, and drops everything after an action (whose effect the
// events report a moment later).

// topicReads lists the reads each topic invalidates. A read missing from
// every list is dropped on every event; staticReads are never dropped.
var topicReads = map[app.Topic][]string{
	app.TopicStatus:       {"Status", "TUNHelperInstalled", "Proxies", "NetInfo"},
	app.TopicTraffic:      {"Status", "TrafficHistory"},
	app.TopicProxies:      {"Proxies", "ProxyProviders", "IconData", "ProbeRegions", "Status"},
	app.TopicSubs:         {"Subscriptions", "EffectiveDNS", "EffectiveSniffer", "ProbeRegions", "Status", "Proxies", "OverrideList", "Overrides"},
	app.TopicConns:        {"Connections", "ClosedConnections", "Status"},
	app.TopicLogs:         {"Logs", "AppLogs"},
	app.TopicRules:        {"Rules", "CustomRules", "RuleProviders"},
	app.TopicExits:        {"ExitRoutes", "You"},
	app.TopicNet:          {"NetInfo", "Sites", "LeakCheck"},
	app.TopicSettings:     {"Settings", "Overrides", "OverrideList", "EffectiveDNS", "EffectiveSniffer", "WebDAVInfo", "ConfigSyncStatus", "FeedbackInfo", "UIState", "GeoDataInfo", "Status", "Proxies", "CacheUsage"},
	app.TopicCoreDownload: {"CoreDownload"},
	app.TopicCores:        {"InstalledCores", "Status"},
	app.TopicProbe:        {"Probe", "ProbeRegions"},
	app.TopicStats:        {"TrafficStats"},
	app.TopicProcIcons:    {"ProcessIcon"},
}

var staticReads = map[string]bool{"ConfigDir": true, "LogDir": true, "ATrustReference": true, "LookupIP": true}

// uncachedReads are reads with effects or that block.
var uncachedReads = map[string]bool{"TakeNotices": true, "TestMirrors": true}

var listedReads = func() map[string]bool {
	m := map[string]bool{}
	for _, ms := range topicReads {
		for _, x := range ms {
			m[x] = true
		}
	}
	return m
}()

// cacheable reports whether method is a read whose result may be kept.
func cacheable(method string) bool {
	if uncachedReads[method] {
		return false
	}
	m, ok := rpc.Lookup(method)
	return ok && !m.Agent && !m.Error && len(m.Results) > 0
}

func uncachedRead(method string) bool { return uncachedReads[method] }

type cacheEntry struct {
	method string
	vals   []reflect.Value
}

type readCache struct {
	mu  sync.Mutex
	gen uint64
	m   map[string]cacheEntry
}

func newReadCache() *readCache { return &readCache{m: map[string]cacheEntry{}} }

func cacheKey(method string, args []any) string {
	if len(args) == 0 {
		return method
	}
	b, _ := json.Marshal(args)
	return method + string(b)
}

// get fills outs from the cache.
func (c *readCache) get(method string, args []any, outs []any) bool {
	if !cacheable(method) {
		return false
	}
	c.mu.Lock()
	e, ok := c.m[cacheKey(method, args)]
	c.mu.Unlock()
	if !ok || len(e.vals) != len(outs) {
		return false
	}
	for i, o := range outs {
		reflect.ValueOf(o).Elem().Set(copyTop(e.vals[i]))
	}
	return true
}

// generation is read before a call; put stores only when nothing was
// invalidated meanwhile.
func (c *readCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

func (c *readCache) put(method string, args []any, outs []any) {
	c.putAt(c.generation(), method, args, outs)
}

func (c *readCache) putAt(gen uint64, method string, args []any, outs []any) {
	vals := make([]reflect.Value, len(outs))
	for i, o := range outs {
		vals[i] = copyTop(reflect.ValueOf(o).Elem())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	c.m[cacheKey(method, args)] = cacheEntry{method: method, vals: vals}
}

// invalidate drops the reads that depend on t and those depending on no
// topic in particular.
func (c *readCache) invalidate(t app.Topic) {
	drop := map[string]bool{}
	for _, m := range topicReads[t] {
		drop[m] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for k, e := range c.m {
		if drop[e.method] || (!listedReads[e.method] && !staticReads[e.method]) {
			delete(c.m, k)
		}
	}
}

// drop forgets every result of method.
func (c *readCache) drop(method string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for k, e := range c.m {
		if e.method == method {
			delete(c.m, k)
		}
	}
}

// clear forgets everything but the static reads.
func (c *readCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for k, e := range c.m {
		if !staticReads[e.method] {
			delete(c.m, k)
		}
	}
}

// reset forgets everything (a new connection: maybe a new service).
func (c *readCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	clear(c.m)
}

// copyTop copies a slice's backing array, so that a view sorting the list
// it was given in place does not change the cached one.
func copyTop(v reflect.Value) reflect.Value {
	if v.Kind() != reflect.Slice || v.IsNil() {
		return v
	}
	out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
	reflect.Copy(out, v)
	return out
}
