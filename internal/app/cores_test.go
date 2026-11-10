package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/core"
	"lanflow/internal/model"
)

// ---- release list parsing ----

func TestParseCoreAssetPerPlatform(t *testing.T) {
	cases := []struct {
		name, goos, goarch, want string
		ok                       bool
	}{
		{"mihomo-darwin-arm64-v1.19.33.gz", "darwin", "arm64", "v1.19.33", true},
		{"mihomo-darwin-amd64-v1.19.33.gz", "darwin", "amd64", "v1.19.33", true},
		{"mihomo-linux-amd64-v1.19.33.gz", "linux", "amd64", "v1.19.33", true},
		{"mihomo-windows-amd64-v1.19.33.zip", "windows", "amd64", "v1.19.33", true},
		{"mihomo-windows-arm64-v1.19.33.zip", "windows", "arm64", "v1.19.33", true},
		{"mihomo-darwin-arm64-alpha-5a3a0ef.gz", "darwin", "arm64", "alpha-5a3a0ef", true},
		{"mihomo-windows-amd64-alpha-5a3a0ef.zip", "windows", "amd64", "alpha-5a3a0ef", true},
		{"mihomo-linux-arm64-alpha-5a3a0ef.gz", "linux", "arm64", "alpha-5a3a0ef", true},
		// variants and other platforms
		{"mihomo-darwin-arm64-v1.19.33.gz", "darwin", "amd64", "", false},
		{"mihomo-linux-amd64-v3-v1.19.33.gz", "linux", "amd64", "", false},
		{"mihomo-windows-amd64-compatible-alpha-5a3a0ef.zip", "windows", "amd64", "", false},
		{"mihomo-linux-amd64-go120-alpha-5a3a0ef.gz", "linux", "amd64", "", false},
		{"mihomo-windows-amd64-v1.19.33.gz", "windows", "amd64", "", false}, // windows ships zip
		{"mihomo-darwin-arm64-v1.19.33.zip", "darwin", "arm64", "", false},
		{"mihomo-android-arm64-v8-v1.19.33.gz", "android", "arm64", "", false},
		{"mihomo-darwin-arm64-v1.19.33.gz.sha256", "darwin", "arm64", "", false},
	}
	for _, c := range cases {
		got, ok := parseCoreAsset(c.name, c.goos, c.goarch)
		if ok != c.ok || got != c.want {
			t.Errorf("parseCoreAsset(%q, %s/%s) = %q %v, want %q %v", c.name, c.goos, c.goarch, got, ok, c.want, c.ok)
		}
	}
}

// sampleReleases is a GitHub /releases answer with a prerelease, a draft
// and two stable releases carrying assets for every platform.
func sampleReleases() []ghRelease {
	assets := func(version string) []ghAsset {
		var out []ghAsset
		for _, p := range []string{"darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64", "linux-amd64-v3"} {
			out = append(out, ghAsset{Name: "mihomo-" + p + "-" + version + ".gz", URL: "https://github.com/x/" + p, Size: 19 << 20})
		}
		for _, p := range []string{"windows-amd64", "windows-arm64", "windows-amd64-compatible"} {
			out = append(out, ghAsset{Name: "mihomo-" + p + "-" + version + ".zip", URL: "https://github.com/x/" + p, Size: 20 << 20})
		}
		return out
	}
	day := 24 * time.Hour
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	return []ghRelease{
		{Tag: "Prerelease-Alpha", Prerelease: true, PublishedAt: now.Add(-2 * time.Hour), Body: "alpha", Assets: assets("alpha-5a3a0ef")},
		{Tag: "v1.19.35", Draft: true, PublishedAt: now, Assets: assets("v1.19.35")},
		{Tag: "v1.19.34", PublishedAt: now.Add(-3 * day), Body: "## What's Changed\r\n* fix: dns fallback by @a in https://x\r\n* feat: sniffer\r\n\r\n**Full Changelog**: https://x", Assets: assets("v1.19.34")},
		{Tag: "v1.19.33", PublishedAt: now.Add(-20 * day), Body: "fix: tun", Assets: assets("v1.19.33")},
	}
}

