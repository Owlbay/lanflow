package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"lanflow/internal/model"
)

// The pinned table here and the one in scripts/fetch-core.sh must agree,
// and cover every platform coreAsset knows.
func TestPinnedCoreSumsMatchFetchScript(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "fetch-core.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	if m := regexp.MustCompile(`(?m)^PINNED_VERSION="([^"]+)"`).FindStringSubmatch(script); m == nil || m[1] != pinnedCoreVersion {
		t.Fatalf("PINNED_VERSION in fetch-core.sh = %v, want %s", m, pinnedCoreVersion)
	}
	inScript := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*(mihomo-[^)]+)\) echo ([0-9a-f]{64}) ;;`).FindAllStringSubmatch(script, -1) {
		inScript[m[1]] = m[2]
	}
	if len(inScript) != len(pinnedCoreSHA256) {
		t.Fatalf("fetch-core.sh pins %d assets, coresums.go %d", len(inScript), len(pinnedCoreSHA256))
	}
	for asset, sum := range pinnedCoreSHA256 {
		if inScript[asset] != sum {
			t.Errorf("%s: fetch-core.sh %q, coresums.go %q", asset, inScript[asset], sum)
		}
		if !sha256Hex.MatchString(sum) {
			t.Errorf("%s: malformed sum %q", asset, sum)
		}
	}
	for _, p := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		asset, _, err := coreAsset(p[0], p[1], pinnedCoreVersion)
		if err != nil || pinnedCoreSHA256[asset] == "" {
			t.Errorf("no pinned sum for %s/%s (%q, %v)", p[0], p[1], asset, err)
		}
	}
}

// digestServer serves /releases/tags/<tag> with the given asset digests
// and points coreAPIBase at it.
func digestServer(t *testing.T, tag string, digests map[string]string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/releases/tags/"+tag {
			http.NotFound(w, r)
			return
		}
		type asset struct {
			Name   string  `json:"name"`
			Digest *string `json:"digest"`
		}
		rel := struct {
			Tag    string  `json:"tag_name"`
			Assets []asset `json:"assets"`
		}{Tag: tag}
		for name, d := range digests {
			a := asset{Name: name}
			if d != "" {
				a.Digest = &d
			}
			rel.Assets = append(rel.Assets, a)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rel)
	}))
	t.Cleanup(srv.Close)
	prev := coreAPIBase
	coreAPIBase = srv.URL
	t.Cleanup(func() { coreAPIBase = prev })
	return &hits
}

func TestExpectedCoreSHA256(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	good := strings.Repeat("ab", 32)

	// the pinned version needs no network
	hits := digestServer(t, "v1.19.34", nil)
	asset := "mihomo-darwin-arm64-v1.19.33.gz"
	if sum, err := h.s.expectedCoreSHA256(ctx, model.CoreChannelStable, pinnedCoreVersion, asset); err != nil || sum != pinnedCoreSHA256[asset] {
		t.Fatalf("pinned: %q %v", sum, err)
	}
	if hits.Load() != 0 {
		t.Fatalf("the pinned version must not ask GitHub, hits=%d", hits.Load())
	}

	// another stable version: the API digest of the asset
	asset = "mihomo-darwin-arm64-v1.19.34.gz"
	digestServer(t, "v1.19.34", map[string]string{asset: "sha256:" + strings.ToUpper(good), "other.gz": "sha256:" + strings.Repeat("0", 64)})
	if sum, err := h.s.expectedCoreSHA256(ctx, model.CoreChannelStable, "v1.19.34", asset); err != nil || sum != good {
		t.Fatalf("api digest: %q %v", sum, err)
	}
	// an asset without a digest, or with something else, is refused
	digestServer(t, "v1.19.34", map[string]string{asset: ""})
	if _, err := h.s.expectedCoreSHA256(ctx, model.CoreChannelStable, "v1.19.34", asset); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("missing digest must be refused: %v", err)
	}
	digestServer(t, "v1.19.34", map[string]string{asset: "md5:abc"})
	if _, err := h.s.expectedCoreSHA256(ctx, model.CoreChannelStable, "v1.19.34", asset); err == nil {
		t.Fatal("a non-sha256 digest must be refused")
	}
	digestServer(t, "v1.19.34", map[string]string{"other.gz": "sha256:" + good})
	if _, err := h.s.expectedCoreSHA256(ctx, model.CoreChannelStable, "v1.19.34", asset); err == nil {
		t.Fatal("an asset the release lacks must be refused")
	}

	// Alpha versions are looked up in the rolling Prerelease-Alpha release
	asset = "mihomo-darwin-arm64-alpha-5a3a0ef.gz"
	digestServer(t, coreAlphaTag, map[string]string{asset: "sha256:" + good})
	if sum, err := h.s.expectedCoreSHA256(ctx, model.CoreChannelAlpha, "alpha-5a3a0ef", asset); err != nil || sum != good {
		t.Fatalf("alpha digest: %q %v", sum, err)
	}

	// the API answering an error is an error, not an empty sum
	digestServer(t, "nothing", nil)
	if _, err := h.s.expectedCoreSHA256(ctx, model.CoreChannelStable, "v1.19.34", "mihomo-darwin-arm64-v1.19.34.gz"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("API error must surface: %v", err)
	}
}

