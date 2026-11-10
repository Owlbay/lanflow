package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Mihomo core version management (API.InstalledCores / InstallCore /
// UseCore / RemoveCore / CoreReleases / CheckCoreUpdate; the 内核 tab).
//
// Installed cores live side by side:
//
//	<DataDir>/cores/<version>/mihomo[.exe]
//	<DataDir>/cores/<version>/meta.json   {version, channel, sha256, installedAt, source}
//
// Settings.CoreVersion names the active one ("" = the core bundled with
// the app, or a custom file in CorePath); CorePath always holds the
// resolved binary so FindBinary, the helper and the diagnostics keep
// working as before. The bundled core (app Resources) is listed as 「内置」
// and is never deleted. A core downloaded by an older version into
// <DataDir>/core/bin is moved into the new layout at startup.

const (
	coresDirName = "cores"
	coreMetaFile = "meta.json"
)

// coreMeta is cores/<version>/meta.json.
type coreMeta struct {
	Version     string    `json:"version"`
	Channel     string    `json:"channel"`
	SHA256      string    `json:"sha256"`
	InstalledAt time.Time `json:"installedAt"`
	Source      string    `json:"source"`
}

// coresState caches what the cheap reads return; its own lock, never held
// across I/O other than a directory scan.
type coresState struct {
	mu        sync.Mutex
	installed []model.InstalledCore // managed versions, newest first; nil = not scanned yet
	bundled   string                // version of the bundled core, "" until known
	bundleBin string                // path of the bundled core, "" when none
	releases  map[string]releaseCache
	lastCheck time.Time // last update check (coreUpdateLoop)
}

// versionDirRe keeps directory names (and the versions users can pass)
// to what Mihomo releases: v1.19.33, alpha-5a3a0ef.
var versionDirRe = regexp.MustCompile(`^(v\d+\.\d+\.\d+|alpha-[0-9a-f]{6,12})$`)

// coreBinaryName is the file name of the core inside a version directory.
func coreBinaryName() string {
	if runtime.GOOS == "windows" {
		return "mihomo.exe"
	}
	return "mihomo"
}

// coresDir is <DataDir>/cores.
func (s *Service) coresDir() string { return filepath.Join(s.opt.DataDir, coresDirName) }

// coreVersionDir is <DataDir>/cores/<version>.
func (s *Service) coreVersionDir(version string) string {
	return filepath.Join(s.coresDir(), version)
}

// coreVersionPath is the binary of an installed version.
func (s *Service) coreVersionPath(version string) string {
	return filepath.Join(s.coreVersionDir(version), coreBinaryName())
}

// channelOf guesses the channel of a version string.
func channelOf(version string) string {
	if strings.HasPrefix(version, "alpha") {
		return model.CoreChannelAlpha
	}
	return model.CoreChannelStable
}

// validChannel normalizes channel ("" = stable).
func validChannel(channel string) (string, error) {
	switch channel {
	case "", model.CoreChannelStable:
		return model.CoreChannelStable, nil
	case model.CoreChannelAlpha:
		return model.CoreChannelAlpha, nil
	}
	return "", fmt.Errorf(i18n.T("未知的内核更新通道：%s"), channel)
}

// checkVersionName refuses anything that is not a release version, so a
// version never names a path outside cores/.
func checkVersionName(version string) error {
	if !versionDirRe.MatchString(version) {
		return fmt.Errorf(i18n.T("内核版本号格式不正确：%q"), version)
	}
	return nil
}

// readCoreMeta reads cores/<version>/meta.json.
func readCoreMeta(dir string) (coreMeta, error) {
	var m coreMeta
	data, err := os.ReadFile(filepath.Join(dir, coreMetaFile))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	return m, nil
}

// writeCoreMeta writes cores/<version>/meta.json.
func writeCoreMeta(dir string, m coreMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, coreMetaFile), data, 0o600)
}

