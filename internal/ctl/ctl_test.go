package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"lanflow/internal/app/remote"
	"lanflow/internal/rpc"
	"lanflow/internal/rpc/rpctest"
)

func run(t *testing.T, s *rpctest.Server, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(args, Env{Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(""), Version: "test",
		Dial: func(_ context.Context, reveal bool) (*remote.Client, error) { return s.Dial("ctl", reveal) }})
	return out.String(), errb.String(), code
}

func TestStatusAndProxies(t *testing.T) {
	s := rpctest.Start(t, nil)
	out, errs, code := run(t, s, "status")
	if code != 0 {
		t.Fatalf("status: %d %s", code, errs)
	}
	var st StatusView
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.Core == "" {
		t.Fatalf("status output %q: %v", out, err)
	}
	if _, errs, code = run(t, s, "proxies", "select", "节点选择", "香港 01"); code != 0 {
		t.Fatalf("select: %s", errs)
	}
	out, _, code = run(t, s, "proxies", "list", "节点选择", "--format", "table")
	if code != 0 || !strings.Contains(out, "* 香港 01") {
		t.Fatalf("table:\n%s", out)
	}
	if _, errs, code = run(t, s, "mode", "set", "warp"); code != ExitUsage {
		t.Fatalf("bad mode: %d %s", code, errs)
	}
}

func TestConfigDryRunAndUndo(t *testing.T) {
	s := rpctest.Start(t, nil)
	before := s.Fake.Settings().MixedPort
	out, errs, code := run(t, s, "config", "set", "MixedPort", "7999", "--dry-run")
	if code != 0 {
		t.Fatalf("dry run: %s", errs)
	}
	var pv PatchView
	if err := json.Unmarshal([]byte(out), &pv); err != nil || pv.Applied || !strings.Contains(strings.Join(pv.Diff, "\n"), "+   \"MixedPort\": 7999") {
		t.Fatalf("dry run output: %s", out)
	}
	if s.Fake.Settings().MixedPort != before {
		t.Fatal("dry run applied")
	}
	if _, errs, code = run(t, s, "config", "set", "MixedPort", "7999"); code != 0 {
		t.Fatalf("set: %s", errs)
	}
	if s.Fake.Settings().MixedPort != 7999 {
		t.Fatal("not applied")
	}
	out, _, _ = run(t, s, "config", "get", "mixedport")
	if strings.TrimSpace(out) != "7999" {
		t.Fatalf("get = %q", out)
	}
	if out, errs, code = run(t, s, "undo"); code != 0 || !strings.Contains(out, "PatchSettings") {
		t.Fatalf("undo: %s %s", out, errs)
	}
	if s.Fake.Settings().MixedPort != before {
		t.Fatal("undo did not restore the port")
	}
	// rule add --dry-run prints the list with the new rule first.
	out, _, code = run(t, s, "rule", "add", "domain-suffix", "example.org", "DIRECT", "--dry-run")
	if code != 0 || !strings.Contains(out, "+ DOMAIN-SUFFIX,example.org,DIRECT") {
		t.Fatalf("rule dry run: %s", out)
	}
	out, _, _ = run(t, s, "audit")
	if !strings.Contains(out, `"method": "PatchSettings"`) {
		t.Fatalf("audit: %s", out)
	}
}

func TestSensitiveNeedsReveal(t *testing.T) {
	s := rpctest.Start(t, nil)
	_, errs, code := run(t, s, "call", "Connections")
	if code != ExitError || !strings.Contains(errs, "--reveal") {
		t.Fatalf("Connections without --reveal: %d %s", code, errs)
	}
	out, _, _ := run(t, s, "sub", "list")
	if strings.Contains(out, "token") || strings.Contains(out, "subscribe") || strings.Contains(out, "/link/") {
		t.Fatalf("subscription links in sub list: %s", out)
	}
	_ = rpc.LevelRead
}

func TestDiag(t *testing.T) {
	s := rpctest.Start(t, nil)
	out, errs, code := run(t, s, "diag")
	if code != 0 {
		t.Fatalf("diag: %s", errs)
	}
	var rep DiagReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil || len(rep.Findings) == 0 {
		t.Fatalf("diag output: %s (%v)", out, err)
	}
}

func TestNotRunning(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main([]string{"status"}, Env{Stdout: &out, Stderr: &errb,
		Dial: func(context.Context, bool) (*remote.Client, error) { return nil, rpc.ErrNotRunning }})
	if code != ExitNotRunning || !strings.Contains(errb.String(), "没有在运行") {
		t.Fatalf("code %d, %q", code, errb.String())
	}
}

func TestConfigSync(t *testing.T) {
	s := rpctest.Start(t, nil)
	out, errs, code := run(t, s, "config", "sync", "status")
	if code != 0 {
		t.Fatalf("status: %d %s", code, errs)
	}
	var v SyncStatusView
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Mode != "off" || v.State != "off" {
		t.Fatalf("status output %q: %v", out, err)
	}
	if err := s.Fake.SetConfigSync("folder", "/Users/demo/Sync", ""); err != nil {
		t.Fatal(err)
	}
	out, _, _ = run(t, s, "config", "sync", "status", "--format", "table")
	if !strings.Contains(out, "/Users/demo/Sync/lanflow.yaml") {
		t.Fatalf("table:\n%s", out)
	}
	out, errs, code = run(t, s, "config", "sync", "apply", "lanflow.yaml", "--dry-run")
	if code != 0 {
		t.Fatalf("apply: %d %s", code, errs)
	}
	var pv PatchView
	if err := json.Unmarshal([]byte(out), &pv); err != nil || pv.Applied {
		t.Fatalf("apply output %q: %v", out, err)
	}
	if _, _, code = run(t, s, "config", "sync", "frobnicate"); code != ExitUsage {
		t.Fatalf("unknown sub: %d", code)
	}
}
