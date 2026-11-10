package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lanflow/internal/model"
)

func TestExportSettingsExcludesSecretsByDefault(t *testing.T) {
	h := newHarness(t, nil)
	set := h.s.Settings()
	set.CorePath = "/opt/mihomo"
	set.MixedPort = 7899
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SaveOverride("global", "log-level: warning\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.SaveCustomRule(model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: "openai.com", Target: "DIRECT"}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "lanflow.json")
	if err := h.s.ExportSettings(path, false); err != nil {
		t.Fatalf("ExportSettings: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "example.com/sub") {
		t.Fatal("subscription URL must not be exported by default")
	}
	var ex model.SettingsExport
	if err := json.Unmarshal(data, &ex); err != nil {
		t.Fatal(err)
	}
	if ex.App != "lanflow" || ex.Format != 1 || ex.Settings == nil || ex.Overrides == nil {
		t.Fatalf("bad export header: %+v", ex)
	}
	if ex.Settings.CorePath != "" || ex.Settings.MixedPort != 7899 {
		t.Fatalf("settings not exported as expected: %+v", ex.Settings)
	}
	if ex.Overrides.Global != "log-level: warning\n" || len(ex.CustomRules) != 1 || ex.CustomRules[0].Payload != "openai.com" {
		t.Fatalf("overrides/rules missing: %+v %+v", ex.Overrides, ex.CustomRules)
	}
	if len(ex.Subscriptions) != 0 {
		t.Fatal("subscriptions exported without opt-in")
	}

	if err := h.s.ExportSettings(path, true); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &ex)
	if len(ex.Subscriptions) != 1 || ex.Subscriptions[0].URL != "https://example.com/sub" || ex.Subscriptions[0].Name != "测试机场" {
		t.Fatalf("subscriptions not exported with opt-in: %+v", ex.Subscriptions)
	}
}

func TestImportSettingsAppliesAndKeepsLocalChoices(t *testing.T) {
	src := newHarness(t, nil)
	set := src.s.Settings()
	set.MixedPort = 7899
	set.AllowLAN = true
	set.LogLevel = "warning"
	if err := src.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	_ = src.s.SaveOverride("global", "log-level: warning\n")
	if _, err := src.s.SaveCustomRule(model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: "openai.com", Target: "DIRECT"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.json")
	if err := src.s.ExportSettings(path, false); err != nil {
		t.Fatal(err)
	}

	dst := newHarness(t, nil)
	dset := dst.s.Settings()
	dset.CorePath = "/local/mihomo"
	_ = dst.s.SaveSettings(dset)
	// an existing identical rule is not duplicated
	if _, err := dst.s.SaveCustomRule(model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: "openai.com", Target: "DIRECT"}); err != nil {
		t.Fatal(err)
	}
	summary, err := dst.s.ImportSettings(path)
	if err != nil {
		t.Fatalf("ImportSettings: %v", err)
	}
	if !strings.Contains(summary, "设置") || !strings.Contains(summary, "全局覆写") {
		t.Fatalf("summary = %q", summary)
	}
	got := dst.s.Settings()
	if got.MixedPort != 7899 || !got.AllowLAN || got.LogLevel != "warning" {
		t.Fatalf("settings not applied: %+v", got)
	}
	if got.CorePath != "/local/mihomo" {
		t.Fatalf("CorePath must stay local, got %q", got.CorePath)
	}
	if dst.s.Overrides().Global != "log-level: warning\n" {
		t.Fatalf("override not applied: %q", dst.s.Overrides().Global)
	}
	if rules := dst.s.CustomRules(); len(rules) != 1 {
		t.Fatalf("rules should be merged without duplicates: %+v", rules)
	}
	// importing again changes nothing
	summary, err = dst.s.ImportSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "设置") {
		t.Fatalf("second import summary = %q", summary)
	}
}

func TestImportSettingsRejectsBadFiles(t *testing.T) {
	h := newHarness(t, nil)
	dir := t.TempDir()
	cases := map[string]string{
		"garbage.json": "not json",
		"other.json":   `{"app":"other","format":1,"settings":{}}`,
		"future.json":  `{"app":"lanflow","format":99,"settings":{}}`,
		"empty.json":   `{"app":"lanflow","format":1}`,
		"badrule.json": `{"app":"lanflow","format":1,"customRules":[{"Type":"NOPE","Payload":"x","Target":"DIRECT"}]}`,
		"badsub.json":  `{"app":"lanflow","format":1,"subscriptions":[{"name":"a","url":"ftp://x"}]}`,
	}
	for name, body := range cases {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := h.s.ImportSettings(p); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	if _, err := h.s.ImportSettings(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing file must error")
	}
	// nothing changed
	if h.s.Settings().MixedPort != 7890 || len(h.s.CustomRules()) != 0 {
		t.Fatal("rejected imports must not change state")
	}
}

func TestImportSettingsReportsPartialFailure(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.taken[7899] = true })
	body := `{"app":"lanflow","format":1,"settings":{"MixedPort":7899},"customRules":[{"Type":"DOMAIN","Payload":"a.com","Target":"DIRECT"}]}`
	p := filepath.Join(t.TempDir(), "partial.json")
	_ = os.WriteFile(p, []byte(body), 0o600)
	summary, err := h.s.ImportSettings(p)
	if err != nil {
		t.Fatalf("partial import should still succeed: %v", err)
	}
	if !strings.Contains(summary, "1 条自定义规则") || !strings.Contains(summary, "未导入") || !strings.Contains(summary, "设置") {
		t.Fatalf("summary = %q", summary)
	}
	if h.s.Settings().MixedPort != 7890 {
		t.Fatal("taken port must not be applied")
	}
}