// fileSHA256 hashes a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// scanInstalledCores reads every version directory under cores/.
func (s *Service) scanInstalledCores() []model.InstalledCore {
	entries, err := os.ReadDir(s.coresDir())
	if err != nil {
		return nil
	}
	var out []model.InstalledCore
	for _, e := range entries {
		if !e.IsDir() || !versionDirRe.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(s.coresDir(), e.Name())
		bin := filepath.Join(dir, coreBinaryName())
		fi, err := os.Stat(bin)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		ic := model.InstalledCore{Version: e.Name(), Channel: channelOf(e.Name()), Path: bin, Size: fi.Size(), InstalledAt: fi.ModTime()}
		if m, err := readCoreMeta(dir); err == nil {
			if m.Channel != "" {
				ic.Channel = m.Channel
			}
			if !m.InstalledAt.IsZero() {
				ic.InstalledAt = m.InstalledAt
			}
			ic.SHA256, ic.Source = m.SHA256, m.Source
		}
		out = append(out, ic)
	}
	sort.Slice(out, func(i, j int) bool { return newerVersion(out[i].Version, out[j].Version) })
	return out
}

// newerVersion reports whether a sorts before b: stable releases by
// semver descending, then alpha builds by name; stable before alpha.
func newerVersion(a, b string) bool {
	ca, cb := channelOf(a), channelOf(b)
	if ca != cb {
		return ca == model.CoreChannelStable
	}
	if ca == model.CoreChannelStable {
		return compareSemver(a, b) > 0
	}
	return a > b
}

