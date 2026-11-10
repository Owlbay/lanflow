package app

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secretSub = `proxies:
  - name: 香港 01
    type: vmess
    server: hk.secret-airport.example
    port: 443
    uuid: 0b9c5d3e-aaaa-bbbb-cccc-123456789abc
    ws-opts: {path: /ray, headers: {Host: cdn.secret-airport.example}}
  - name: 日本 02
    type: ss
    server: 203.0.113.9
    port: 8388
    cipher: aes-128-gcm
    password: s3cretPass
proxy-providers:
  extra:
    type: http
    url: https://sub.secret-airport.example/api/v1/client/subscribe?token=tok123
proxy-groups:
  - {name: 节点选择, type: select, proxies: [香港 01, 日本 02]}
rules:
  - MATCH,节点选择
`

func TestAppLogRecordsActions(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	h.s.notify(Notice{Kind: "error", Title: "订阅更新失败", Detail: "https://a.example/s?token=abc 超时"})
	h.mgr.startErr = errors.New("boom")
	_ = h.s.RestartCore()

	var all []string
	for _, e := range h.s.AppLogs() {
		all = append(all, e.Level+" "+e.Payload)
	}
	text := strings.Join(all, "\n")
	for _, want := range []string{"info start core: ok", "info system proxy on: ok", "error notice [error] 订阅更新失败", "error restart core: failed"} {
		if !strings.Contains(text, want) {
			t.Errorf("app log lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "token=abc") {
		t.Error("token not redacted")
	}
	data, err := os.ReadFile(filepath.Join(h.dir, "lanflow.log"))
	if err != nil || !strings.Contains(string(data), "system proxy on: ok") {
		t.Fatalf("lanflow.log: %v\n%s", err, data)
	}
	h.s.ClearAppLogs()
	if len(h.s.AppLogs()) != 0 {
		t.Fatal("clear")
	}
}

func TestExportDiagnostics(t *testing.T) {
	h := newHarness(t, nil)
	id := h.s.Status().CurrentSubscription
	if err := h.store.SetContent(id, []byte(secretSub)); err != nil {
		t.Fatal(err)
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(h.dir, "mihomo.log"), []byte("level=info msg=\"dial https://sub.secret-airport.example/x?token=tok123\"\n"), 0o600)

	out := filepath.Join(t.TempDir(), "diag.zip")
	if err := h.s.ExportDiagnostics(out); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	for _, name := range []string{"README.txt", "system.txt", "config/settings.json", "config/runtime.yaml", "logs/lanflow.log", "logs/mihomo.log", "network/routes.txt"} {
		if _, ok := files[name]; !ok {
			t.Errorf("zip lacks %s (has %v)", name, keys(files))
		}
	}
	for name, body := range files {
		for _, secret := range []string{"hk.secret-airport.example", "203.0.113.9", "0b9c5d3e", "s3cretPass", "tok123", "zzz9", "cdn.secret-airport.example"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s leaks %q", name, secret)
			}
		}
	}
	rt := files["config/runtime.yaml"]
	for _, keep := range []string{"香港 01", "type: vmess", "MATCH,节点选择", "sub.secret-airport.example/***"} {
		if !strings.Contains(rt, keep) {
			t.Errorf("runtime.yaml lost %q:\n%s", keep, rt)
		}
	}
	if !strings.Contains(files["system.txt"], "core:") {
		t.Errorf("system.txt:\n%s", files["system.txt"])
	}
	if err := h.s.ExportDiagnostics(""); err == nil {
		t.Fatal("empty path accepted")
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