func TestReleasesFromAPIStableAndAlpha(t *testing.T) {
	for _, plat := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		stable := releasesFromAPI(model.CoreChannelStable, sampleReleases(), plat[0], plat[1])
		if len(stable) != 2 || stable[0].Version != "v1.19.34" || stable[1].Version != "v1.19.33" {
			t.Fatalf("%s/%s stable: %+v", plat[0], plat[1], stable)
		}
		ext := ".gz"
		if plat[0] == "windows" {
			ext = ".zip"
		}
		if stable[0].Asset != "mihomo-"+plat[0]+"-"+plat[1]+"-v1.19.34"+ext || stable[0].Tag != "v1.19.34" || stable[0].Channel != model.CoreChannelStable {
			t.Fatalf("%s/%s asset: %+v", plat[0], plat[1], stable[0])
		}
		if stable[0].Notes != "fix: dns fallback by @a in https://x\nfeat: sniffer" {
			t.Fatalf("notes excerpt: %q", stable[0].Notes)
		}
		alpha := releasesFromAPI(model.CoreChannelAlpha, sampleReleases(), plat[0], plat[1])
		if len(alpha) != 1 || alpha[0].Version != "alpha-5a3a0ef" || alpha[0].Tag != coreAlphaTag || alpha[0].Channel != model.CoreChannelAlpha {
			t.Fatalf("%s/%s alpha: %+v", plat[0], plat[1], alpha)
		}
		if alpha[0].Asset != "mihomo-"+plat[0]+"-"+plat[1]+"-alpha-5a3a0ef"+ext {
			t.Fatalf("%s/%s alpha asset: %q", plat[0], plat[1], alpha[0].Asset)
		}
	}
	if u := coreAssetURL(model.CoreChannelAlpha, "alpha-5a3a0ef", "mihomo-darwin-arm64-alpha-5a3a0ef.gz"); u != coreReleaseBase+"Prerelease-Alpha/mihomo-darwin-arm64-alpha-5a3a0ef.gz" {
		t.Fatalf("alpha url: %s", u)
	}
	if u := coreAssetURL(model.CoreChannelStable, "v1.19.34", "mihomo-darwin-arm64-v1.19.34.gz"); u != coreReleaseBase+"v1.19.34/mihomo-darwin-arm64-v1.19.34.gz" {
		t.Fatalf("stable url: %s", u)
	}
}

func TestVersionOrderAndMatch(t *testing.T) {
	if compareSemver("v1.19.34", "v1.19.33") <= 0 || compareSemver("v1.20.0", "v1.19.99") <= 0 || compareSemver("v1.19.33", "v1.19.33") != 0 {
		t.Fatal("compareSemver")
	}
	if !newerVersion("v1.19.34", "v1.19.33") || !newerVersion("v1.19.33", "alpha-abc1234") || newerVersion("alpha-abc1234", "v1.0.0") {
		t.Fatal("newerVersion")
	}
	if !isNewer(model.CoreChannelStable, "v1.19.34", "v1.19.33") || isNewer(model.CoreChannelStable, "v1.19.33", "v1.19.34") || isNewer(model.CoreChannelStable, "v1.19.33", "v1.19.33") {
		t.Fatal("isNewer stable")
	}
	if !isNewer(model.CoreChannelAlpha, "alpha-b", "alpha-a") || isNewer(model.CoreChannelAlpha, "alpha-a", "alpha-a") {
		t.Fatal("isNewer alpha")
	}
	if !versionMatches(model.CoreChannelStable, "v1.19.33", "1.19.33") || versionMatches(model.CoreChannelStable, "v1.19.33", "v1.19.34") {
		t.Fatal("versionMatches stable")
	}
	if !versionMatches(model.CoreChannelAlpha, "alpha-5a3a0ef", "alpha-5a3a0ef1") || versionMatches(model.CoreChannelAlpha, "alpha-5a3a0ef", "v1.19.33") {
		t.Fatal("versionMatches alpha")
	}
	for _, bad := range []string{"", "../x", "v1.19", "latest", "alpha-ZZZ"} {
		if checkVersionName(bad) == nil {
			t.Errorf("checkVersionName(%q) should fail", bad)
		}
	}
}

// ---- layout migration ----

