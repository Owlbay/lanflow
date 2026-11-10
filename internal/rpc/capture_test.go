package rpc_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"lanflow/internal/rpc"
)

// docs/capture.md §10: agents get summaries and structure, never values;
// reading captured traffic and acting are kept apart per connection;
// switching decryption on and the root are out of reach for MCP.
func TestCaptureForAgents(t *testing.T) {
	h := start(t, func(ctx context.Context, c rpc.Consent) (rpc.Answer, error) { return rpc.AnswerOnce, nil })
	h.fake.CaptureStatus() // sample data
	m := h.dial(t, "mcp:claude", false)
	ctx := context.Background()

	var st json.RawMessage
	if err := m.Conn().CallAPI(ctx, "CaptureStatus", nil, &st); err != nil {
		t.Fatal(err)
	}
	var flows json.RawMessage
	if err := m.Conn().CallAPI(ctx, "CaptureFlows", nil, &flows); err != nil {
		t.Fatal(err)
	}
	s := string(flows)
	if !strings.Contains(s, "Untrusted data") || strings.Contains(s, "abcdef0123456789") || !strings.Contains(s, "api.github.com") {
		t.Fatalf("summaries: %s", s)
	}
	var det json.RawMessage
	if err := m.Conn().CallAPI(ctx, "CaptureFlow", []any{3}, &det); err != nil {
		t.Fatal(err)
	}
	s = string(det)
	for _, secret := range []string{"hunter2hunter2", "ghp_abcdefghij", "0123456789abcdef", "eyJhbGciOiJIUzI1NiJ9", "octo"} {
		if strings.Contains(s, secret) {
			t.Fatalf("value %q reached the agent: %s", secret, s)
		}
	}
	if !strings.Contains(s, `"password":"string(14)"`) && !strings.Contains(s, `"password": "string(14)"`) {
		t.Fatalf("no structure: %s", s)
	}
	// the same connection can no longer act
	if err := m.Conn().CallAPI(ctx, "ClearCaptureFlows", nil); err == nil || code(err) != rpc.CodeDenied {
		t.Fatalf("write after reading capture: %v", err)
	}
	// a fresh connection that acted cannot read
	w := h.dial(t, "mcp:claude", false)
	if err := w.Conn().CallAPI(ctx, "ClearCaptureFlows", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Conn().CallAPI(ctx, "CaptureFlows", nil, &flows); err == nil || code(err) != rpc.CodeDenied {
		t.Fatalf("capture read after a write: %v", err)
	}
	// MCP never turns decryption on or touches the root
	for _, method := range []string{"SetCapture", "InstallCACert", "SaveCaptureSettings", "AddCaptureDomain"} {
		c := h.dial(t, "mcp:claude", false)
		var args []any
		switch method {
		case "SetCapture":
			args = []any{true}
		case "AddCaptureDomain":
			args = []any{"x.example.com", true}
		case "SaveCaptureSettings":
			args = []any{map[string]any{}}
		}
		if err := c.Conn().CallAPI(ctx, method, args); err == nil || code(err) != rpc.CodeDenied {
			t.Fatalf("%s for MCP: %v", method, err)
		}
	}
	if h.fake.CaptureStatus().Enabled {
		t.Fatal("an agent turned decryption on")
	}
}
