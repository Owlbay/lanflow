package app

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/model"
)

// waitProbe polls until cond holds on the probe state (background work).
func waitProbe(t *testing.T, s *Service, cond func(model.ProbeState) bool) model.ProbeState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p := s.Probe(); cond(p) {
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("probe state never satisfied: %+v", s.Probe())
	return model.ProbeState{}
}

// ProbeIP through a node selects it in the probe group, measures the
// exit with the locator and, when asked, reads both online sources
// through the probe listener, naming each; the offline native verdict
// compares the database with the trace.
func TestProbeIPNodeOnline(t *testing.T) {
	h := newHarness(t, nil)
	var mu sync.Mutex
	var seen []string
	client := siteServer(t, func(w http.ResponseWriter, host, path string, r *http.Request) {
		mu.Lock()
		seen = append(seen, host)
		mu.Unlock()
		switch host {
		case "ip-api.com":
			_, _ = w.Write([]byte(`{"status":"success","query":"45.76.211.38","countryCode":"JP","isp":"Vultr","mobile":false,"proxy":true,"hosting":true}`))
		case "my.ippure.com":
			_, _ = w.Write([]byte(`{"ip":"45.76.211.38","countryCode":"US","fraudScore":42,"isResidential":false,"isBroadcast":true}`))
		default:
			http.NotFound(w, r)
		}
	})
	h.s.d.httpVia = func(int, time.Duration) *http.Client { return client }
	if err := h.s.ProbeIP("东京 03", true); err == nil {
		t.Fatal("must need the running core")
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.ProbeIP("", true); err == nil {
		t.Fatal("empty target must be refused")
	}
	if err := h.s.ProbeIP("节点选择", true); err == nil || !strings.Contains(err.Error(), "代理组") {
		t.Fatalf("group target: %v", err)
	}
	if err := h.s.ProbeIP("东京 03", true); err != nil {
		t.Fatal(err)
	}
	if p := h.s.Probe(); !p.IP["东京 03"].Running {
		t.Fatalf("not running: %+v", p.IP)
	}
	p := waitProbe(t, h.s, func(p model.ProbeState) bool { return !p.IP["东京 03"].Running })
	rep := p.IP["东京 03"]
	if rep.Error != "" || rep.Colo != "HKG" || rep.Place == nil || rep.Place.Country != "HK" || rep.Target != "东京 03" || rep.CheckedAt.IsZero() {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Risk) != 2 || rep.Risk[0].Source != srcIPAPI || rep.Risk[1].Source != srcIppure {
		t.Fatalf("risk = %+v", rep.Risk)
	}
	if r := rep.Risk[0]; !r.HasFlags || !r.Proxy || !r.Hosting || r.Mobile || r.RegionOfBlock != "JP" {
		t.Errorf("ip-api = %+v", r)
	}
	if r := rep.Risk[1]; !r.HasScore || r.Score != 42 || !r.HasBroadcast || !r.Broadcast || r.Residential {
		t.Errorf("ippure = %+v", r)
	}
	if rep.Kind != model.IPKindDatacenter || rep.Native != model.NativeBroadcast || !strings.Contains(rep.NativeWhy, "ippure.com") {
		t.Errorf("kind/native = %q %q %q", rep.Kind, rep.Native, rep.NativeWhy)
	}
	if strings.Join(rep.Sources, ",") != strings.Join([]string{srcTrace, srcIPAPI, srcIppure}, ",") {
		t.Errorf("sources = %q", rep.Sources)
	}
	if len(seen) != 2 {
		t.Errorf("online requests = %q, want one per source", seen)
	}
	h.ctrl.mu.Lock()
	sel := h.ctrl.selected
	h.ctrl.mu.Unlock()
	if len(sel) == 0 || sel[len(sel)-1] != [2]string{probeGroup, "东京 03"} {
		t.Errorf("probe group selections: %v", sel)
	}
	h.loc.mu.Lock()
	probed := append([]string(nil), h.loc.probed...)
	h.loc.mu.Unlock()
	if len(probed) == 0 || probed[len(probed)-1] != "东京 03" {
		t.Errorf("locator probed %v", probed)
	}
}

// Without online, no request leaves; the direct exit needs no core and
// the native verdict rests on the local comparison.
func TestProbeIPDirectOffline(t *testing.T) {
	h := newHarness(t, nil)
	called := false
	h.s.d.httpDirect = func(time.Duration) *http.Client {
		called = true
		return &http.Client{}
	}
	if err := h.s.ProbeIP(model.ProbeDirect, false); err != nil {
		t.Fatal(err)
	}
	p := waitProbe(t, h.s, func(p model.ProbeState) bool { return !p.IP[model.ProbeDirect].Running })
	rep := p.IP[model.ProbeDirect]
	if rep.Error != "" || rep.IP != "1.2.3.4" || len(rep.Risk) != 0 || rep.RiskError != "" {
		t.Fatalf("report = %+v", rep)
	}
	if called {
		t.Error("offline probe must not make HTTP requests of its own")
	}
	if rep.Native != model.NativeUnknown {
		t.Errorf("native without database country = %q", rep.Native)
	}
}

// Online sources that fail leave the offline report intact and say why.
func TestProbeIPOnlineFailure(t *testing.T) {
	h := newHarness(t, nil)
	h.s.d.httpDirect = func(time.Duration) *http.Client { return failingClient() }
	if err := h.s.ProbeIP(model.ProbeDirect, true); err != nil {
		t.Fatal(err)
	}
	p := waitProbe(t, h.s, func(p model.ProbeState) bool { return !p.IP[model.ProbeDirect].Running })
	rep := p.IP[model.ProbeDirect]
	if rep.Error != "" || rep.IP != "1.2.3.4" || len(rep.Risk) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if !strings.Contains(rep.RiskError, srcIPAPI) || !strings.Contains(rep.RiskError, srcIppure) {
		t.Errorf("risk error = %q", rep.RiskError)
	}
}

func TestIPReportOfflineNative(t *testing.T) {
	jp := model.Place{Country: "JP", City: "东京"}
	us := model.Place{Country: "US", City: "美国"}
	rep := ipReportFrom("n", model.ExitInfo{Exit: model.EndpointInfo{IP: "1.1.1.1", ASN: "AS20473", ISP: "Vultr", Place: &us}, Place: &jp, Colo: "NRT"})
	if rep.Native != model.NativeBroadcast || rep.DBCountry != "US" || rep.Kind != model.IPKindDatacenter || rep.Colo != "NRT" {
		t.Errorf("broadcast report = %+v", rep)
	}
	rep = ipReportFrom("n", model.ExitInfo{Exit: model.EndpointInfo{IP: "1.1.1.1", Place: &jp}, Place: &jp})
	if rep.Native != model.NativeYes || rep.Kind != model.IPKindUnknown {
		t.Errorf("native report = %+v", rep)
	}
	if len(rep.Sources) != 2 || rep.Sources[0] != srcTrace {
		t.Errorf("sources = %q", rep.Sources)
	}
}

// ProbeUnlock reports results one by one and finishes with every service
// judged; a second run while one is going is refused.
func TestProbeUnlock(t *testing.T) {
	h := newHarness(t, nil)
	client := siteServer(t, func(w http.ResponseWriter, host, path string, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	h.s.d.httpVia = func(int, time.Duration) *http.Client { return client }
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.ProbeUnlock("香港 01"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.ProbeUnlock("香港 01"); err == nil {
		t.Fatal("second run must be refused while the first runs")
	}
	p := waitProbe(t, h.s, func(p model.ProbeState) bool { return !p.Unlock["香港 01"].Running })
	rep := p.Unlock["香港 01"]
	if rep.Error != "" || rep.Done != len(unlockChecks) || len(rep.Results) != len(unlockChecks) || rep.CheckedAt.IsZero() {
		t.Fatalf("report = %+v", rep)
	}
	for _, r := range rep.Results {
		if r.Status == model.UnlockPending || r.Status == model.UnlockError {
			t.Errorf("%s = %+v", r.Service, r)
		}
	}
}

// ProbeRegions groups nodes by the name guess; ProbeRegion tests the
// delay of each, measures its exit and (when asked) its unlocks.
func TestProbeRegion(t *testing.T) {
	h := newHarness(t, nil)
	client := siteServer(t, func(w http.ResponseWriter, host, path string, r *http.Request) {
		_, _ = w.Write([]byte("ok ip=1.1.1.1\nloc=JP\n"))
	})
	h.s.d.httpVia = func(int, time.Duration) *http.Client { return client }
	if err := h.s.ProbeRegion("JP", false); err == nil {
		t.Fatal("must need the running core")
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	regions := h.s.ProbeRegions()
	if len(regions) != 1 || regions[0].Country != "JP" || len(regions[0].Nodes) != 1 || regions[0].Nodes[0] != "东京 03" {
		t.Fatalf("regions = %+v", regions)
	}
	if err := h.s.ProbeRegion("US", false); err == nil {
		t.Fatal("unknown region must be refused")
	}
	if err := h.s.ProbeRegion("JP", true); err != nil {
		t.Fatal(err)
	}
	if err := h.s.ProbeRegion("JP", true); err == nil {
		t.Fatal("second run must be refused while the first runs")
	}
	p := waitProbe(t, h.s, func(p model.ProbeState) bool { return !p.Region.Running })
	rep := p.Region
	if rep.Country != "JP" || rep.Error != "" || rep.Done != 1 || len(rep.Nodes) != 1 || !rep.WithUnlock {
		t.Fatalf("report = %+v", rep)
	}
	n := rep.Nodes[0]
	if n.Node != "东京 03" || n.Testing || n.Delay != 100 || !n.Probed || n.Exit.Place == nil || n.Exit.Place.Country != "HK" || len(n.Unlock) != len(unlockChecks) {
		t.Fatalf("node = %+v", n)
	}
	if err := h.s.SetMode(model.ModeDirect); err != nil {
		t.Fatal(err)
	}
	if err := h.s.ProbeRegion("JP", false); err == nil {
		t.Fatal("direct mode must be refused")
	}
}