func TestMigrateLegacyCore(t *testing.T) {
	var legacy string
	h := newHarness(t, func(h *harness) {
		h.core.findFn = realFind
		s := &Service{opt: Options{DataDir: h.dir}, workDir: filepath.Join(h.dir, "core")}
		legacy = s.managedCorePath()
		if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacy, []byte("legacy core"), 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(legacy+".old", []byte("older"), 0o700)
		set := defaultSettings()
		set.CorePath = legacy
		if err := saveSettings(h.dir, set); err != nil {
			t.Fatal(err)
		}
	})
	dst := h.s.coreVersionPath("v1.19.33") // the fake core reports v1.19.33
	if got, err := os.ReadFile(dst); err != nil || string(got) != "legacy core" {
		t.Fatalf("core not moved: %v %q", err, got)
	}
	if _, err := os.Stat(legacy); err == nil {
		t.Fatal("legacy file should be gone")
	}
	if _, err := os.Stat(legacy + ".old"); err == nil {
		t.Fatal("legacy .old should be gone")
	}
	m, err := readCoreMeta(filepath.Dir(dst))
	if err != nil || m.Version != "v1.19.33" || m.Channel != model.CoreChannelStable || m.Source != "migrated" || m.SHA256 == "" {
		t.Fatalf("meta: %+v %v", m, err)
	}
	if set := h.s.Settings(); set.CorePath != dst || set.CoreVersion != "v1.19.33" {
		t.Fatalf("settings not repointed: %q %q", set.CorePath, set.CoreVersion)
	}
	list := h.s.InstalledCores()
	var found *model.InstalledCore
	for i := range list {
		if list[i].Version == "v1.19.33" && !list[i].Bundled {
			found = &list[i]
		}
	}
	if found == nil || !found.Active || found.Path != dst || found.Size != int64(len("legacy core")) {
		t.Fatalf("installed list: %+v", list)
	}
	if h.s.Status().Core != model.CoreStopped {
		t.Fatalf("core should resolve after migration: %+v", h.s.Status())
	}
}

// ---- install ----

func TestInstallCoreStoresVersionWithoutSwitching(t *testing.T) {
	body := []byte("#!/bin/sh\necho Mihomo Meta v1.19.33\n")
	srv, hits := coreServer(t, body, http.StatusOK)
	useCoreServer(t, srv)
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })

	if err := h.s.InstallCore(model.CoreChannelAlpha, "v1.19.33"); err == nil || !strings.Contains(err.Error(), "通道") {
		t.Fatalf("stable version in the alpha channel must be refused: %v", err)
	}
	if err := h.s.InstallCore("", "v1.19.33"); err != nil {
		t.Fatalf("InstallCore: %v", err)
	}
	bin := h.s.coreVersionPath("v1.19.33")
	got, err := os.ReadFile(bin)
	if err != nil || string(got) != string(body) {
		t.Fatalf("core not written: %v", err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(bin); st.Mode().Perm()&0o100 == 0 {
			t.Fatalf("core not executable: %v", st.Mode())
		}
	}
	sum := sha256.Sum256(body)
	m, err := readCoreMeta(filepath.Dir(bin))
	if err != nil || m.Version != "v1.19.33" || m.Channel != model.CoreChannelStable || m.SHA256 != hex.EncodeToString(sum[:]) || !strings.Contains(m.Source, "v1.19.33/mihomo-") || m.InstalledAt.IsZero() {
		t.Fatalf("meta: %+v %v", m, err)
	}
	// installed, listed, but not active and nothing started
	if set := h.s.Settings(); set.CorePath != "" || set.CoreVersion != "" {
		t.Fatalf("InstallCore must not switch: %+v", set)
	}
	if h.mgr.starts != 0 {
		t.Fatalf("core must not start, starts=%d", h.mgr.starts)
	}
	var ic *model.InstalledCore
	for _, c := range h.s.InstalledCores() {
		if c.Version == "v1.19.33" && !c.Bundled {
			ic = &c
		}
	}
	if ic == nil || ic.Active || ic.Size != int64(len(body)) || ic.Channel != model.CoreChannelStable {
		t.Fatalf("installed list: %+v", h.s.InstalledCores())
	}
	if dl := h.s.CoreDownload(); dl.Active || dl.Done != "v1.19.33" || dl.Error != "" {
		t.Fatalf("progress: %+v", dl)
	}
	for _, leftover := range []string{bin + ".download", bin + ".new"} {
		if _, err := os.Stat(leftover); err == nil {
			t.Fatalf("temp file left behind: %s", leftover)
		}
	}
	// a second install of the same version is a no-op
	n := hits.Load()
	if err := h.s.InstallCore(model.CoreChannelStable, "1.19.33"); err != nil || hits.Load() != n {
		t.Fatalf("reinstall: %v hits=%d", err, hits.Load()-n)
	}
}

