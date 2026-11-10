package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/model"
)

func TestParseRateLimit(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	reset := now.Add(37 * time.Minute)
	for _, c := range []struct {
		name    string
		status  int
		h       http.Header
		limited bool
		reset   time.Time
	}{
		{"primary limit", 403, hdr("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", fmt.Sprint(reset.Unix())), true, reset},
		{"403 for another reason", 403, hdr("X-RateLimit-Remaining", "12"), false, time.Time{}},
		{"secondary limit, seconds", 403, hdr("Retry-After", "90"), true, now.Add(90 * time.Second)},
		{"429, HTTP date", 429, hdr("Retry-After", now.Add(5*time.Minute).Format(http.TimeFormat)), true, now.Add(5 * time.Minute)},
		{"429 without a time", 429, hdr(), true, time.Time{}},
		{"the later of both", 403, hdr("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", fmt.Sprint(now.Add(time.Minute).Unix()), "Retry-After", "600"), true, now.Add(10 * time.Minute)},
		{"200", 200, hdr("X-RateLimit-Remaining", "0"), false, time.Time{}},
	} {
		got, limited := parseRateLimit(c.status, c.h, now)
		if limited != c.limited || !got.Equal(c.reset) {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, limited, c.reset, c.limited)
		}
	}
}

// ghServer is a GitHub API stand-in whose answer the test changes.
type ghServer struct {
	mu     sync.Mutex
	status int       // answer: 200, 403 (rate limit) or 500
	reset  time.Time // X-RateLimit-Reset with 403
	etag   string    // ETag of the list; If-None-Match with it gets 304
	inm    []string  // If-None-Match of every request
	hits   atomic.Int32
}

func newGHServer(t *testing.T) *ghServer {
	g := &ghServer{status: http.StatusOK, etag: `W/"rel-1"`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.hits.Add(1)
		g.mu.Lock()
		status, reset, etag := g.status, g.reset, g.etag
		g.inm = append(g.inm, r.Header.Get("If-None-Match"))
		g.mu.Unlock()
		switch status {
		case http.StatusForbidden:
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
			w.WriteHeader(status)
			return
		case http.StatusOK:
		default:
			w.WriteHeader(status)
			return
		}
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(sampleReleases())
	}))
	t.Cleanup(srv.Close)
	prev := coreAPIBase
	coreAPIBase = srv.URL
	t.Cleanup(func() { coreAPIBase = prev })
	return g
}

func (g *ghServer) set(fn func(g *ghServer)) { g.mu.Lock(); fn(g); g.mu.Unlock() }

// A refresh of a cached list asks If-None-Match; GitHub's 304 keeps the
// list and refreshes the cache time without a body.
func TestCoreReleasesConditionalRequest(t *testing.T) {
	g := newGHServer(t)
	h := newHarness(t, nil)
	list, err := h.s.CoreReleases(model.CoreChannelStable, true)
	if err != nil || len(list) != 2 {
		t.Fatalf("first fetch: %v %+v", err, list)
	}
	rc, _ := h.s.cachedReleases(model.CoreChannelStable)
	if rc.ETag != `W/"rel-1"` {
		t.Fatalf("ETag not cached: %q", rc.ETag)
	}
	first := rc.At
	time.Sleep(5 * time.Millisecond)
	list, err = h.s.CoreReleases(model.CoreChannelStable, true)
	if err != nil || len(list) != 2 || list[0].Version != "v1.19.34" {
		t.Fatalf("304 refresh: %v %+v", err, list)
	}
	g.mu.Lock()
	inm := append([]string(nil), g.inm...)
	g.mu.Unlock()
	if len(inm) != 2 || inm[0] != "" || inm[1] != `W/"rel-1"` {
		t.Fatalf("If-None-Match sent: %q", inm)
	}
	if rc, _ := h.s.cachedReleases(model.CoreChannelStable); !rc.At.After(first) || rc.ETag != `W/"rel-1"` {
		t.Fatalf("cache after 304: %+v (first at %v)", rc, first)
	}
}

// After a rate-limit answer nothing is sent until GitHub's reset, and the
// minutes left are counted again for every message.
func TestRateLimitHoldsRequestsUntilReset(t *testing.T) {
	g := newGHServer(t)
	g.set(func(g *ghServer) { g.status, g.reset = http.StatusForbidden, time.Now().Add(37*time.Minute) })
	h := newHarness(t, nil)
	now := time.Now()
	h.s.d.now = func() time.Time { return now }
	_, err := h.s.CoreReleases(model.CoreChannelStable, true)
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("expected the rate limit: %v", err)
	}
	if _, err := h.s.CoreReleases(model.CoreChannelStable, true); err == nil || g.hits.Load() != 1 {
		t.Fatalf("a second request went out while rate-limited: hits=%d err=%v", g.hits.Load(), err)
	}
	// once the reset has passed, GitHub is asked again
	g.set(func(g *ghServer) { g.status = http.StatusOK })
	now = now.Add(38 * time.Minute)
	if _, err := h.s.CoreReleases(model.CoreChannelStable, true); err != nil || g.hits.Load() != 2 {
		t.Fatalf("after the reset: hits=%d err=%v", g.hits.Load(), err)
	}
	// the minutes are counted from the reset each time the message is made
	e := rateLimitError{reset: time.Now().Add(20 * time.Minute)}
	if !strings.Contains(e.Error(), "约 20 分钟") {
		t.Fatalf("message: %s", e.Error())
	}
}

