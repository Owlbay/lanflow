package app

import (
	"os"
	"slices"
	"testing"

	"lanflow/internal/platform/sysdns"
)

func tunHarness(t *testing.T, dns []string) *harness {
	h := newHarness(t, func(h *harness) { h.helper.installed = true; h.dns.servers = dns })
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestTUNSetsAndRestoresSystemDNS(t *testing.T) {
	h := tunHarness(t, []string{"192.168.1.1"})
	if h.dns.sets != 0 {
		t.Fatal("DNS must not change without TUN")
	}
	if err := h.s.SetTUN(true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.dns.get(), sysdns.TUNServers) {
		t.Fatalf("DNS while TUN = %v", h.dns.get())
	}
	if _, err := os.Stat(h.s.dnsMarkerPath()); err != nil {
		t.Fatal("marker missing while DNS is ours")
	}
	if err := h.s.SetTUN(false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.dns.get(), []string{"192.168.1.1"}) {
		t.Fatalf("DNS after TUN off = %v", h.dns.get())
	}
	if _, err := os.Stat(h.s.dnsMarkerPath()); !os.IsNotExist(err) {
		t.Fatal("marker left behind")
	}
}

func TestStopAndShutdownRestoreDNS(t *testing.T) {
	h := tunHarness(t, nil)
	h.s.SetTUN(true)
	if err := h.s.StopCore(); err != nil {
		t.Fatal(err)
	}
	if h.dns.get() != nil {
		t.Fatalf("DNS after stop = %v, want automatic", h.dns.get())
	}
	h.s.StartCore()
	if !slices.Equal(h.dns.get(), sysdns.TUNServers) {
		t.Fatalf("DNS after restart with TUN saved on = %v", h.dns.get())
	}
	h.s.Shutdown()
	if h.dns.get() != nil {
		t.Fatalf("DNS after shutdown = %v", h.dns.get())
	}
}

func TestTUNSetDNSOffLeavesDNS(t *testing.T) {
	h := tunHarness(t, []string{"192.168.1.1"})
	set := h.s.Settings()
	set.TUNSetDNS = false
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.s.SetTUN(true)
	if h.dns.sets != 0 {
		t.Fatal("DNS changed although the setting is off")
	}
	// Turning it on while TUN runs applies at once, off restores.
	set = h.s.Settings()
	set.TUNSetDNS = true
	h.s.SaveSettings(set)
	if !slices.Equal(h.dns.get(), sysdns.TUNServers) {
		t.Fatalf("DNS after enabling = %v", h.dns.get())
	}
	set.TUNSetDNS = false
	h.s.SaveSettings(set)
	if !slices.Equal(h.dns.get(), []string{"192.168.1.1"}) {
		t.Fatalf("DNS after disabling = %v", h.dns.get())
	}
}

func TestUserChangedDNSIsLeftAlone(t *testing.T) {
	h := tunHarness(t, []string{"192.168.1.1"})
	h.s.SetTUN(true)
	h.dns.Set([]string{"9.9.9.9"}) // the user edits DNS while TUN runs
	h.s.SetTUN(false)
	if !slices.Equal(h.dns.get(), []string{"9.9.9.9"}) {
		t.Fatalf("user's DNS overwritten: %v", h.dns.get())
	}
}

func TestLeftoverDNSRestoredAtLaunch(t *testing.T) {
	h := tunHarness(t, []string{"10.0.0.1"})
	h.s.SetTUN(true)
	// Simulate a crash: the marker stays, a new service starts on the same dir.
	marker, _ := os.ReadFile(h.s.dnsMarkerPath())
	h.s.Shutdown()
	h.dns.Set(sysdns.TUNServers)
	os.WriteFile(h.s.dnsMarkerPath(), marker, 0o600)
	s2, err := newServiceWith(h.s.opt, h.s.d)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Shutdown()
	if !slices.Equal(h.dns.get(), []string{"10.0.0.1"}) {
		t.Fatalf("leftover DNS not restored at launch: %v", h.dns.get())
	}
}
