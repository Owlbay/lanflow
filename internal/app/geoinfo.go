package app

import (
	"os"
	"path/filepath"
	"time"

	"lanflow/internal/model"
)

// geoFiles are the database files Mihomo keeps in its work directory:
// what geox-url downloads (geoip.metadb, ASN.mmdb, geosite.dat, geoip.dat)
// plus the bundled ASN copy the exit locator reads.
var geoFiles = []string{"geoip.metadb", "ASN.mmdb", "GeoLite2-ASN.mmdb", "geosite.dat", "geoip.dat"}

// geoInfoTTL bounds how often GeoDataInfo stats the files; the settings
// page asks on every frame.
const geoInfoTTL = 5 * time.Second

// GeoDataInfo reports when the geo databases last changed. It is derived
// from the files themselves, so the core's scheduled geo-auto-update
// (which Lanflow never sees) shows up too; the shipped copies keep the
// bundle's time and are reported as such.
func (s *Service) GeoDataInfo() model.GeoDataInfo {
	now := s.d.now()
	s.mu.Lock()
	if !s.state.geoInfoAt.IsZero() && now.Sub(s.state.geoInfoAt) < geoInfoTTL {
		info := s.state.geoInfo
		s.mu.Unlock()
		return info
	}
	s.mu.Unlock()
	info := readGeoInfo(s.workDir, filepath.Join(s.opt.ResourceDir, "geo"))
	s.mu.Lock()
	s.state.geoInfo, s.state.geoInfoAt = info, now
	s.mu.Unlock()
	return info
}

// readGeoInfo stats the geo files in workDir. A file counts as bundled
// when it still matches the copy in bundleDir (size and time, which
// copyBundledGeo preserves).
func readGeoInfo(workDir, bundleDir string) model.GeoDataInfo {
	var info model.GeoDataInfo
	bundled := true
	for _, name := range geoFiles {
		fi, err := os.Stat(filepath.Join(workDir, name))
		if err != nil || fi.IsDir() {
			continue
		}
		info.Files++
		if fi.ModTime().After(info.UpdatedAt) {
			info.UpdatedAt = fi.ModTime()
		}
		bfi, err := os.Stat(filepath.Join(bundleDir, name))
		if err != nil || bfi.Size() != fi.Size() || !bfi.ModTime().Equal(fi.ModTime()) {
			bundled = false
		}
	}
	info.Bundled = info.Files > 0 && bundled
	return info
}