func TestInstallCoreCleansUpOnFailure(t *testing.T) {
	srv, _ := coreServer(t, []byte("bin"), http.StatusOK)
	useCoreServer(t, srv)
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	err := h.s.InstallCore(model.CoreChannelStable, "v9.9.9") // the fake core reports v1.19.33
	if err == nil || !strings.Contains(err.Error(), "不符") {
		t.Fatalf("expected version mismatch, got %v", err)
	}
	if _, serr := os.Stat(h.s.coreVersionDir("v9.9.9")); serr == nil {
		t.Fatal("version directory should be removed")
	}
	if len(h.s.InstalledCores()) != 0 {
		t.Fatalf("nothing should be listed: %+v", h.s.InstalledCores())
	}
	if dl := h.s.CoreDownload(); dl.Active || dl.Error == "" {
		t.Fatalf("progress should record the error: %+v", dl)
	}
}

// ---- use / switch ----

// twoCores installs "v1.19.32" (active) and "v1.19.33" on disk.
func twoCores(t *testing.T, h *harness) (old, newer string) {
	t.Helper()
	old = installOldCore(t, h)
	newer = h.s.coreVersionPath("v1.19.33")
	if err := os.MkdirAll(filepath.Dir(newer), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("new core"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = writeCoreMeta(filepath.Dir(newer), coreMeta{Version: "v1.19.33", Channel: model.CoreChannelStable, Source: "test"})
	h.s.refreshCores()
	return old, newer
}

func TestUseCoreHotSwitchesRunningCore(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	old, newer := twoCores(t, h)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	stops, starts := h.mgr.stops, h.mgr.starts
	sets, clears := h.sys.sets, h.sys.clears
	validated := len(h.core.validated)

	if err := h.s.UseCore("v1.19.33"); err != nil {
		t.Fatalf("UseCore: %v", err)
	}
	if h.mgr.stops != stops+1 || h.mgr.starts != starts+1 || !h.mgr.Running() {
		t.Fatalf("expected one stop and one start: stops=%d starts=%d", h.mgr.stops-stops, h.mgr.starts-starts)
	}
	spec := h.mgr.specs[len(h.mgr.specs)-1]
	if spec.Bin != newer || spec.Config != h.s.runtime || spec.Controller != h.s.ctrlAddr {
		t.Fatalf("new core must start on the same config and controller: %+v", spec)
	}
	if h.sys.sets != sets || h.sys.clears != clears {
		t.Fatal("system proxy must not be touched")
	}
	if st := h.s.Status(); !st.SystemProxy || st.Core != model.CoreRunning {
		t.Fatalf("status after switch: %+v", st)
	}
	// the new binary validated the current runtime config before the stop
	if len(h.core.validated) <= validated || h.core.validated[validated] != h.s.runtime {
		t.Fatalf("new core was not validated first: %v", h.core.validated[validated:])
	}
	if set := h.s.Settings(); set.CorePath != newer || set.CoreVersion != "v1.19.33" {
		t.Fatalf("settings: %+v", set)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("previous version must stay installed")
	}
	active := 0
	for _, c := range h.s.InstalledCores() {
		if c.Active {
			active++
			if c.Version != "v1.19.33" {
				t.Fatalf("active = %+v", c)
			}
		}
	}
	if active != 1 {
		t.Fatalf("exactly one active core expected: %+v", h.s.InstalledCores())
	}
	logged := false
	for _, l := range h.s.AppLogs() {
		if strings.Contains(l.Payload, "core switched: v1.19.32 → v1.19.33") {
			logged = true
		}
	}
	if !logged {
		t.Fatal("switch not recorded in the app log")
	}
	// switching to the version in use is a no-op
	if err := h.s.UseCore("v1.19.33"); err != nil || h.mgr.stops != stops+1 {
		t.Fatalf("no-op switch: %v stops=%d", err, h.mgr.stops)
	}
}

func TestUseCoreRollsBackWhenNewCoreFailsToStart(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	old, newer := twoCores(t, h)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	// the new binary fails to start; the old one starts fine
	h.mgr.mu.Lock()
	h.mgr.startFn = func(spec core.Spec) error {
		if spec.Bin == newer {
			return errors.New("boom")
		}
		return nil
	}
	h.mgr.mu.Unlock()

	err := h.s.UseCore("v1.19.33")
	if err == nil || !strings.Contains(err.Error(), "已恢复并重新启动原内核") {
		t.Fatalf("expected rollback error, got %v", err)
	}
	if set := h.s.Settings(); set.CorePath != old || set.CoreVersion != "v1.19.32" {
		t.Fatalf("settings not rolled back: %q %q", set.CorePath, set.CoreVersion)
	}
	if !h.mgr.Running() || h.mgr.specs[len(h.mgr.specs)-1].Bin != old {
		t.Fatalf("previous core should run again: %+v", h.mgr.specs[len(h.mgr.specs)-1])
	}
	if dl := h.s.CoreDownload(); dl.Active || dl.Error == "" {
		t.Fatalf("progress should record the failure: %+v", dl)
	}
	if _, err := os.Stat(newer); err != nil {
		t.Fatal("the failed version stays installed for another try")
	}
}

func TestUseCoreRefusesInvalidConfigBeforeStopping(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	_, _ = twoCores(t, h)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	stops := h.mgr.stops
	h.core.mu.Lock()
	h.core.validateErr = errors.New("unknown field")
	h.core.mu.Unlock()
	err := h.s.UseCore("v1.19.33")
	if err == nil || !strings.Contains(err.Error(), "已保留现有内核") {
		t.Fatalf("expected validation error, got %v", err)
	}
	if h.mgr.stops != stops || !h.mgr.Running() {
		t.Fatal("the running core must not be stopped when the new one fails -t")
	}
	if h.s.Settings().CoreVersion != "v1.19.32" {
		t.Fatal("settings changed")
	}
}

func TestUseCoreBundledAndTUNHelperOrder(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.core.findFn = func(custom string) (string, error) {
			if custom == "" {
				return filepath.Join(h.dir, "bundled-mihomo"), nil
			}
			return realFind(custom)
		}
		h.helper.installed = true
	})
	_ = os.WriteFile(filepath.Join(h.dir, "bundled-mihomo"), []byte("bundled"), 0o700)
	h.s.refreshHelper()
	old, newer := twoCores(t, h)
	set := h.s.Settings()
	set.TUN = true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if !h.s.Status().TUN {
		t.Fatalf("precondition: TUN should run: %+v", h.s.Status())
	}
	h.helper.mu.Lock()
	h.helper.installs = nil
	h.helper.mu.Unlock()
	stopsBefore := h.mgr.stops
	h.mgr.mu.Lock()
	h.mgr.onStop = func() {
		h.helper.mu.Lock()
		defer h.helper.mu.Unlock()
		if len(h.helper.installs) != 0 {
			t.Error("with a helper-run core, the helper must be re-registered after the stop")
		}
	}
	h.mgr.mu.Unlock()
	if err := h.s.UseCore("v1.19.33"); err != nil {
		t.Fatalf("UseCore: %v", err)
	}
	h.mgr.mu.Lock()
	h.mgr.onStop = nil
	h.mgr.mu.Unlock()
	h.helper.mu.Lock()
	var installs []string
	for _, o := range h.helper.installs {
		installs = append(installs, o.CoreBin)
	}
	h.helper.mu.Unlock()
	if len(installs) != 1 || installs[0] != newer || h.mgr.stops != stopsBefore+1 {
		t.Fatalf("helper installs = %v stops=%d", installs, h.mgr.stops-stopsBefore)
	}
	_ = old

	// back to the bundled core
	if err := h.s.UseCore(""); err != nil {
		t.Fatalf("UseCore(bundled): %v", err)
	}
	if set := h.s.Settings(); set.CorePath != "" || set.CoreVersion != "" {
		t.Fatalf("bundled should clear CorePath/CoreVersion: %+v", set)
	}
	if spec := h.mgr.specs[len(h.mgr.specs)-1]; spec.Bin != filepath.Join(h.dir, "bundled-mihomo") {
		t.Fatalf("bundled core should run: %+v", spec)
	}
	list := h.s.InstalledCores()
	if len(list) != 3 || !list[0].Bundled || !list[0].Active || list[1].Active || list[2].Active {
		t.Fatalf("installed list: %+v", list)
	}
}

