package rpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/app/remote"
	"lanflow/internal/model"
	"lanflow/internal/rpc"
)

// shortDir is a temporary directory short enough for a unix socket path
// (macOS limits it to 104 bytes; t.TempDir is often longer).
func shortDir(t *testing.T) string {
	t.Helper()
	base := ""
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	d, err := os.MkdirTemp(base, "lf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

type harness struct {
	dir   string
	fake  *fake.Fake
	srv   *rpc.Server
	asked atomic.Int32
}

// start serves a fake backend; prompt answers consent requests.
func start(t *testing.T, prompt rpc.PromptFunc) *harness {
	t.Helper()
	h := &harness{dir: shortDir(t), fake: fake.NewInstant()}
	if runtime.GOOS == "windows" {
		t.Setenv("LANFLOW_SOCKET", `\\.\pipe\lanflow-test-`+filepath.Base(h.dir))
	}
	var p rpc.PromptFunc
	if prompt != nil {
		p = func(ctx context.Context, c rpc.Consent) (rpc.Answer, error) {
			h.asked.Add(1)
			return prompt(ctx, c)
		}
	}
	srv, err := rpc.Start(h.dir, rpc.Options{API: h.fake, Version: "test", AuditDir: filepath.Join(h.dir, "logs"), Prompt: p})
	if err != nil {
		t.Fatal(err)
	}
	h.srv = srv
	t.Cleanup(func() { srv.Close() })
	return h
}

func (h *harness) dial(t *testing.T, client string, reveal bool) *remote.Client {
	t.Helper()
	c, err := remote.Dial(context.Background(), remote.Options{DataDir: h.dir, Client: client, Version: "test", Reveal: reveal})
	if err != nil {
		t.Fatal(err)
	}
	c.Timeout = 10 * time.Second
	t.Cleanup(func() { c.Close() })
	return c
}

func code(err error) int {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

func TestRoundTrip(t *testing.T) {
	h := start(t, nil)
	c := h.dial(t, "ui", false)
	if got, want := c.Status().Core, h.fake.Status().Core; got != want {
		t.Fatalf("Status().Core = %v, want %v", got, want)
	}
	if err := c.SelectProxy("节点选择", "香港 01"); err != nil {
		t.Fatal(err)
	}
	for _, g := range h.fake.Proxies().Groups {
		if g.Name == "节点选择" && g.Now != "香港 01" {
			t.Fatalf("selection = %q", g.Now)
		}
	}
	// Errors come back with the server's text.
	if err := c.SelectProxy("没有这个组", "x"); err == nil || !strings.Contains(err.Error(), "没有这个组") {
		t.Fatalf("error = %v", err)
	}
	// Two results.
	up, down := c.TrafficHistory()
	wu, wd := h.fake.TrafficHistory()
	if len(up) != len(wu) || len(down) != len(wd) {
		t.Fatalf("TrafficHistory lengths %d/%d, want %d/%d", len(up), len(down), len(wu), len(wd))
	}
	// The window sees subscription links; agents do not.
	if subs := c.Subscriptions(); len(subs) == 0 || subs[0].URL == "" {
		t.Fatalf("ui subscriptions lack URLs: %+v", subs)
	}
	ctl := h.dial(t, "ctl", false)
	for _, s := range ctl.Subscriptions() {
		if s.URL != "" {
			t.Fatalf("ctl got a subscription URL: %q", s.URL)
		}
	}
	// UpdateUIState runs fn here and sends the state back.
	if err := c.UpdateUIState(func(u *model.UIState) { u.ProxyGrid = true }); err != nil {
		t.Fatal(err)
	}
	if !h.fake.UIState().ProxyGrid {
		t.Fatal("UpdateUIState did not reach the service")
	}
}

func TestHandshake(t *testing.T) {
	h := start(t, nil)
	if _, err := remote.Dial(context.Background(), remote.Options{DataDir: h.dir, Client: "robot"}); code(err) != rpc.CodeHandshake {
		t.Fatalf("bad identity: err = %v", err)
	}
	c := h.dial(t, "mcp:Claude Desktop", false)
	if got := c.Conn().Welcome().Client; got != "mcp:Claude Desktop" {
		t.Fatalf("welcome client = %q", got)
	}
}

func TestNotRunning(t *testing.T) {
	dir := shortDir(t)
	if runtime.GOOS == "windows" {
		t.Setenv("LANFLOW_SOCKET", `\\.\pipe\lanflow-test-none-`+filepath.Base(dir))
	}
	_, err := remote.Dial(context.Background(), remote.Options{DataDir: dir, Client: "ctl"})
	if !errors.Is(err, rpc.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

func TestPermissions(t *testing.T) {
	var answer atomic.Value
	answer.Store(rpc.AnswerDeny)
	h := start(t, func(ctx context.Context, c rpc.Consent) (rpc.Answer, error) {
		return answer.Load().(rpc.Answer), nil
	})
	mcp := h.dial(t, "mcp:claude", false)

	// Reads pass, danger and window-only methods do not.
	if mcp.Status().Core == "" {
		t.Fatalf("read failed: %v", mcp.LastError())
	}
	if err := mcp.InstallTUNHelper(); code(err) != rpc.CodeDenied {
		t.Fatalf("InstallTUNHelper: %v", err)
	}
	if _ = mcp.TakeNotices(); code(mcp.LastError()) != rpc.CodeDenied {
		t.Fatalf("TakeNotices: %v", mcp.LastError())
	}
	if err := mcp.SaveSettings(model.Settings{}); code(err) != rpc.CodeDenied {
		t.Fatalf("SaveSettings: %v", err)
	}

	// The first write asks; "deny" is remembered.
	if err := mcp.SetMode(model.ModeGlobal); code(err) != rpc.CodeConsent {
		t.Fatalf("SetMode after deny: %v", err)
	}
	if h.asked.Load() != 1 {
		t.Fatalf("asked %d times", h.asked.Load())
	}
	if err := mcp.SetMode(model.ModeGlobal); code(err) != rpc.CodeConsent || h.asked.Load() != 1 {
		t.Fatalf("second SetMode: %v, asked %d", err, h.asked.Load())
	}
	if g := h.fake.Settings().AgentGrants; len(g) != 1 || g[0].Agent != "claude" || g[0].Allow {
		t.Fatalf("grants = %+v", g)
	}

	// After revoking (the settings page), "always" lets writes through and
	// is remembered.
	s := h.fake.Settings()
	s.AgentGrants = nil
	if err := h.fake.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	answer.Store(rpc.AnswerAlways)
	if err := mcp.SetMode(model.ModeGlobal); err != nil {
		t.Fatal(err)
	}
	other := h.dial(t, "mcp:claude", false)
	if err := other.SetMode(model.ModeRule); err != nil {
		t.Fatal(err)
	}
	if h.asked.Load() != 2 {
		t.Fatalf("asked %d times, want 2", h.asked.Load())
	}

	// "once" holds for one connection only.
	answer.Store(rpc.AnswerOnce)
	cur := h.dial(t, "mcp:cursor", false)
	if err := cur.SetMode(model.ModeGlobal); err != nil {
		t.Fatal(err)
	}
	if err := cur.SetMode(model.ModeRule); err != nil {
		t.Fatal(err)
	}
	if h.asked.Load() != 3 {
		t.Fatalf("asked %d times, want 3", h.asked.Load())
	}
	if _, ok := grantOf(h.fake.Settings(), "cursor"); ok {
		t.Fatal("once was stored as a grant")
	}

	// Sensitive reads: ctl needs --reveal, then asks.
	ctl := h.dial(t, "ctl", false)
	if _ = ctl.Connections(); code(ctl.LastError()) != rpc.CodeDenied {
		t.Fatalf("ctl Connections without reveal: %v", ctl.LastError())
	}
	rev := h.dial(t, "ctl", true)
	answer.Store(rpc.AnswerOnce)
	if _ = rev.Connections(); rev.LastError() != nil {
		t.Fatalf("ctl --reveal Connections: %v", rev.LastError())
	}
	// ctl writes go through without asking, but are audited.
	n := h.asked.Load()
	if err := ctl.SetMode(model.ModeDirect); err != nil {
		t.Fatal(err)
	}
	if h.asked.Load() != n {
		t.Fatal("ctl write asked for consent")
	}
	var entries []rpc.AuditEntry
	if err := ctl.Conn().CallAPI(context.Background(), "AuditLog", []any{50}, &entries); err != nil {
		t.Fatal(err)
	}
	if last := entries[len(entries)-1]; last.Client != "ctl" || last.Method != "SetMode" || last.Result != "ok" {
		t.Fatalf("last audit entry = %+v", last)
	}
	b, err := os.ReadFile(filepath.Join(h.dir, "logs", rpc.AuditFile))
	if err != nil || !strings.Contains(string(b), `"method":"InstallTUNHelper"`) {
		t.Fatalf("audit.log lacks the refused call: %v\n%s", err, b)
	}
}

func grantOf(s model.Settings, agent string) (bool, bool) {
	for _, g := range s.AgentGrants {
		if g.Agent == agent {
			return g.Allow, true
		}
	}
	return false, false
}

func TestNoPrompterRefuses(t *testing.T) {
	h := start(t, nil)
	mcp := h.dial(t, "mcp:x", false)
	if err := mcp.SetMode(model.ModeGlobal); code(err) != rpc.CodeConsent {
		t.Fatalf("err = %v", err)
	}
}

func TestEvents(t *testing.T) {
	h := start(t, nil)
	c := h.dial(t, "ctl", false)
	got := make(chan app.Topic, 16)
	off := c.OnChange(func(t app.Topic) { got <- t })
	defer off()
	// The subscription is in place once a later call returns.
	_ = c.Status()
	if err := c.SelectProxy("节点选择", "香港 01"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case tp := <-got:
			if tp == app.TopicProxies {
				return
			}
		case <-deadline:
			t.Fatal("no proxies event")
		}
	}
}

func TestPatchDryRunAndUndo(t *testing.T) {
	h := start(t, nil)
	c := h.dial(t, "ctl", false)
	before := h.fake.Settings().Mode
	var res rpc.PatchResult
	if err := c.Conn().CallAPI(context.Background(), "PatchSettings", []any{map[string]any{"mode": "global"}, true}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Applied || len(res.Changed) != 1 || res.Changed[0] != "Mode" {
		t.Fatalf("dry run = %+v", res)
	}
	plus := false
	for _, l := range res.Diff {
		if l.Op == '+' && strings.Contains(l.Text, `"global"`) {
			plus = true
		}
	}
	if !plus {
		t.Fatalf("diff lacks the new mode: %+v", res.Diff)
	}
	if h.fake.Settings().Mode != before {
		t.Fatal("dry run changed the settings")
	}
	// Unknown and locked fields.
	if err := c.Conn().CallAPI(context.Background(), "PatchSettings", []any{map[string]any{"Nope": 1}, true}); code(err) != rpc.CodeInvalidParams {
		t.Fatalf("unknown field: %v", err)
	}
	if err := c.Conn().CallAPI(context.Background(), "PatchSettings", []any{map[string]any{"CorePath": "/tmp/x"}, false}); code(err) != rpc.CodeInvalidParams {
		t.Fatalf("locked field: %v", err)
	}

	// Apply, then undo.
	if err := c.Conn().CallAPI(context.Background(), "PatchSettings", []any{map[string]any{"Mode": "global", "DNS": map[string]any{"TakeOver": true}}, false}, &res); err != nil {
		t.Fatal(err)
	}
	if s := h.fake.Settings(); s.Mode != model.ModeGlobal || !s.DNS.TakeOver {
		t.Fatalf("patch not applied: %v %v", s.Mode, s.DNS.TakeOver)
	}
	rulesBefore := h.fake.CustomRules()
	if _, err := c.SaveCustomRule(model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: "example.org", Target: "DIRECT"}); err != nil {
		t.Fatal(err)
	}
	if len(h.fake.CustomRules()) != len(rulesBefore)+1 {
		t.Fatal("rule not added")
	}
	var u rpc.UndoResult
	if err := c.Conn().CallAPI(context.Background(), "Undo", nil, &u); err != nil {
		t.Fatal(err)
	}
	if u.Method != "SaveCustomRule" || len(h.fake.CustomRules()) != len(rulesBefore) {
		t.Fatalf("undo = %+v, rules = %d", u, len(h.fake.CustomRules()))
	}
	if err := c.Conn().CallAPI(context.Background(), "Undo", nil, &u); err != nil {
		t.Fatal(err)
	}
	if s := h.fake.Settings(); u.Method != "PatchSettings" || s.Mode != before || s.DNS.TakeOver {
		t.Fatalf("undo = %+v, mode %v, takeover %v", u, s.Mode, s.DNS.TakeOver)
	}
	// Deleting a rule comes back in place.
	rules := h.fake.CustomRules()
	if len(rules) < 2 {
		t.Skip("fake has too few rules")
	}
	if err := c.DeleteCustomRule(rules[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Conn().CallAPI(context.Background(), "Undo", nil, &u); err != nil {
		t.Fatal(err)
	}
	got := h.fake.CustomRules()
	if len(got) != len(rules) || got[1].Payload != rules[1].Payload {
		t.Fatalf("rules after undo = %+v, want %+v", got, rules)
	}
	if err := c.Conn().CallAPI(context.Background(), "Undo", nil, &u); code(err) != rpc.CodeApp {
		t.Fatalf("empty undo: %v", err)
	}
}

func TestSettingsRedacted(t *testing.T) {
	h := start(t, nil)
	s := h.fake.Settings()
	s.WebDAV.URL = "https://dav.example.com/remote.php/dav/files/me"
	s.Advanced.Authentication = []string{"alice:secret"}
	s.DNS.Nameserver = []string{"https://dns.example.com/dns-query/abc123"}
	if err := h.fake.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	c := h.dial(t, "mcp:x", false)
	var raw json.RawMessage
	if err := c.Conn().CallAPI(context.Background(), "Settings", nil, &raw); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"secret", "abc123", "files/me"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("redacted settings contain %q: %s", leak, raw)
		}
	}
}
