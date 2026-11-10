package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The bundled copies keep the bundle's time and count as shipped; a file
// the core downloaded (newer, different) makes the set a real update and
// its time the 上次更新 value.
func TestGeoDataInfoBundledAndUpdated(t *testing.T) {
	res := t.TempDir()
	bundle := filepath.Join(res, "geo")
	work := t.TempDir()
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	built := time.Date(2025, 10, 1, 12, 0, 0, 0, time.Local)
	for _, name := range []string{"geoip.metadb", "geosite.dat"} {
		p := filepath.Join(bundle, name)
		if err := os.WriteFile(p, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, built, built); err != nil {
			t.Fatal(err)
		}
	}
	if info := readGeoInfo(work, bundle); info.Files != 0 || info.Bundled || !info.UpdatedAt.IsZero() {
		t.Fatalf("empty work dir = %+v", info)
	}

	copyBundledGeo(bundle, work)
	info := readGeoInfo(work, bundle)
	if info.Files != 2 || !info.Bundled || !info.UpdatedAt.Equal(built) {
		t.Fatalf("after copy = %+v", info)
	}

	// The core's auto-update rewrote geosite.dat.
	later := built.Add(30 * 24 * time.Hour)
	p := filepath.Join(work, "geosite.dat")
	if err := os.WriteFile(p, []byte("new geosite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	info = readGeoInfo(work, bundle)
	if info.Bundled || !info.UpdatedAt.Equal(later) || info.Files != 2 {
		t.Fatalf("after update = %+v", info)
	}
}

// The service caches the answer briefly and re-reads after a manual update.
func TestGeoDataInfoCaching(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	h := newHarness(t, nil)
	h.s.d.now = func() time.Time { return now }
	if info := h.s.GeoDataInfo(); info.Files != 0 {
		t.Fatalf("no files yet: %+v", info)
	}
	p := filepath.Join(h.s.workDir, "geoip.metadb")
	if err := os.MkdirAll(h.s.workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info := h.s.GeoDataInfo(); info.Files != 0 {
		t.Fatalf("cached answer expected, got %+v", info)
	}
	now = now.Add(geoInfoTTL + time.Second)
	if info := h.s.GeoDataInfo(); info.Files != 1 || info.Bundled {
		t.Fatalf("after TTL = %+v", info)
	}
	// 立即更新 invalidates at once.
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.s.workDir, "geosite.dat"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.s.UpdateGeoData(); err != nil {
		t.Fatal(err)
	}
	if info := h.s.GeoDataInfo(); info.Files != 2 {
		t.Fatalf("after UpdateGeoData = %+v", info)
	}
}
