package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"lanflow/internal/app/remote"
	"lanflow/internal/rpc"
	"lanflow/internal/rpc/rpctest"
)

func TestToolName(t *testing.T) {
	for in, want := range map[string]string{
		"SelectProxy": "select_proxy", "DNSQuery": "dns_query", "ImportSubscriptionURL": "import_subscription_url",
		"TUNHelperInstalled": "tun_helper_installed", "ProbeIP": "probe_ip", "Status": "status",
		"TestWebDAV": "test_webdav", "ATrustReference": "atrust_reference",
	} {
		if got := ToolName(in); got != want {
			t.Errorf("ToolName(%s) = %s, want %s", in, got, want)
		}
	}
}

// TestToolList: tools come from the manifest filtered by level; names
// are unique and every schema is an object.
func TestToolList(t *testing.T) {
	tools := Tools()
	seen := map[string]bool{}
	for _, tl := range tools {
		if seen[tl.Name] {
			t.Errorf("duplicate tool %s", tl.Name)
		}
		seen[tl.Name] = true
		if tl.InputSchema["type"] != "object" {
			t.Errorf("%s: schema type %v", tl.Name, tl.InputSchema["type"])
		}
		if _, err := json.Marshal(tl); err != nil {
			t.Errorf("%s: %v", tl.Name, err)
		}
	}
	for _, m := range rpc.Manifest() {
		if (m.Level == rpc.LevelDanger || m.Level == rpc.LevelUI) && seen[ToolName(m.Name)] {
			t.Errorf("%s (%s) is offered", m.Name, m.Level)
		}
	}
	for _, want := range []string{"status", "proxies", "select_proxy", "test_rule", "patch_settings", "undo",
		"route_domain", "diagnose_site", "switch_node", "diagnose"} {
		if !seen[want] {
			t.Errorf("tool %s missing", want)
		}
	}
	if !seen["build_feedback"] {
		t.Error("tool build_feedback missing: agents may draft a report")
	}
	for _, never := range []string{"install_tun_helper", "delete_subscription", "save_settings", "take_notices", "shutdown", "export_settings",
		"send_feedback", "set_feedback_token", "save_feedback"} {
		if seen[never] {
			t.Errorf("tool %s must not be offered", never)
		}
	}
}

type session struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Scanner
	id  int
}

func newSession(t *testing.T, s *rpctest.Server) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := &Server{Version: "test", Dial: func(ctx context.Context, client string) (*remote.Client, error) {
		return s.Dial(client, false)
	}}
	go func() { _ = srv.Serve(context.Background(), inR, outW); outW.Close() }()
	t.Cleanup(func() { inW.Close() })
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	return &session{t: t, in: inW, out: sc}
}

func (s *session) call(method string, params any) map[string]any {
	s.t.Helper()
	s.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": s.id, "method": method, "params": params})
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		s.t.Fatal(err)
	}
	done := make(chan map[string]any, 1)
	go func() {
		if !s.out.Scan() {
			done <- nil
			return
		}
		var m map[string]any
		_ = json.Unmarshal(s.out.Bytes(), &m)
		done <- m
	}()
	select {
	case m := <-done:
		if m == nil {
			s.t.Fatal("server closed")
		}
		if m["error"] != nil {
			s.t.Fatalf("%s: %v", method, m["error"])
		}
		return m["result"].(map[string]any)
	case <-time.After(15 * time.Second):
		s.t.Fatalf("%s: no answer", method)
	}
	return nil
}

func (s *session) tool(name string, args map[string]any) (string, bool) {
	s.t.Helper()
	res := s.call("tools/call", map[string]any{"name": name, "arguments": args})
	content := res["content"].([]any)[0].(map[string]any)
	return content["text"].(string), res["isError"] == true
}

func TestRouteDomainEndToEnd(t *testing.T) {
	asked := 0
	srv := rpctest.Start(t, func(ctx context.Context, c rpc.Consent) (rpc.Answer, error) {
		asked++
		if c.Agent != "claude-code" || c.Kind != rpc.LevelWrite {
			t.Errorf("consent = %+v", c)
		}
		return rpc.AnswerAlways, nil
	})
	s := newSession(t, srv)
	init := s.call("initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]any{"name": "claude-code"}})
	if init["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize = %v", init)
	}
	list := s.call("tools/list", map[string]any{})
	if len(list["tools"].([]any)) < 20 {
		t.Fatalf("only %d tools", len(list["tools"].([]any)))
	}

	// Preview first: nothing changes, nobody is asked.
	text, isErr := s.tool("route_domain", map[string]any{"domain": "https://chat.example.ai/path", "target": "香港 01", "dry_run": true})
	if isErr {
		t.Fatal(text)
	}
	var dry RouteResult
	if err := json.Unmarshal([]byte(text), &dry); err != nil || dry.Applied || len(dry.Diff) == 0 || !strings.HasPrefix(dry.Diff[0], "+ DOMAIN-SUFFIX,chat.example.ai,香港 01") {
		t.Fatalf("dry run: %s", text)
	}
	if asked != 0 {
		t.Fatal("dry run asked for consent")
	}

	// Apply: asks once, adds the rule, verifies it.
	text, isErr = s.tool("route_domain", map[string]any{"domain": "chat.example.ai", "target": "香港 01"})
	if isErr {
		t.Fatal(text)
	}
	var res RouteResult
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Applied || !res.Verified || res.Exit != "香港 01" || res.Delay == nil {
		t.Fatalf("route_domain: %s", text)
	}
	if asked != 1 {
		t.Fatalf("asked %d times", asked)
	}
	rules := srv.Fake.CustomRules()
	if len(rules) == 0 || rules[0].Payload != "chat.example.ai" || rules[0].Target != "香港 01" {
		t.Fatalf("custom rules = %+v", rules)
	}

	// The other composites.
	if text, isErr = s.tool("diagnose_site", map[string]any{"host": "chat.example.ai"}); isErr || !strings.Contains(text, "香港 01") {
		t.Fatalf("diagnose_site: %s", text)
	}
	if text, isErr = s.tool("switch_node", map[string]any{"group": "节点选择"}); isErr || !strings.Contains(text, `"selected"`) {
		t.Fatalf("switch_node: %s", text)
	}

	// Undo twice: the selection, then the rule.
	if text, isErr = s.tool("undo", nil); isErr || !strings.Contains(text, "SelectProxy") {
		t.Fatalf("undo: %s", text)
	}
	if text, isErr = s.tool("undo", nil); isErr || !strings.Contains(text, "SaveCustomRule") {
		t.Fatalf("undo: %s", text)
	}
	for _, r := range srv.Fake.CustomRules() {
		if r.Payload == "chat.example.ai" {
			t.Fatal("undo left the rule")
		}
	}

	// Dangerous methods are not tools, and calling them by name fails.
	if text, isErr = s.tool("status", nil); isErr || !strings.Contains(text, "Core") {
		t.Fatalf("status: %s", text)
	}
}
