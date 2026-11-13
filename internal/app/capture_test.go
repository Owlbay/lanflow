package app

import (
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/capture/agent"
	"lanflow/internal/capture/ca"
	"lanflow/internal/controller"
	"lanflow/internal/model"
)

// The test binary doubles as the capture agent: with
// LANFLOW_TEST_CAPTURE_KEYS set it runs agent.Main with a file key store
// in that directory (never the Keychain).
func TestMain(m *testing.M) {
	if os.Getenv(leftoverSleepEnv) == "1" {
		// A process left behind (leftover_test.go): runs until its stdin
		// closes, or a minute at most.
		go func() { _, _ = io.Copy(io.Discard, os.Stdin); os.Exit(0) }()
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if dir := os.Getenv("LANFLOW_TEST_CAPTURE_KEYS"); dir != "" {
		os.Exit(agent.Main(os.Stdin, os.Stdout, ca.SecretKeys{S: fileSecrets(dir)}))
	}
	os.Exit(m.Run())
}

type fileSecrets string

func (d fileSecrets) Get(k string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(string(d), hex.EncodeToString([]byte(k))))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ca.ErrNoKey
	}
	return b, err
}
func (d fileSecrets) Set(k string, v []byte) error {
	return os.WriteFile(filepath.Join(string(d), hex.EncodeToString([]byte(k))), v, 0o600)
}
func (d fileSecrets) Delete(k string) error {
	os.Remove(filepath.Join(string(d), hex.EncodeToString([]byte(k))))
	return nil
}

type fakeTrust struct {
	mu        sync.Mutex
	installed map[string]bool
	installs  int
	removes   int
	deny      bool
}

func (f *fakeTrust) hooks() func() {
	old := TrustHooks
	TrustHooks.Install = func(der []byte) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.installs++
		if !f.deny {
			f.installed[ca.Fingerprint(der)] = true
		}
		return nil
	}
	TrustHooks.Remove = func(der []byte) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.removes++
		delete(f.installed, ca.Fingerprint(der))
		return nil
	}
	TrustHooks.Trusted = func(leaf *x509.Certificate) bool {
		if leaf == nil {
			return false
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		// trusted when the leaf's issuer is an installed root
		for fp := range f.installed {
			if fp == leafIssuerFP(leaf) {
				return true
			}
		}
		return false
	}
	return func() { TrustHooks = old }
}

var issuerFPs sync.Map // subject key id → root fingerprint (filled by the test)

func leafIssuerFP(leaf *x509.Certificate) string {
	v, _ := issuerFPs.Load(string(leaf.AuthorityKeyId))
	s, _ := v.(string)
	return s
}

