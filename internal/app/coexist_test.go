package app

import (
	"slices"
	"strings"
	"testing"

	"lanflow/internal/model"
	"lanflow/internal/platform/atrust"
	"lanflow/internal/platform/sysdns"
	"lanflow/internal/profile"
)

var sampleATrust = atrust.Info{
	Processes: []string{"aTrustAgent"},
	Ifaces:    []string{"utun6"},
	CIDRs:     []string{"2.0.0.0/24", "2.0.0.53/32", "10.20.0.0/16"},
	Domains:   []string{"corp.example.com"},
	DNS:       []string{"2.0.0.53"},
	Gateways:  []string{"203.0.113.10"},
}

func lastInput(h *harness) int { h.prof.mu.Lock(); defer h.prof.mu.Unlock(); return len(h.prof.inputs) }

func lastCoexist(h *harness) profile.Coexist {
	h.prof.mu.Lock()
	defer h.prof.mu.Unlock()
	return h.prof.inputs[len(h.prof.inputs)-1].Coexist
}

func TestATrustCoexist(t *testing.T) {
	h := tunHarness(t, []string{"192.168.1.1"})
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetTUN(true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.dns.get(), sysdns.TUNServers) {
		t.Fatalf("auto DNS not applied before aTrust: %v", h.dns.get())
	}
	h.s.TakeNotices()

	// aTrust connects: config recomposed with the coexist layer, the
	// bypass list gains the intranet, our system DNS is put back.
	n := lastInput(h)
	h.vpn.set(sampleATrust)
	h.s.recheckCoexist()
	if lastInput(h) == n {
		t.Fatal("config not recomposed after aTrust appeared")
	}
	co := lastCoexist(h)
	// the ranges holding aTrust's DNS server (its /32 route and the /24
	// around it) stay in the TUN, DIRECT only; the others and the gateway
	// leave it
	if !slices.Equal(co.ExcludeCIDRs, []string{"10.20.0.0/16", "203.0.113.10/32"}) {
		t.Fatalf("ExcludeCIDRs = %v", co.ExcludeCIDRs)
	}
	if !slices.Equal(co.DirectCIDRs, []string{"2.0.0.0/24", "2.0.0.53/32"}) {
		t.Fatalf("DirectCIDRs = %v", co.DirectCIDRs)
	}
	if !slices.Equal(co.CorpDomains, sampleATrust.Domains) || !slices.Equal(co.Processes, sampleATrust.Processes) {
		t.Fatalf("coexist input = %+v", co)
	}
	if !slices.Equal(co.CorpDNS, sampleATrust.DNS) || !co.UDPFallback || !slices.Equal(co.PublicDNS, sysdns.TUNServers) {
		t.Fatalf("DNS of the layer = %+v", co)
	}
	st, _ := h.sys.Get()
	for _, want := range []string{"*.corp.example.com", "corp.example.com", "10.20.0.0/16", "2.0.0.53/32", "203.0.113.10/32"} {
		if !slices.Contains(st.Bypass, want) {
			t.Fatalf("bypass %v lacks %s", st.Bypass, want)
		}
	}
	if !slices.Equal(h.dns.get(), sysdns.TUNServers) {
		t.Fatalf("system DNS while aTrust runs = %v, want auto DNS kept", h.dns.get())
	}
	if s := h.s.Status().ATrust; !s.Running || !s.Applied || !slices.Equal(s.CIDRs, sampleATrust.CIDRs) || !slices.Equal(s.Gateways, sampleATrust.Gateways) {
		t.Fatalf("status = %+v", s)
	}
	if ns := h.s.TakeNotices(); len(ns) != 1 || !strings.Contains(ns[0].Title, "aTrust") {
		t.Fatalf("notices = %+v", ns)
	}

	// nothing changed: no recompose
	n = lastInput(h)
	h.s.recheckCoexist()
	if lastInput(h) != n {
		t.Fatal("recomposed without a change")
	}

	// aTrust quits: everything back
	h.vpn.set(atrust.Info{})
	h.s.recheckCoexist()
	if co = lastCoexist(h); !coexistEmpty(co) {
		t.Fatalf("coexist input after quit = %+v", co)
	}
	st, _ = h.sys.Get()
	if slices.Contains(st.Bypass, "10.20.0.0/16") {
		t.Fatalf("bypass still has the intranet: %v", st.Bypass)
	}
	if !slices.Equal(h.dns.get(), sysdns.TUNServers) {
		t.Fatalf("auto DNS after aTrust quit: %v", h.dns.get())
	}
	if s := h.s.Status().ATrust; s.Running || s.Applied {
		t.Fatalf("status after quit = %+v", s)
	}
}

