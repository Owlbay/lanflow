package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"lanflow/internal/model"
	"lanflow/internal/platform/sysdns"
)

// leftOver prepares what a service killed with TUN, the system proxy and
// the system DNS on leaves behind: the core still running in the helper,
// the proxy marker with the proxy at our port, the DNS marker.
func leftOver(tun bool) func(h *harness) {
	return func(h *harness) {
		h.helper.installed = true
		h.stray.found, h.stray.tun = true, tun
		h.sys.state.Enabled, h.sys.state.Host, h.sys.state.Port = true, "127.0.0.1", 7890
		h.dns.servers = sysdns.TUNServers // the system DNS points at the TUN
		_ = os.MkdirAll(h.dir, 0o700)
		_ = os.WriteFile(filepath.Join(h.dir, markerFile), []byte(`{"port":7890}`), 0o600)
		data, _ := json.Marshal(dnsMarker{Servers: sysdns.TUNServers, Saved: sysdns.Saved(`{"Wi-Fi":["192.168.1.1"]}`)})
		_ = os.WriteFile(filepath.Join(h.dir, dnsMarkerFile), data, 0o600)
		_ = os.WriteFile(filepath.Join(h.dir, settingsFile), []byte(`{"TUN":true,"TUNSetDNS":true,"MixedPort":7890}`), 0o600)
	}
}

// The service was killed while its core ran in the helper: the new
// service takes the core over; TUN, the system proxy and the DNS stay,
// nothing is asked.
func TestTakeOverHelperCore(t *testing.T) {
	h := newHarness(t, leftOver(true))
	if h.s.Status().PendingRecovery || h.dns.restores != 0 {
		t.Fatalf("treated as a crash at start: pending=%v dns restores=%d", h.s.Status().PendingRecovery, h.dns.restores)
	}
	h.s.bootstrap()
	st := h.s.Status()
	if h.mgr.adopts != 1 || h.mgr.starts != 0 || h.stray.stops != 0 {
		t.Fatalf("adopts=%d starts=%d stray stops=%d", h.mgr.adopts, h.mgr.starts, h.stray.stops)
	}
	if st.Core != model.CoreRunning || !st.TUN || !st.SystemProxy || st.PendingRecovery {
		t.Fatalf("status after the takeover: %+v", st)
	}
	if r, ok := h.mgr.runner.(namedRunner); !ok || r.name != "helper" {
		t.Fatalf("adopted through %v", h.mgr.runner)
	}
	if _, err := os.Stat(filepath.Join(h.dir, dnsMarkerFile)); err != nil {
		t.Fatal("DNS marker dropped")
	}
	if _, clears := h.sys.counts(); clears != 0 || h.dns.restores != 0 || !h.s.markerExists() {
		t.Fatalf("proxy or DNS touched: clears=%d dns restores=%d", clears, h.dns.restores)
	}
	for _, n := range h.s.TakeNotices() {
		if n.Kind == "recovery" {
			t.Fatal("recovery asked after a takeover")
		}
	}
}

// When the core cannot be taken over it is stopped and started again as
// the previous run had it, still without asking.
func TestTakeOverFailsRestarts(t *testing.T) {
	h := newHarness(t, leftOver(true))
	h.mgr.adoptErr = errors.New("no answer")
	h.s.bootstrap()
	st := h.s.Status()
	if h.stray.stops != 1 || h.mgr.starts != 1 {
		t.Fatalf("stray stops=%d starts=%d", h.stray.stops, h.mgr.starts)
	}
	if st.Core != model.CoreRunning || !st.TUN || !st.SystemProxy || st.PendingRecovery {
		t.Fatalf("status after the restart: %+v", st)
	}
	for _, n := range h.s.TakeNotices() {
		if n.Kind == "recovery" {
			t.Fatal("recovery asked after a restart")
		}
	}
}

// An orphaned child core (no TUN) is taken over through the exec runner.
func TestTakeOverOrphanCore(t *testing.T) {
	h := newHarness(t, leftOver(false))
	h.s.bootstrap()
	if r, ok := h.mgr.runner.(namedRunner); !ok || r.name != "exec" || h.mgr.adopts != 1 {
		t.Fatalf("adopted through %v (%d)", h.mgr.runner, h.mgr.adopts)
	}
	if st := h.s.Status(); st.Core != model.CoreRunning || st.TUN || !st.SystemProxy {
		t.Fatalf("status: %+v", st)
	}
}

// Nothing left running: a crash as before (recovery asked, DNS restored).
func TestNoStrayIsACrash(t *testing.T) {
	h := newHarness(t, func(hh *harness) {
		leftOver(true)(hh)
		hh.stray.found = false
	})
	if !h.s.Status().PendingRecovery || h.dns.restores != 1 {
		t.Fatalf("pending=%v dns restores=%d", h.s.Status().PendingRecovery, h.dns.restores)
	}
}

// Lanflow's own core in the helper is not "another client's TUN", and an
// orphan holding the mixed port is stopped before a start.
func TestStrayIsNotAnotherClient(t *testing.T) {
	h := newHarness(t, nil)
	h.s.d.otherTUN = func(ctx context.Context) (string, bool) { return "utun1700", true }
	h.stray.found, h.stray.tun = true, true
	if err := h.s.checkOtherTUN(); err != nil {
		t.Fatalf("own TUN refused: %v", err)
	}
	h.stray.found = false
	if err := h.s.checkOtherTUN(); err == nil {
		t.Fatal("another client's TUN not seen")
	}

	h.stray.found, h.stray.tun = true, false
	h.taken[7890] = true
	h.s.d.portInUse = func(p int, _ bool) bool { return h.stray.found && h.taken[p] }
	if err := h.s.StartCore(); err != nil {
		t.Fatalf("start with our orphan on the port: %v", err)
	}
	if h.stray.stops != 1 {
		t.Fatalf("stray stops = %d", h.stray.stops)
	}
}