// compareSemver compares "v1.19.33"-style versions; anything unparsable
// sorts last.
func compareSemver(a, b string) int {
	pa, oka := semverParts(a)
	pb, okb := semverParts(b)
	if oka != okb {
		if oka {
			return 1
		}
		return -1
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] > pb[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

func semverParts(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// refreshCores rescans the directory and tells the UI.
func (s *Service) refreshCores() {
	list := s.scanInstalledCores()
	s.cores.mu.Lock()
	s.cores.installed = list
	s.cores.mu.Unlock()
	s.em.emit(TopicCores)
}

// installedLocked returns the cached list, scanning once; cores.mu must be held.
func (s *Service) installedLocked() []model.InstalledCore {
	if s.cores.installed == nil {
		s.cores.installed = s.scanInstalledCores()
		if s.cores.installed == nil {
			s.cores.installed = []model.InstalledCore{}
		}
	}
	return s.cores.installed
}

// bundledCore returns the bundled core's path ("" when the app ships none)
// and its version when known.
func (s *Service) bundledCore() (bin, version string) {
	s.cores.mu.Lock()
	defer s.cores.mu.Unlock()
	if s.cores.bundleBin == "" {
		if p, err := s.d.core.FindBinary(""); err == nil {
			s.cores.bundleBin = p
		}
	}
	return s.cores.bundleBin, s.cores.bundled
}

// learnBundledVersion runs the bundled core once (-v) and remembers its
// version; called from bootstrap, off the UI thread.
func (s *Service) learnBundledVersion() {
	bin, v := s.bundledCore()
	if bin == "" || v != "" {
		return
	}
	ctx, cancel := context.WithTimeout(s.rootCtx, 10*time.Second)
	got, err := s.d.core.Version(ctx, bin)
	cancel()
	if err != nil {
		return
	}
	s.cores.mu.Lock()
	s.cores.bundled = normalizeVersion(got)
	s.cores.mu.Unlock()
	s.em.emit(TopicCores)
}

// activeCore returns the version in use: Settings.CoreVersion when its
// binary is the one CorePath points at, else "" (bundled or custom).
func (s *Service) activeCore() (version string, custom bool) {
	s.mu.Lock()
	set := s.state.settings
	s.mu.Unlock()
	if set.CoreVersion != "" && set.CorePath == s.coreVersionPath(set.CoreVersion) {
		return set.CoreVersion, false
	}
	return "", set.CorePath != ""
}

// InstalledCores implements API.InstalledCores.
func (s *Service) InstalledCores() []model.InstalledCore {
	active, custom := s.activeCore()
	bundleBin, bundleVer := s.bundledCore()
	s.mu.Lock()
	stVersion := s.state.st.CoreVersion
	s.mu.Unlock()

	s.cores.mu.Lock()
	managed := s.installedLocked()
	out := make([]model.InstalledCore, 0, len(managed)+1)
	if bundleBin != "" {
		b := model.InstalledCore{Version: bundleVer, Bundled: true, Active: active == "" && !custom, Path: bundleBin, Source: "bundled"}
		if fi, err := os.Stat(bundleBin); err == nil {
			b.Size, b.InstalledAt = fi.Size(), fi.ModTime()
		}
		if b.Version == "" && b.Active {
			b.Version = stVersion
		}
		out = append(out, b)
	}
	for _, c := range managed {
		c.Active = c.Version == active
		out = append(out, c)
	}
	s.cores.mu.Unlock()
	return out
}

// isInstalled reports whether version has a usable binary in cores/.
func (s *Service) isInstalled(version string) bool {
	s.cores.mu.Lock()
	defer s.cores.mu.Unlock()
	for _, c := range s.installedLocked() {
		if c.Version == version {
			return true
		}
	}
	return false
}

// RemoveCore implements API.RemoveCore.
func (s *Service) RemoveCore(version string) (err error) {
	defer func() { s.logOp("remove core "+version, err) }()
	version = normalizeVersion(version)
	if version == "" {
		return errors.New(i18n.T("内置内核随应用安装，不能删除。"))
	}
	if err := checkVersionName(version); err != nil {
		return err
	}
	if active, _ := s.activeCore(); active == version {
		return errors.New(i18n.T("这是正在使用的内核，请先切换到其他版本再删除。"))
	}
	s.mu.Lock()
	dl := s.coreDLLocked()
	busy := dl.Active && dl.Version == version
	s.mu.Unlock()
	if busy {
		return errors.New(i18n.T("内核正在下载中，请稍候。"))
	}
	if !s.isInstalled(version) {
		return fmt.Errorf(i18n.T("内核 %s 未安装。"), version)
	}
	if err := os.RemoveAll(s.coreVersionDir(version)); err != nil {
		return fmt.Errorf(i18n.T("删除内核 %s 失败：%v"), version, err)
	}
	s.refreshCores()
	return nil
}

// migrateLegacyCore moves a core downloaded by an older Lanflow from
// <DataDir>/core/bin/mihomo into cores/<version>/ and repoints CorePath
// when it was in use. Runs before the binary is resolved at startup; a
// core that cannot report its version stays where it is.
func (s *Service) migrateLegacyCore() {
	legacy := s.managedCorePath()
	fi, err := os.Stat(legacy)
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	got, err := s.d.core.Version(ctx, legacy)
	cancel()
	version := normalizeVersion(got)
	if err != nil || checkVersionName(version) != nil {
		s.alog.Warnf("legacy core %s left in place: %v (version %q)", legacy, err, got)
		return
	}
	dir := s.coreVersionDir(version)
	dst := filepath.Join(dir, coreBinaryName())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.alog.Errorf("migrate core: %v", err)
		return
	}
	if _, err := os.Stat(dst); err == nil {
		os.Remove(legacy) // the same version is already in the new layout
	} else if err := os.Rename(legacy, dst); err != nil {
		s.alog.Errorf("migrate core %s → %s: %v", legacy, dst, err)
		return
	}
	if _, err := os.Stat(filepath.Join(dir, coreMetaFile)); err != nil {
		sum, _ := fileSHA256(dst)
		_ = writeCoreMeta(dir, coreMeta{Version: version, Channel: channelOf(version), SHA256: sum, InstalledAt: fi.ModTime(), Source: "migrated"})
	}
	os.Remove(legacy + ".old")
	os.Remove(legacy + ".download")
	os.Remove(legacy + ".new")
	os.Remove(filepath.Dir(legacy)) // only when empty
	s.alog.Infof("core %s moved from %s to %s", version, legacy, dst)
	if s.state.settings.CorePath == legacy || s.state.settings.CorePath == dst {
		s.state.settings.CorePath = dst
		s.state.settings.CoreVersion = version
		_ = saveSettings(s.opt.DataDir, copySettings(s.state.settings))
	}
	s.cores.mu.Lock()
	s.cores.installed = nil
	s.cores.mu.Unlock()
}