func TestATrustCompatSetting(t *testing.T) {
	h := tunHarness(t, nil)
	h.vpn.set(sampleATrust)
	h.s.recheckCoexist()
	if !h.s.Status().ATrust.Running {
		t.Fatal("not detected with the default setting")
	}
	h.s.TakeNotices()

	set := h.s.Settings()
	set.ATrustCompat = false
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if co := lastCoexist(h); !coexistEmpty(co) || h.s.Status().ATrust.Running {
		t.Fatalf("setting off still applies coexist: %+v", co)
	}
	if ns := h.s.TakeNotices(); len(ns) != 0 {
		t.Fatalf("turning the setting off must not claim aTrust quit: %+v", ns)
	}
	h.s.recheckCoexist()
	if h.s.Status().ATrust.Running {
		t.Fatal("detected although the setting is off")
	}

	set.ATrustCompat = true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if co := lastCoexist(h); !slices.Contains(co.ExcludeCIDRs, "10.20.0.0/16") {
		t.Fatalf("setting on again: %+v", co)
	}
}

var sampleConfig = model.ATrustConfig{
	CorpDomains:   []string{"intranet.example.net", "corp.example.com"},
	DirectDomains: []string{"chat.example.org"},
	CorpDNS:       []string{"10.1.0.53"},
	ExcludeCIDRs:  []string{"198.18.0.0/16", "172.16.0.0/12"},
	DirectCIDRs:   []string{"10.0.0.0/8"},
}

// TestATrustConfigMerged: the user's lists join the detected ones, and
// changing them reloads the core and the proxy bypass.
func TestATrustConfigMerged(t *testing.T) {
	h := tunHarness(t, nil)
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	h.vpn.set(sampleATrust)
	h.s.recheckCoexist()

	set := h.s.Settings()
	set.ATrust = sampleConfig
	n := lastInput(h)
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if lastInput(h) == n {
		t.Fatal("config not recomposed after the aTrust lists changed")
	}
	co := lastCoexist(h)
	if !slices.Equal(co.ExcludeCIDRs, []string{"10.20.0.0/16", "203.0.113.10/32", "198.18.0.0/16", "172.16.0.0/12"}) {
		t.Fatalf("ExcludeCIDRs = %v", co.ExcludeCIDRs)
	}
	if !slices.Equal(co.DirectCIDRs, []string{"2.0.0.0/24", "2.0.0.53/32", "10.0.0.0/8"}) {
		t.Fatalf("DirectCIDRs = %v", co.DirectCIDRs)
	}
	if !slices.Equal(co.CorpDomains, []string{"corp.example.com", "intranet.example.net"}) || !slices.Equal(co.DirectDomains, []string{"chat.example.org"}) {
		t.Fatalf("domains = %+v", co)
	}
	if !slices.Equal(co.CorpDNS, []string{"10.1.0.53"}) { // configured servers win over the detected ones
		t.Fatalf("CorpDNS = %v", co.CorpDNS)
	}
	st, _ := h.sys.Get()
	for _, want := range []string{"chat.example.org", "*.intranet.example.net", "10.0.0.0/8", "198.18.0.0/16"} {
		if !slices.Contains(st.Bypass, want) {
			t.Fatalf("bypass %v lacks %s", st.Bypass, want)
		}
	}

	// the same lists again: nothing to do
	n = lastInput(h)
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if lastInput(h) != n {
		t.Fatal("recomposed although the lists did not change")
	}
}