// ---- remove ----

func TestRemoveCoreRefusals(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	old, newer := twoCores(t, h)
	if err := h.s.RemoveCore(""); err == nil || !strings.Contains(err.Error(), "内置") {
		t.Fatalf("bundled must be refused: %v", err)
	}
	if err := h.s.RemoveCore("v1.19.32"); err == nil || !strings.Contains(err.Error(), "正在使用") {
		t.Fatalf("active must be refused: %v", err)
	}
	if err := h.s.RemoveCore("v1.19.99"); err == nil || !strings.Contains(err.Error(), "未安装") {
		t.Fatalf("unknown must be refused: %v", err)
	}
	if err := h.s.RemoveCore("../core"); err == nil {
		t.Fatal("path-like names must be refused")
	}
	if err := h.s.RemoveCore("v1.19.33"); err != nil {
		t.Fatalf("RemoveCore: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(newer)); err == nil {
		t.Fatal("version directory should be gone")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("active core must stay")
	}
	for _, c := range h.s.InstalledCores() {
		if c.Version == "v1.19.33" {
			t.Fatalf("removed version still listed: %+v", c)
		}
	}
}

// ---- update check ----

// apiServer serves the GitHub releases API from sampleReleases, counting
// requests; status overrides the answer when not 200.
func apiServer(t *testing.T, rels []ghRelease, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if status != http.StatusOK {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(37*time.Minute).Unix()))
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/releases/tags/"+coreAlphaTag) {
			for _, rel := range rels {
				if rel.Tag == coreAlphaTag {
					_ = json.NewEncoder(w).Encode(rel)
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(rels)
	}))
	t.Cleanup(srv.Close)
	prev := coreAPIBase
	coreAPIBase = srv.URL
	t.Cleanup(func() { coreAPIBase = prev })
	return srv, &hits
}

