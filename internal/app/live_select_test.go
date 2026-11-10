//go:build darwin

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"lanflow/internal/model"
)

// TestLiveSelectionSurvivesRestart runs the real core (no system proxy, no
// TUN, nothing outside a temp directory) and checks that a node picked in
// a select group is still picked after Lanflow quits and starts again.
// Opt-in: LANFLOW_LIVE_SELECT=1 with LANFLOW_CORE pointing at a mihomo binary.
func TestLiveSelectionSurvivesRestart(t *testing.T) {
	if os.Getenv("LANFLOW_LIVE_SELECT") != "1" {
		t.Skip("set LANFLOW_LIVE_SELECT=1 and LANFLOW_CORE=<mihomo> to run")
	}
	coreBin := os.Getenv("LANFLOW_CORE")
	root := t.TempDir()
	sub := filepath.Join(root, "sub.yaml")
	if err := os.WriteFile(sub, []byte(`proxies:
  - {name: a1, type: http, server: 127.0.0.1, port: 1}
  - {name: a2, type: http, server: 127.0.0.1, port: 2}
  - {name: b1, type: http, server: 127.0.0.1, port: 3}
  - {name: b2, type: http, server: 127.0.0.1, port: 4}
proxy-groups:
  - {name: 节点选择, type: select, proxies: [A, B, DIRECT]}
  - {name: A, type: select, proxies: [a1, a2]}
  - {name: B, type: select, proxies: [b1, b2]}
rules:
  - MATCH,节点选择
`), 0o600); err != nil {
		t.Fatal(err)
	}

	start := func(first bool) *Service {
		s, err := NewService(Options{DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "log"), CacheDir: filepath.Join(root, "cache")})
		if err != nil {
			t.Fatal(err)
		}
		if first {
			set := s.Settings()
			set.CorePath, set.MixedPort, set.ProbeExits, set.DisableUpdateCheck = coreBin, freePort(t), false, true
			set.TUN, set.Mode, set.AutoSystemProxy = false, model.ModeRule, false
			if err := s.SaveSettings(set); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ImportSubscriptionFile(sub, true); err != nil {
				t.Fatal(err)
			}
		}
		e2eWait(t, 30*time.Second, "core running", func() bool { return s.Status().Core == model.CoreRunning })
		e2eWait(t, 10*time.Second, "groups loaded", func() bool { return groupNow(s, "B") != "" })
		return s
	}

	s := start(true)
	for _, sel := range [][2]string{{"B", "b2"}, {"A", "a2"}, {"节点选择", "B"}} {
		if err := s.SelectProxy(sel[0], sel[1]); err != nil {
			t.Fatal(err)
		}
	}
	s.Shutdown()
	// Without the core's cache.db the core starts on its defaults; the
	// selections must come back from Lanflow's own record.
	if err := os.Remove(filepath.Join(root, "data", "core", "cache.db")); err != nil {
		t.Fatal(err)
	}

	s = start(false)
	defer s.Shutdown()
	for _, want := range [][2]string{{"B", "b2"}, {"A", "a2"}, {"节点选择", "B"}} {
		deadline := time.Now().Add(5 * time.Second)
		for groupNow(s, want[0]) != want[1] && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if got := groupNow(s, want[0]); got != want[1] {
			t.Errorf("after restart %s = %q, want %q", want[0], got, want[1])
		}
	}
}

func groupNow(s *Service, name string) string {
	for _, g := range s.Proxies().Groups {
		if g.Name == name {
			return g.Now
		}
	}
	return ""
}