// TestATrustAlways: with 未运行 aTrust 时也应用 the lists apply without
// aTrust, without its processes and without the UDP fallback.
func TestATrustAlways(t *testing.T) {
	h := tunHarness(t, nil)
	set := h.s.Settings()
	set.ATrust = sampleConfig
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if co := lastCoexist(h); !coexistEmpty(co) || h.s.Status().ATrust.Applied {
		t.Fatalf("lists applied without aTrust and without Always: %+v", co)
	}

	set.ATrust.Always = true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	co := lastCoexist(h)
	if len(co.Processes) != 0 || co.UDPFallback {
		t.Fatalf("Always layer carries running-only parts: %+v", co)
	}
	if !slices.Equal(co.ExcludeCIDRs, sampleConfig.ExcludeCIDRs) || !slices.Equal(co.DirectCIDRs, sampleConfig.DirectCIDRs) ||
		!slices.Equal(co.CorpDomains, sampleConfig.CorpDomains) || !slices.Equal(co.DirectDomains, sampleConfig.DirectDomains) ||
		!slices.Equal(co.CorpDNS, sampleConfig.CorpDNS) || !slices.Equal(co.PublicDNS, sysdns.TUNServers) {
		t.Fatalf("Always layer = %+v", co)
	}
	if s := h.s.Status().ATrust; s.Running || !s.Applied {
		t.Fatalf("status = %+v", s)
	}

	// aTrust starts: its parts join; quits: the lists stay
	h.vpn.set(sampleATrust)
	h.s.recheckCoexist()
	if co = lastCoexist(h); !co.UDPFallback || !slices.Contains(co.ExcludeCIDRs, "10.20.0.0/16") {
		t.Fatalf("running + Always = %+v", co)
	}
	h.vpn.set(atrust.Info{})
	h.s.recheckCoexist()
	if co = lastCoexist(h); co.UDPFallback || !slices.Equal(co.ExcludeCIDRs, sampleConfig.ExcludeCIDRs) {
		t.Fatalf("after quit with Always = %+v", co)
	}

	// the master switch off: nothing
	set.ATrustCompat = false
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if co = lastCoexist(h); !coexistEmpty(co) || h.s.Status().ATrust.Applied {
		t.Fatalf("switch off still applies: %+v", co)
	}
}

// TestCoexistDNSRanges: a detected range holding the system's or the
// configured company DNS is never kept out of the TUN.
func TestCoexistDNSRanges(t *testing.T) {
	info := atrust.Info{Processes: []string{"aTrust"}, CIDRs: []string{"10.0.0.0/8", "172.16.0.0/12", "198.18.0.0/16"}}
	set := model.Settings{ATrustCompat: true}

	co := coexistInput(info, set, []string{"10.139.1.142"})
	if !slices.Equal(co.ExcludeCIDRs, []string{"172.16.0.0/12", "198.18.0.0/16"}) || !slices.Equal(co.DirectCIDRs, []string{"10.0.0.0/8"}) {
		t.Fatalf("system DNS inside a range: %+v", co)
	}

	set.ATrust.CorpDNS = []string{"172.20.0.53"}
	co = coexistInput(info, set, nil)
	if !slices.Equal(co.ExcludeCIDRs, []string{"10.0.0.0/8", "198.18.0.0/16"}) || !slices.Equal(co.DirectCIDRs, []string{"172.16.0.0/12"}) {
		t.Fatalf("company DNS inside a range: %+v", co)
	}

	// the detector lists aTrust's own DNS as /32 routes: DIRECT only
	info.DNS = []string{"198.18.0.2"}
	info.CIDRs = append(info.CIDRs, "198.18.0.2/32")
	co = coexistInput(info, set, nil)
	if slices.Contains(co.ExcludeCIDRs, "198.18.0.2/32") || !slices.Contains(co.DirectCIDRs, "198.18.0.2/32") {
		t.Fatalf("aTrust DNS excluded: %+v", co)
	}
	// ...and the /16 holding it stays DIRECT only as well
	if slices.Contains(co.ExcludeCIDRs, "198.18.0.0/16") {
		t.Fatalf("range holding aTrust's DNS excluded: %+v", co)
	}

	if co := coexistInput(info, model.Settings{}, nil); !coexistEmpty(co) {
		t.Fatalf("setting off: %+v", co)
	}
}

func TestCoexistDetectedBeforeStart(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.vpn.set(sampleATrust) })
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.prof.mu.Lock()
	co := h.prof.inputs[0].Coexist
	h.prof.mu.Unlock()
	if !slices.Equal(co.CorpDomains, sampleATrust.Domains) {
		t.Fatalf("first compose lacks aTrust: %+v", co)
	}
}

func TestCoexistBypass(t *testing.T) {
	base := []string{"localhost", "*.local"}
	if got := coexistBypass(base, profile.Coexist{}); !slices.Equal(got, base) {
		t.Fatal(got)
	}
	got := coexistBypass(base, profile.Coexist{CorpDomains: []string{"local"}, DirectDomains: []string{"x.org"}, ExcludeCIDRs: []string{"10.0.0.0/8"}, DirectCIDRs: []string{"172.16.0.0/12"}})
	if !slices.Equal(got, []string{"localhost", "*.local", "local", "x.org", "*.x.org", "10.0.0.0/8", "172.16.0.0/12"}) {
		t.Fatal(got)
	}
	got[0] = "x"
	if base[0] != "localhost" {
		t.Fatal("base modified")
	}
}

func TestIsOwnTUNByName(t *testing.T) {
	if !isOwnTUN(profile.TUNDevice) {
		t.Fatal("our device name not recognised")
	}
	if isOwnTUN("utun999999") {
		t.Fatal("an unknown interface taken for ours")
	}
}