func TestCaptureLifecycle(t *testing.T) {
	keys := t.TempDir()
	oldCmd := CaptureAgentCommand
	CaptureAgentCommand = func() (*exec.Cmd, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "LANFLOW_TEST_CAPTURE_KEYS="+keys)
		return cmd, nil
	}
	defer func() { CaptureAgentCommand = oldCmd }()
	ft := &fakeTrust{installed: map[string]bool{}}
	defer ft.hooks()()

	h := newHarness(t, nil)
	s := h.s
	if err := s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if st := s.CaptureStatus(); st.CA != model.CANone || st.Enabled {
		t.Fatalf("initial %+v", st)
	}
	if err := s.SetCapture(true); err == nil {
		t.Fatal("turned on without acknowledgement")
	}
	if err := s.SaveCaptureSettings(model.CaptureSettings{Domains: []string{"bad domain"}}); err == nil {
		t.Fatal("bad pattern saved")
	}
	if err := s.SaveCaptureSettings(model.CaptureSettings{Domains: []string{"api.example.com"}, Acknowledged: true, AutoOffMin: -1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCapture(true); err == nil || !strings.Contains(err.Error(), "根证书") {
		t.Fatalf("turned on without a trusted root: %v", err)
	}

	// install: the agent makes the root, the window only gets the certificate
	s.capLoad()
	if err := installWithIssuer(s); err != nil {
		t.Fatal(err)
	}
	st := s.CaptureStatus()
	if st.CA != model.CATrusted || ft.installs != 1 {
		t.Fatalf("after install %+v installs=%d", st, ft.installs)
	}
	pemb, _ := os.ReadFile(filepath.Join(h.dir, "capture", "ca.pem"))
	if strings.Contains(string(pemb), "PRIVATE") {
		t.Fatal("private key in the window's files")
	}
	if ents, _ := os.ReadDir(keys); len(ents) != 1 {
		t.Fatalf("keys stored: %d", len(ents))
	}

	reloads := h.ctrl.reloads
	if err := s.SetCapture(true); err != nil {
		t.Fatal(err)
	}
	st = s.CaptureStatus()
	if !st.Enabled || !st.AgentUp || st.Until.IsZero() || time.Until(st.Until) > 31*time.Minute {
		t.Fatalf("on: %+v", st)
	}
	if h.ctrl.reloads <= reloads {
		t.Fatal("no reload on turning on")
	}
	in := h.prof.inputs[len(h.prof.inputs)-1].Capture
	if in.AgentPort == 0 || in.UpPort == 0 || !strings.HasPrefix(in.Domain.Outbound, "LANFLOW-MITM-") || len(in.Domains) != 1 || len(in.Bypass) == 0 {
		t.Fatalf("compose input %+v", in)
	}

	// the agent dies: restarted on the same port
	s.capst.mu.Lock()
	r := s.capst.run
	s.capst.mu.Unlock()
	r.cmd.Process.Kill()
	if !waitForLong(func() bool {
		s.capst.mu.Lock()
		defer s.capst.mu.Unlock()
		return s.capst.run != nil && s.capst.run != r
	}) {
		t.Fatal("agent not restarted")
	}
	s.capst.mu.Lock()
	nr := s.capst.run
	s.capst.mu.Unlock()
	if nr.agentPort != r.agentPort || nr.outbound != r.outbound {
		t.Fatalf("restart changed the port or outbound: %d→%d", r.agentPort, nr.agentPort)
	}

	// turning off: reload, close the decrypted connections, stop the agent
	h.ctrl.mu.Lock()
	h.ctrl.conns = controller.Connections{Connections: []controller.Connection{
		{ID: "dec", Chains: []string{nr.outbound}}, {ID: "plain", Chains: []string{"DIRECT"}}}}
	h.ctrl.mu.Unlock()
	if err := s.SetCapture(false); err != nil {
		t.Fatal(err)
	}
	h.ctrl.mu.Lock()
	closed := slices.Clone(h.ctrl.closedIDs)
	h.ctrl.mu.Unlock()
	if !slices.Contains(closed, "dec") || slices.Contains(closed, "plain") {
		t.Fatalf("closed %v", closed)
	}
	select {
	case <-nr.done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent still running")
	}
	if in := h.prof.inputs[len(h.prof.inputs)-1].Capture; in.AgentPort != 0 {
		t.Fatal("layer still composed after turning off")
	}
	if s.CaptureStatus().Settings.LastDisabled.IsZero() {
		t.Fatal("LastDisabled not recorded")
	}

	// auto off by time
	if err := s.SetCapture(true); err != nil {
		t.Fatal(err)
	}
	s.capst.mu.Lock()
	s.capst.until = time.Now().Add(-time.Second)
	s.capst.mu.Unlock()
	s.captureTick()
	if s.CaptureStatus().Enabled {
		t.Fatal("not turned off after the deadline")
	}

	// seven days after turning off: trust and key removed
	set := s.CaptureStatus().Settings
	set.LastDisabled = time.Now().Add(-8 * 24 * time.Hour)
	s.capst.mu.Lock()
	s.capst.set.LastDisabled = set.LastDisabled
	s.capst.mu.Unlock()
	s.captureAutoRemove()
	if st := s.CaptureStatus(); st.CA != model.CANone || ft.removes == 0 {
		t.Fatalf("not removed: %+v", st)
	}
	if ents, _ := os.ReadDir(keys); len(ents) != 0 {
		t.Fatal("key left behind")
	}
	if _, err := os.Stat(filepath.Join(h.dir, "capture", "ca.pem")); err == nil {
		t.Fatal("certificate left behind")
	}
}

// installWithIssuer runs InstallCACert and teaches the fake trust which
// root the trust leaf comes from.
func installWithIssuer(s *Service) error {
	old := TrustHooks.Install
	TrustHooks.Install = func(der []byte) error {
		c, _ := x509.ParseCertificate(der)
		issuerFPs.Store(string(c.SubjectKeyId), ca.Fingerprint(der))
		return old(der)
	}
	defer func() { TrustHooks.Install = old }()
	return s.InstallCACert()
}

func waitForLong(cond func() bool) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestCaptureAppFor(t *testing.T) {
	a := CaptureAppFor("/Applications/Slack.app/Contents/Frameworks/Slack Helper.app/Contents/MacOS/Slack Helper", "Slack Helper")
	if a.Match != "/Applications/Slack.app/*" || a.Name != "Slack" || a.Kind != "path" {
		t.Fatalf("%+v", a)
	}
	if a := CaptureAppFor("/usr/bin/curl", "curl"); a.Match != "/usr/bin/curl" {
		t.Fatalf("%+v", a)
	}
	if a := CaptureAppFor("", "curl"); a.Kind != "name" || CaptureAppRule(a) != "PROCESS-NAME,curl" {
		t.Fatalf("%+v", a)
	}
}

func TestCaptureDiagRedaction(t *testing.T) {
	y := []byte("rule-providers:\n  lanflow-mitm-sel:\n    type: inline\n    behavior: classical\n    payload:\n      - DOMAIN,secret-bank.example\n      - DOMAIN,b.example\nlisteners:\n  - name: lanflow-mitm-up\n    users:\n      - username: lanflow\n        password: abcdef\n")
	out := string(redactConfig(y))
	if strings.Contains(out, "secret-bank") || strings.Contains(out, "abcdef") || !strings.Contains(out, "(2 项，已隐藏)") {
		t.Fatal(out)
	}
	log := []byte("a\n[TCP] 127.0.0.1:1 --> bank.example:443 match AND using LANFLOW-MITM-1234\nb\n")
	if got := string(dropCaptureLines(log)); got != "a\nb\n" {
		t.Fatalf("%q", got)
	}
}

// An agent that has not answered yet (waiting on a keychain prompt) is
// ended when Lanflow quits, instead of holding up Shutdown for a minute.
func TestAgentStartEndsOnShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sleep")
	}
	oldCmd := CaptureAgentCommand
	CaptureAgentCommand = func() (*exec.Cmd, error) { return exec.Command("sleep", "60"), nil }
	defer func() { CaptureAgentCommand = oldCmd }()

	h := newHarness(t, nil)
	done := make(chan error, 1)
	go func() {
		_, _, err := h.s.agentStart(agent.Start{Op: "serve"})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	shut := make(chan struct{})
	go func() { h.s.Shutdown(); close(shut) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("agentStart succeeded without an agent")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agentStart still waits after Shutdown")
	}
	select {
	case <-shut:
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown did not finish")
	}
}
