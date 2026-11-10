package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/model"
)

// coreServer serves fake release assets: every path under
// /<version>/<asset> returns the archive built from body for this platform.
func coreServer(t *testing.T, body []byte, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	asset, isZip, err := coreAsset(runtime.GOOS, runtime.GOARCH, "vX")
	if err != nil {
		t.Skip(err)
	}
	var payload []byte
	if isZip {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("mihomo-windows-amd64.exe")
		_, _ = w.Write(body)
		_ = zw.Close()
		payload = buf.Bytes()
	} else {
		payload = gzipBytes(body)
	}
	suffix := strings.TrimPrefix(asset, "mihomo-"+runtime.GOOS+"-"+runtime.GOARCH+"-vX")
	// The fake archive is what every version "publishes": its SHA-256 is
	// the expected one for any asset (coresums_test.go covers the lookup).
	sum := sha256.Sum256(payload)
	trustDigest(t, hex.EncodeToString(sum[:]))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasSuffix(r.URL.Path, suffix) || !strings.Contains(r.URL.Path, "mihomo-") {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// trustDigest makes sum the expected SHA-256 of every core asset for the
// test.
func trustDigest(t *testing.T, sum string) {
	t.Helper()
	prev := coreDigest
	coreDigest = func(*Service, context.Context, string, string, string) (string, error) { return sum, nil }
	t.Cleanup(func() { coreDigest = prev })
}

// useCoreServer points downloads at srv for the test.
func useCoreServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prev := coreReleaseBase
	coreReleaseBase = srv.URL + "/"
	t.Cleanup(func() { coreReleaseBase = prev })
}

// realFind makes FindBinary behave like the real one for the managed path:
// a custom path is accepted when the file exists, "" means missing.
func realFind(custom string) (string, error) {
	if custom == "" {
		return "", errors.New("missing")
	}
	if _, err := os.Stat(custom); err != nil {
		return "", err
	}
	return custom, nil
}

func TestDownloadCoreInstallsAndStarts(t *testing.T) {
	body := []byte("#!/bin/sh\necho fake mihomo\n")
	srv, hits := coreServer(t, body, http.StatusOK)
	useCoreServer(t, srv)

	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	if h.s.Status().Core != model.CoreMissing {
		t.Fatalf("precondition: core should be missing, got %v", h.s.Status().Core)
	}
	var mu sync.Mutex
	var topics []Topic
	off := h.s.OnChange(func(tp Topic) { mu.Lock(); topics = append(topics, tp); mu.Unlock() })
	defer off()

	if err := h.s.DownloadCore(""); err != nil {
		t.Fatalf("DownloadCore: %v", err)
	}
	managed := h.s.coreVersionPath(pinnedCoreVersion)
	got, err := os.ReadFile(managed)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("managed core not written: %v (%q)", err, got)
	}
	if set := h.s.Settings(); set.CoreVersion != pinnedCoreVersion {
		t.Fatalf("CoreVersion = %q, want %q", set.CoreVersion, pinnedCoreVersion)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(managed); st.Mode().Perm()&0o100 == 0 {
			t.Fatalf("core not executable: %v", st.Mode())
		}
	}
	if set := h.s.Settings(); set.CorePath != managed {
		t.Fatalf("CorePath = %q, want %q", set.CorePath, managed)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected one direct download, got %d", hits.Load())
	}
	if st := h.s.Status(); st.Core != model.CoreRunning {
		t.Fatalf("core should run after download: %+v", st)
	}
	dl := h.s.CoreDownload()
	if dl.Active || dl.Done != pinnedCoreVersion || dl.Error != "" || dl.Bytes == 0 && dl.Total == 0 {
		t.Fatalf("unexpected progress record: %+v", dl)
	}
	// the emitter coalesces a topic to one callback per 100 ms window and a
	// trailing one, so at least two TopicCoreDownload callbacks arrive
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, tp := range topics {
			if tp == TopicCoreDownload {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(time.Second)
	for count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := count(); n < 2 {
		t.Fatalf("expected progress notifications, got %d", n)
	}
	for _, leftover := range []string{managed + ".download", managed + ".new"} {
		if _, err := os.Stat(leftover); err == nil {
			t.Fatalf("temp file left behind: %s", leftover)
		}
	}
}

func TestDownloadCoreRejectsWrongVersion(t *testing.T) {
	srv, _ := coreServer(t, []byte("bin"), http.StatusOK)
	useCoreServer(t, srv)
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	err := h.s.DownloadCore("v9.9.9")
	if err == nil || !strings.Contains(err.Error(), "不符") {
		t.Fatalf("expected version mismatch error, got %v", err)
	}
	if _, serr := os.Stat(h.s.coreVersionDir("v9.9.9")); serr == nil {
		t.Fatal("rejected core must not be installed")
	}
	if h.s.Settings().CorePath != "" {
		t.Fatal("CorePath must be unchanged")
	}
	if dl := h.s.CoreDownload(); dl.Active || dl.Error == "" || dl.Done != "" {
		t.Fatalf("progress should record the error: %+v", dl)
	}
	if h.mgr.starts != 0 {
		t.Fatalf("core must not be started, starts=%d", h.mgr.starts)
	}
}

func TestDownloadCoreHTTPErrorWhileStopped(t *testing.T) {
	srv, _ := coreServer(t, nil, http.StatusNotFound)
	useCoreServer(t, srv)
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	err := h.s.DownloadCore("")
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "未运行") {
		t.Fatalf("expected a clear direct-download error, got %v", err)
	}
	if _, serr := os.Stat(h.s.coreVersionPath(pinnedCoreVersion) + ".download"); serr == nil {
		t.Fatal("partial download left behind")
	}
}

func TestDownloadCoreRefusesConcurrentRuns(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	useCoreServer(t, srv)
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	done := make(chan error, 1)
	go func() { done <- h.s.DownloadCore("") }()
	deadline := time.Now().Add(2 * time.Second)
	for !h.s.CoreDownload().Active && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := h.s.DownloadCore(""); err == nil || !strings.Contains(err.Error(), "正在下载") {
		t.Fatalf("second call should be refused, got %v", err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("first call should fail with 404")
	}
}

// installOldCore puts an installed version "v1.19.32" in place and makes
// it the active core.
func installOldCore(t *testing.T, h *harness) string {
	t.Helper()
	old := h.s.coreVersionPath("v1.19.32")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old core"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = writeCoreMeta(filepath.Dir(old), coreMeta{Version: "v1.19.32", Channel: model.CoreChannelStable, Source: "test"})
	set := h.s.Settings()
	set.CorePath, set.CoreVersion = old, "v1.19.32"
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.s.refreshCores()
	return old
}

func TestDownloadCoreUpdatesRunningCoreAndKeepsOld(t *testing.T) {
	srv, hits := coreServer(t, []byte("new core"), http.StatusOK)
	useCoreServer(t, srv)
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	old := installOldCore(t, h)
	if err := h.s.StartCore(); err != nil {
		t.Fatalf("StartCore: %v", err)
	}
	stops := h.mgr.stops
	sets, clears := h.sys.sets, h.sys.clears

	if err := h.s.DownloadCore("v1.19.33"); err != nil {
		t.Fatalf("DownloadCore: %v", err)
	}
	// the harness's "via proxy" client is a plain client, so one request suffices
	if hits.Load() < 1 {
		t.Fatalf("no download request made")
	}
	managed := h.s.coreVersionPath("v1.19.33")
	if got, _ := os.ReadFile(managed); string(got) != "new core" {
		t.Fatalf("managed core = %q", got)
	}
	if got, _ := os.ReadFile(old); string(got) != "old core" {
		t.Fatalf("old core not kept: %q", got)
	}
	if h.mgr.stops != stops+1 || !h.mgr.Running() {
		t.Fatalf("core should have been stopped once and restarted: stops=%d running=%v", h.mgr.stops, h.mgr.Running())
	}
	if spec := h.mgr.specs[len(h.mgr.specs)-1]; spec.Bin != managed || spec.Config != h.s.runtime {
		t.Fatalf("new core should start on the same runtime config: %+v", spec)
	}
	if h.sys.sets != sets || h.sys.clears != clears {
		t.Fatal("the system proxy must not be touched by a core switch")
	}
	// -t ran against the runtime config with the new binary before the swap
	found := false
	for _, v := range h.core.validated {
		if v == h.s.runtime {
			found = true
		}
	}
	if !found {
		t.Fatalf("new core was not validated against runtime.yaml: %v", h.core.validated)
	}
	if set := h.s.Settings(); set.CorePath != managed || set.CoreVersion != "v1.19.33" {
		t.Fatalf("settings not switched: %+v", set)
	}
}

func TestDownloadCoreRollsBackWhenStartFails(t *testing.T) {
	srv, _ := coreServer(t, []byte("new core"), http.StatusOK)
	useCoreServer(t, srv)
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	managed := installOldCore(t, h)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	// the new binary "fails" to start once: fail every Start of a spec
	// whose binary content is the new core
	h.mgr.mu.Lock()
	h.mgr.startErr = errors.New("boom")
	h.mgr.mu.Unlock()

	err := h.s.DownloadCore("")
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("expected rollback error, got %v", err)
	}
	if got, _ := os.ReadFile(managed); string(got) != "old core" {
		t.Fatalf("old core not restored: %q", got)
	}
	if set := h.s.Settings(); set.CorePath != managed || set.CoreVersion != "v1.19.32" {
		t.Fatalf("settings not rolled back: %q %q", set.CorePath, set.CoreVersion)
	}
}

func TestDownloadCoreReregistersHelper(t *testing.T) {
	srv, _ := coreServer(t, []byte("new core"), http.StatusOK)
	useCoreServer(t, srv)
	h := newHarness(t, func(h *harness) {
		h.core.findFn = realFind
		h.helper.installed = true
	})
	h.s.refreshHelper()
	if err := h.s.DownloadCore(""); err != nil {
		t.Fatalf("DownloadCore: %v", err)
	}
	h.helper.mu.Lock()
	defer h.helper.mu.Unlock()
	if len(h.helper.installs) != 1 || h.helper.installs[0].CoreBin != h.s.coreVersionPath(pinnedCoreVersion) {
		t.Fatalf("helper should be re-installed with the new core: %+v", h.helper.installs)
	}
}

func TestDownloadCoreHelperFailureRollsBack(t *testing.T) {
	srv, _ := coreServer(t, []byte("new core"), http.StatusOK)
	useCoreServer(t, srv)
	h := newHarness(t, func(h *harness) {
		h.core.findFn = realFind
		h.helper.installed = true
		h.helper.installErr = errors.New("用户取消了授权")
	})
	h.s.refreshHelper()
	err := h.s.DownloadCore("")
	if err == nil || !strings.Contains(err.Error(), "权限组件") {
		t.Fatalf("expected helper error, got %v", err)
	}
	// the download is kept as an installed version; only the switch failed
	if _, serr := os.Stat(h.s.coreVersionPath(pinnedCoreVersion)); serr != nil {
		t.Fatal("installed core should stay on disk")
	}
	if set := h.s.Settings(); set.CorePath != "" || set.CoreVersion != "" {
		t.Fatalf("settings should be restored, got %q %q", set.CorePath, set.CoreVersion)
	}
}

func TestCoreAssetAndExtract(t *testing.T) {
	name, isZip, err := coreAsset("windows", "amd64", "v1.19.33")
	if err != nil || !isZip || name != "mihomo-windows-amd64-v1.19.33.zip" {
		t.Fatalf("windows asset: %q %v %v", name, isZip, err)
	}
	name, isZip, err = coreAsset("darwin", "arm64", "v1.19.33")
	if err != nil || isZip || name != "mihomo-darwin-arm64-v1.19.33.gz" {
		t.Fatalf("darwin asset: %q %v %v", name, isZip, err)
	}
	if _, _, err := coreAsset("plan9", "386", "v1"); err == nil {
		t.Fatal("unsupported platform must error")
	}
	if normalizeVersion(" 1.19.33 ") != "v1.19.33" || normalizeVersion("v1.2.3") != "v1.2.3" {
		t.Fatal("normalizeVersion")
	}

	dir := t.TempDir()
	// zip with a nested exe
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("dir/mihomo-windows-amd64.exe")
	_, _ = w.Write([]byte("exe"))
	_ = zw.Close()
	zipPath := filepath.Join(dir, "a.zip")
	_ = os.WriteFile(zipPath, buf.Bytes(), 0o600)
	out := filepath.Join(dir, "out.exe")
	if err := extractCore(zipPath, out, true); err != nil {
		t.Fatalf("extract zip: %v", err)
	}
	if got, _ := os.ReadFile(out); string(got) != "exe" {
		t.Fatalf("zip content = %q", got)
	}
	// zip without the binary
	buf.Reset()
	zw = zip.NewWriter(&buf)
	w, _ = zw.Create("readme.txt")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	_ = os.WriteFile(zipPath, buf.Bytes(), 0o600)
	if err := extractCore(zipPath, out, true); err == nil {
		t.Fatal("zip without mihomo*.exe must fail")
	}
	// gz
	gzPath := filepath.Join(dir, "a.gz")
	_ = os.WriteFile(gzPath, gzipBytes([]byte("elf")), 0o600)
	if err := extractCore(gzPath, out, false); err != nil {
		t.Fatalf("extract gz: %v", err)
	}
	if got, _ := os.ReadFile(out); string(got) != "elf" {
		t.Fatalf("gz content = %q", got)
	}
	_ = os.WriteFile(gzPath, []byte("not gzip"), 0o600)
	if err := extractCore(gzPath, out, false); err == nil {
		t.Fatal("bad gzip must fail")
	}
}