// The digest is looked up on api.github.com itself, not through the
// download mirror.
func TestExpectedCoreSHA256IgnoresMirror(t *testing.T) {
	good := strings.Repeat("cd", 32)
	asset := "mihomo-darwin-arm64-v1.19.34.gz"
	hits := digestServer(t, "v1.19.34", map[string]string{asset: "sha256:" + good})
	h := newHarness(t, nil)
	set := h.s.Settings()
	set.DownloadMirror = "http://127.0.0.1:1/" // nothing listens there
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if sum, err := h.s.expectedCoreSHA256(context.Background(), model.CoreChannelStable, "v1.19.34", asset); err != nil || sum != good || hits.Load() == 0 {
		t.Fatalf("mirror must not be used for the digest: %q %v hits=%d", sum, err, hits.Load())
	}
}

// A download whose SHA-256 differs from the expected one is neither run
// nor installed.
func TestInstallCoreRejectsChecksumMismatch(t *testing.T) {
	srv, _ := coreServer(t, []byte("tampered core"), http.StatusOK)
	useCoreServer(t, srv)
	trustDigest(t, strings.Repeat("0", 64))
	var ran atomic.Int32
	h := newHarness(t, func(h *harness) {
		h.core.findFn = realFind
		h.core.versionFn = func(string) (string, error) { ran.Add(1); return pinnedCoreVersion, nil }
	})
	err := h.s.InstallCore(model.CoreChannelStable, pinnedCoreVersion)
	if err == nil || !strings.Contains(err.Error(), "校验失败") || !strings.Contains(err.Error(), strings.Repeat("0", 64)) {
		t.Fatalf("expected a checksum error, got %v", err)
	}
	if ran.Load() != 0 {
		t.Fatalf("a core failing the checksum must not be run (mihomo -v ran %d times)", ran.Load())
	}
	if len(h.core.validated) != 0 {
		t.Fatalf("a core failing the checksum must not be run (mihomo -t: %v)", h.core.validated)
	}
	if _, serr := os.Stat(h.s.coreVersionDir(pinnedCoreVersion)); serr == nil {
		t.Fatal("nothing may be left in the version directory")
	}
	if dl := h.s.CoreDownload(); dl.Active || !strings.Contains(dl.Error, "校验失败") {
		t.Fatalf("progress should carry the error: %+v", dl)
	}
}

// End to end with the real lookup: a non-pinned version installs when the
// API digest matches, and is not even downloaded when there is none.
func TestInstallCoreUsesAPIDigest(t *testing.T) {
	body := []byte("core v1.19.34")
	srv, dlHits := coreServer(t, body, http.StatusOK)
	useCoreServer(t, srv)
	coreDigest = (*Service).expectedCoreSHA256 // coreServer trusted any sum; use the real lookup

	asset, _, err := coreAsset(runtime.GOOS, runtime.GOARCH, "v1.19.34")
	if err != nil {
		t.Skip(err)
	}
	// the payload coreServer serves for this platform
	resp, err := http.Get(srv.URL + "/v1.19.34/" + asset)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("payload: %v %d", err, resp.StatusCode)
	}
	raw := sha256.Sum256(payload)
	sum := hex.EncodeToString(raw[:])

	h := newHarness(t, func(h *harness) {
		h.core.findFn = realFind
		h.core.versionFn = func(string) (string, error) { return "v1.19.34", nil }
	})

	digestServer(t, "v1.19.34", map[string]string{asset: ""})
	before := dlHits.Load()
	if err := h.s.InstallCore(model.CoreChannelStable, "v1.19.34"); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("no digest must refuse the install: %v", err)
	}
	if dlHits.Load() != before {
		t.Fatal("nothing should be downloaded without a known checksum")
	}

	digestServer(t, "v1.19.34", map[string]string{asset: "sha256:" + sum})
	if err := h.s.InstallCore(model.CoreChannelStable, "v1.19.34"); err != nil {
		t.Fatalf("InstallCore: %v", err)
	}
	if got, _ := os.ReadFile(h.s.coreVersionPath("v1.19.34")); string(got) != string(body) {
		t.Fatalf("installed core = %q", got)
	}
}