func coreCheckLogs(h *harness) (warn, debug int) {
	for _, l := range h.s.AppLogs() {
		if !strings.HasPrefix(l.Payload, "core update check") {
			continue
		}
		switch l.Level {
		case "warning":
			warn++
		case "debug":
			debug++
		}
	}
	return
}

func (h *harness) coreSchedule() (failures int, retry, last time.Time) {
	h.s.cores.mu.Lock()
	defer h.s.cores.mu.Unlock()
	return h.s.cores.failures, h.s.cores.retryAt, h.s.cores.lastCheck
}

// The automatic check after a rate limit is scheduled for GitHub's reset,
// not the next hourly tick; repeated failures warn once and back off.
func TestCoreUpdateCheckSchedulesAfterFailures(t *testing.T) {
	g := newGHServer(t)
	h := newHarness(t, nil)
	now := time.Now()
	h.s.d.now = func() time.Time { return now }
	set := h.s.Settings()
	set.CoreAutoCheck, set.DisableUpdateCheck = true, false
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}

	// 1. rate-limited: the retry is a minute after the reset
	reset := now.Add(25 * time.Minute).Truncate(time.Second)
	g.set(func(g *ghServer) { g.status, g.reset = http.StatusForbidden, reset })
	h.s.runCoreUpdateCheck()
	n, retry, _ := h.coreSchedule()
	if n != 1 || !retry.Equal(reset.Add(time.Minute)) {
		t.Fatalf("after a rate limit: failures=%d retry=%v want %v", n, retry, reset.Add(time.Minute))
	}
	if w := h.s.nextCoreCheckWait(); w > 27*time.Minute || w < 25*time.Minute {
		t.Fatalf("loop wait = %v, want about 26 min", w)
	}
	// the hourly tick before the retry time does nothing
	now = now.Add(10 * time.Minute)
	h.s.runCoreUpdateCheck()
	if g.hits.Load() != 1 {
		t.Fatalf("checked before the retry time: hits=%d", g.hits.Load())
	}

	// 2. at the retry time GitHub fails otherwise: backoff 2 h, logged at debug
	g.set(func(g *ghServer) { g.status = http.StatusInternalServerError })
	now = retry
	h.s.runCoreUpdateCheck()
	n, retry, _ = h.coreSchedule()
	if n != 2 || !retry.Equal(now.Add(2*coreUpdateTick)) {
		t.Fatalf("second failure: failures=%d retry=%v", n, retry)
	}
	if w := h.s.nextCoreCheckWait(); w != coreUpdateTick {
		t.Fatalf("loop wait capped at the tick: %v", w)
	}
	now = retry
	h.s.runCoreUpdateCheck()
	if n, retry2, _ := h.coreSchedule(); n != 3 || !retry2.Equal(now.Add(4*coreUpdateTick)) {
		t.Fatalf("third failure: failures=%d retry=%v", n, retry2)
	}
	if w, d := coreCheckLogs(h); w != 1 || d != 2 {
		t.Fatalf("logs: %d warnings, %d debug; want 1 and 2", w, d)
	}

	// 3. success clears the schedule; the next check is a day later
	g.set(func(g *ghServer) { g.status = http.StatusOK })
	_, retry, _ = h.coreSchedule()
	now = retry
	h.s.runCoreUpdateCheck()
	n, retry, last := h.coreSchedule()
	if n != 0 || !retry.IsZero() || !last.Equal(now) {
		t.Fatalf("after success: failures=%d retry=%v last=%v", n, retry, last)
	}
	hits := g.hits.Load()
	now = now.Add(2 * time.Hour)
	h.s.runCoreUpdateCheck()
	if g.hits.Load() != hits {
		t.Fatal("checked again within a day of a successful check")
	}
}

// A check that only got the cached list (GitHub unreachable) is a failure
// for the schedule, though the list still serves the settings page.
func TestCoreUpdateCheckCachedListIsNoCheck(t *testing.T) {
	g := newGHServer(t)
	h := newHarness(t, nil)
	if _, err := h.s.CoreReleases(model.CoreChannelStable, true); err != nil {
		t.Fatal(err)
	}
	h.storeStale(t, model.CoreChannelStable, mustCached(t, h))
	g.set(func(g *ghServer) { g.status = http.StatusBadGateway })
	set := h.s.Settings()
	set.CoreAutoCheck = true
	_ = h.s.SaveSettings(set)
	h.s.runCoreUpdateCheck()
	if n, retry, last := h.coreSchedule(); n != 1 || retry.IsZero() || !last.IsZero() {
		t.Fatalf("stale list counted as a check: failures=%d retry=%v last=%v", n, retry, last)
	}
}

func mustCached(t *testing.T, h *harness) []model.CoreRelease {
	t.Helper()
	rc, ok := h.s.cachedReleases(model.CoreChannelStable)
	if !ok {
		t.Fatal("no cached list")
	}
	return rc.List
}