func TestCheckCoreUpdateAndCache(t *testing.T) {
	_, hits := apiServer(t, sampleReleases(), http.StatusOK)
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	installOldCore(t, h) // v1.19.32 active

	info, err := h.s.CheckCoreUpdate()
	if err != nil {
		t.Fatalf("CheckCoreUpdate: %v", err)
	}
	if info.Channel != model.CoreChannelStable || info.Current != "v1.19.32" || info.Latest != "v1.19.34" || !info.Available || info.Installed || info.CheckedAt.IsZero() {
		t.Fatalf("info = %+v", info)
	}
	if !strings.Contains(info.Notes, "dns fallback") {
		t.Fatalf("notes: %q", info.Notes)
	}
	// cached: no second request within the hour
	if _, err := h.s.CoreReleases(model.CoreChannelStable, false); err != nil || hits.Load() != 1 {
		t.Fatalf("cache miss: %v hits=%d", err, hits.Load())
	}
	if _, err := h.s.CoreReleases(model.CoreChannelStable, true); err != nil || hits.Load() != 2 {
		t.Fatalf("refresh should fetch: %v hits=%d", err, hits.Load())
	}
	// the disk cache survives a new service on the same data dir
	if _, err := os.Stat(h.s.releaseCachePath(model.CoreChannelStable)); err != nil {
		t.Fatal("release list not cached on disk")
	}
	// alpha channel
	alpha, err := h.s.CoreReleases(model.CoreChannelAlpha, false)
	if err != nil || len(alpha) != 1 || alpha[0].Version != "alpha-5a3a0ef" {
		t.Fatalf("alpha: %+v %v", alpha, err)
	}
	set := h.s.Settings()
	set.CoreChannel = model.CoreChannelAlpha
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	info, err = h.s.CheckCoreUpdate()
	if err != nil || info.Channel != model.CoreChannelAlpha || info.Latest != "alpha-5a3a0ef" || !info.Available {
		t.Fatalf("alpha info = %+v %v", info, err)
	}
	// the latest already installed: a switch, not an update
	set.CoreChannel = model.CoreChannelStable
	_ = h.s.SaveSettings(set)
	dir := h.s.coreVersionDir("v1.19.34")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, coreBinaryName()), []byte("x"), 0o700)
	h.s.refreshCores()
	info, err = h.s.CheckCoreUpdate()
	if err != nil || info.Available || !info.Installed || info.Latest != "v1.19.34" {
		t.Fatalf("installed latest: %+v %v", info, err)
	}
}

