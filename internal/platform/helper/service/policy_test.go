//go:build !windows

package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"lanflow/internal/platform/helper/protocol"
)

// fixture builds a data dir with a work dir, config and a fake "core"
// binary, plus a policy for the current user.
type fixture struct {
	dataDir, core, source string
	spec                  protocol.Spec
	policy                *Policy
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	work := filepath.Join(dataDir, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dataDir, "runtime.yaml")
	if err := os.WriteFile(config, []byte("mixed-port: 7890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "bundled-mihomo")
	if err := os.WriteFile(source, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	core := filepath.Join(root, "privileged-mihomo")
	if err := copyFile(source, core, 0o755); err != nil {
		t.Fatal(err)
	}
	sum, err := FileSHA256(core)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(dataDir)
	cfg := Config{Version: ConfigVersion, Core: core, CoreSource: source, SHA256: sum, DataDir: resolved, UID: strconv.Itoa(os.Getuid())}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dataDir, "ctl.sock")
	return &fixture{
		dataDir: dataDir, core: core, source: source,
		spec:   protocol.Spec{Bin: core, WorkDir: work, Config: config, Controller: sock, LogFile: filepath.Join(dataDir, "core.log")},
		policy: NewPolicy(cfg),
	}
}

func TestValidateAccepts(t *testing.T) {
	f := newFixture(t)
	out, err := f.policy.Validate(f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if out.Bin != f.core {
		t.Fatalf("Bin = %s", out.Bin)
	}
	resolvedWork, _ := filepath.EvalSymlinks(f.spec.WorkDir)
	if out.WorkDir != resolvedWork {
		t.Fatalf("WorkDir = %s, want %s", out.WorkDir, resolvedWork)
	}
	if !strings.HasSuffix(out.LogFile, "/core.log") {
		t.Fatalf("LogFile = %s", out.LogFile)
	}

	// The bundled source is accepted as long as it is byte-identical.
	s := f.spec
	s.Bin = f.source
	out, err = f.policy.Validate(s)
	if err != nil || out.Bin != f.core {
		t.Fatalf("source bin: %v %s", err, out.Bin)
	}

	// Allowed env passes through.
	s = f.spec
	s.Env = []string{"SAFE_PATHS=/x", "CLASH_FOO=1"}
	if out, err = f.policy.Validate(s); err != nil || len(out.Env) != 2 {
		t.Fatalf("env: %v %v", err, out.Env)
	}
}

func TestValidateRefusesBinary(t *testing.T) {
	f := newFixture(t)

	s := f.spec
	s.Bin = filepath.Join(filepath.Dir(f.core), "other")
	if _, err := f.policy.Validate(s); err == nil || !strings.Contains(err.Error(), "未登记") {
		t.Fatalf("foreign bin: %v", err)
	}

	// Updated bundled source: hash differs → refused.
	if err := os.WriteFile(f.source, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s = f.spec
	s.Bin = f.source
	if _, err := f.policy.Validate(s); err == nil || !strings.Contains(err.Error(), "重新安装") {
		t.Fatalf("changed source: %v", err)
	}

	// Tampered privileged copy → refused even by its own path.
	if err := os.WriteFile(f.core, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.policy.Validate(f.spec); err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("tampered core: %v", err)
	}
}

func TestValidateRefusesPaths(t *testing.T) {
	f := newFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "evil.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]func(s *protocol.Spec){
		"workdir outside":        func(s *protocol.Spec) { s.WorkDir = outside },
		"config outside":         func(s *protocol.Spec) { s.Config = filepath.Join(outside, "evil.yaml") },
		"log outside":            func(s *protocol.Spec) { s.LogFile = filepath.Join(outside, "core.log") },
		"dotdot escape":          func(s *protocol.Spec) { s.LogFile = filepath.Join(f.dataDir, "..", "escape.log") },
		"relative":               func(s *protocol.Spec) { s.Config = "runtime.yaml" },
		"empty":                  func(s *protocol.Spec) { s.WorkDir = "" },
		"config is a dir":        func(s *protocol.Spec) { s.Config = s.WorkDir },
		"workdir missing":        func(s *protocol.Spec) { s.WorkDir = filepath.Join(f.dataDir, "nope") },
		"controller relative":    func(s *protocol.Spec) { s.Controller = "ctl.sock" },
		"controller dir missing": func(s *protocol.Spec) { s.Controller = filepath.Join(f.dataDir, "nope", "ctl.sock") },
		"controller is a file":   func(s *protocol.Spec) { s.Controller = s.Config },
		"env dyld":               func(s *protocol.Spec) { s.Env = []string{"DYLD_INSERT_LIBRARIES=/tmp/x.dylib"} },
		"env post-up":            func(s *protocol.Spec) { s.Env = []string{"CLASH_POST_UP=/tmp/x.sh"} },
		"env override":           func(s *protocol.Spec) { s.Env = []string{"CLASH_OVERRIDE_EXTERNAL_CONTROLLER=0.0.0.0:9090"} },
		"env malformed":          func(s *protocol.Spec) { s.Env = []string{"NOEQUALS"} },
	}
	for name, mutate := range cases {
		s := f.spec
		mutate(&s)
		if _, err := f.policy.Validate(s); err == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
	}
}

func TestValidateRefusesSymlinkEscapes(t *testing.T) {
	f := newFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "evil.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Directory symlink inside the data dir pointing outside.
	link := filepath.Join(f.dataDir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	s := f.spec
	s.WorkDir = link
	if _, err := f.policy.Validate(s); err == nil {
		t.Error("workdir via directory symlink accepted")
	}
	s = f.spec
	s.LogFile = filepath.Join(link, "core.log") // not yet existing file under an escaping dir
	if _, err := f.policy.Validate(s); err == nil {
		t.Error("log file under directory symlink accepted")
	}

	// File symlink inside the data dir pointing to an outside file.
	flink := filepath.Join(f.dataDir, "cfg.yaml")
	if err := os.Symlink(filepath.Join(outside, "evil.yaml"), flink); err != nil {
		t.Fatal(err)
	}
	s = f.spec
	s.Config = flink
	if _, err := f.policy.Validate(s); err == nil {
		t.Error("config via file symlink accepted")
	}

	// Controller whose parent directory is not owned by the user.
	s = f.spec
	s.Controller = "/tmp/lanflow-test-" + strconv.Itoa(os.Getpid()) + ".sock"
	if _, err := f.policy.Validate(s); err == nil || !strings.Contains(err.Error(), "属于") {
		t.Errorf("controller in /tmp: %v", err)
	}
	// Controller in a user-owned temp dir (what core.ControllerAddress uses) is fine.
	s.Controller = filepath.Join(t.TempDir(), "c.sock")
	if _, err := f.policy.Validate(s); err != nil {
		t.Errorf("controller in user temp dir: %v", err)
	}
}

func TestFilterEnv(t *testing.T) {
	ok, err := FilterEnv([]string{"SAFE_PATHS=/a:/b", "SKIP_SAFE_PATH_CHECK=1", "CLASH_X=1", "GOGC=50"})
	if err != nil || len(ok) != 4 {
		t.Fatalf("%v %v", ok, err)
	}
	for _, bad := range []string{"PATH=/x", "LD_PRELOAD=/x", "GODEBUG=x", "CLASH_HOME_DIR=/", "CLASH_CONFIG_STRING=x", "HOME=/root"} {
		if _, err := FilterEnv([]string{bad}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.json")
	c := Config{Version: ConfigVersion, Core: "/c", CoreSource: "/s", SHA256: strings.Repeat("ab", 32), DataDir: "/d", UID: "501"}
	if err := SaveConfig(p, c, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(p)
	if err != nil || got != c {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing config accepted")
	}
	bad := c
	bad.SHA256 = "short"
	if err := SaveConfig(p, bad, 0o644); err == nil {
		t.Fatal("invalid config saved")
	}
}