func TestCoreReleasesRateLimit(t *testing.T) {
	_, _ = apiServer(t, nil, http.StatusForbidden)
	h := newHarness(t, func(h *harness) { h.core.findFn = realFind })
	_, err := h.s.CoreReleases(model.CoreChannelStable, false)
	if err == nil || !strings.Contains(err.Error(), "上限") || !strings.Contains(err.Error(), "分钟") {
		t.Fatalf("expected a rate limit message, got %v", err)
	}
	// with a (stale) cache the list still comes back
	h.storeStale(t, model.CoreChannelStable, releasesFromAPI(model.CoreChannelStable, sampleReleases(), runtime.GOOS, runtime.GOARCH))
	list, err := h.s.CoreReleases(model.CoreChannelStable, true)
	if err != nil || len(list) != 2 {
		t.Fatalf("stale cache fallback: %v %+v", err, list)
	}
}

// storeStale caches list as if fetched two hours ago.
func (h *harness) storeStale(t *testing.T, channel string, list []model.CoreRelease) {
	t.Helper()
	h.s.storeReleases(channel, list)
	h.s.cores.mu.Lock()
	rc := h.s.cores.releases[channel]
	rc.At = time.Now().Add(-2 * time.Hour)
	h.s.cores.releases[channel] = rc
	h.s.cores.mu.Unlock()
}

func TestCoreUpdateLoopNotifiesOrInstalls(t *testing.T) {
	_, _ = apiServer(t, sampleReleases(), http.StatusOK)
	body := []byte("new core")
	srv, _ := coreServer(t, body, http.StatusOK)
	useCoreServer(t, srv)
	h := newHarness(t, func(h *harness) {
		h.core.findFn = realFind
		h.core.versionFn = func(bin string) (string, error) {
			if strings.Contains(bin, "v1.19.34") {
				return "v1.19.34", nil
			}
			return "v1.19.32", nil
		}
	})
	installOldCore(t, h)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.s.TakeNotices()

	// auto check off: nothing happens
	set := h.s.Settings()
	set.CoreAutoCheck = false
	_ = h.s.SaveSettings(set)
	h.s.runCoreUpdateCheck()
	if n := h.s.TakeNotices(); len(n) != 0 {
		t.Fatalf("no notice expected with auto check off: %+v", n)
	}

	// auto check on, auto install off: a notice, no switch
	set.CoreAutoCheck = true
	_ = h.s.SaveSettings(set)
	h.s.runCoreUpdateCheck()
	n := h.s.TakeNotices()
	if len(n) != 1 || !strings.Contains(n[0].Title, "v1.19.34") || n[0].Kind != "toast" {
		t.Fatalf("notice = %+v", n)
	}
	if h.s.Settings().CoreVersion != "v1.19.32" {
		t.Fatal("must not switch without auto install")
	}
	// within a day the check does not run again
	h.s.runCoreUpdateCheck()
	if n := h.s.TakeNotices(); len(n) != 0 {
		t.Fatalf("daily check ran twice: %+v", n)
	}

	// auto install on: downloads, verifies and switches
	set.CoreAutoInstall = true
	_ = h.s.SaveSettings(set)
	h.s.cores.mu.Lock()
	h.s.cores.lastCheck = time.Time{}
	h.s.cores.mu.Unlock()
	stops := h.mgr.stops
	h.s.runCoreUpdateCheck()
	n = h.s.TakeNotices()
	if len(n) != 1 || !strings.Contains(n[0].Title, "内核已更新到 v1.19.34") {
		t.Fatalf("notice = %+v", n)
	}
	if set := h.s.Settings(); set.CoreVersion != "v1.19.34" || set.CorePath != h.s.coreVersionPath("v1.19.34") {
		t.Fatalf("not switched: %+v", set)
	}
	if h.mgr.stops != stops+1 || !h.mgr.Running() {
		t.Fatalf("expected a fast restart: stops=%d running=%v", h.mgr.stops-stops, h.mgr.Running())
	}
}
