package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lanflow/internal/fsx"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/platform/sysproxy"
)

const (
	settingsFile = "settings.json"
	markerFile   = "sysproxy-set" // exists while Lanflow has the system proxy pointed at itself
)

const geoBase = "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/"

// defaultSettings returns the settings of a fresh installation.
func defaultSettings() model.Settings {
	return model.Settings{
		CloseToTray: true,
		Appearance:  "system",
		Language:    "zh",

		MixedPort:    7890,
		Bypass:       append([]string(nil), sysproxy.DefaultBypass...),
		TUNStack:     "mixed",
		TUNSetDNS:    true,
		ATrustCompat: true,
		ProbeExits:   true,
		Mode:         model.ModeRule,

		GlobalShortcut: "CmdOrCtrl+Shift+S",

		LogLevel:      "info",
		CoreChannel:   model.CoreChannelStable,
		CoreAutoCheck: true,

		DelayURL:       defaultDelayURL,
		DelayTimeoutMs: int(defaultDelayTimeout / time.Millisecond),

		DNS: model.DNSSettings{
			EnhancedMode:          "fake-ip",
			Nameserver:            []string{"https://doh.pub/dns-query", "https://dns.alidns.com/dns-query"},
			ProxyServerNameserver: []string{"https://doh.pub/dns-query"},
			DirectNameserver:      []string{"system"},
			Policy:                []model.KV{{Key: "geosite:geolocation-!cn", Value: "https://1.1.1.1/dns-query"}},
			FakeIPFilter:          []string{"*.lan", "*.local", "+.msftconnecttest.com"},
			UseSystemHosts:        true,
		},
		Sniffer: model.SnifferSettings{
			Enable: true, HTTP: true, TLS: true,
			HTTPPorts: "80, 8080-8880", TLSPorts: "443, 8443", QUICPorts: "443, 8443",
			OverrideDestination: true,
		},
		Geo: model.GeoSettings{
			GeoIP:         geoBase + "geoip.dat",
			GeoSite:       geoBase + "geosite.dat",
			MMDB:          geoBase + "geoip.metadb",
			ASN:           geoBase + "GeoLite2-ASN.mmdb",
			Mode:          "db",
			IntervalHours: 24,
		},
		Sites: []model.Site{
			{Name: "Google", URL: "https://www.google.com"},
			{Name: "YouTube", URL: "https://www.youtube.com"},
			{Name: "GitHub", URL: "https://github.com"},
			{Name: "OpenAI", URL: "https://api.openai.com"},
			{Name: "Cloudflare", URL: "https://www.cloudflare.com"},
			{Name: "Apple", URL: "https://www.apple.com"},
			{Name: "百度", URL: "https://www.baidu.com"},
		},
	}
}

// loadSettings reads settings.json from dir on top of the defaults, so
// fields missing from an older file keep their default values. A missing
// file yields the defaults; a corrupt file yields the defaults and an error.
func loadSettings(dir string) (model.Settings, error) {
	s := defaultSettings()
	data, err := os.ReadFile(filepath.Join(dir, settingsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf(i18n.T("读取设置失败：%v"), err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return defaultSettings(), fmt.Errorf(i18n.T("设置文件损坏，已使用默认设置：%v"), err)
	}
	return normalizeSettings(s), nil
}

// normalizeSettings fills invalid values with defaults.
func normalizeSettings(s model.Settings) model.Settings {
	d := defaultSettings()
	if s.MixedPort <= 0 || s.MixedPort > 65535 {
		s.MixedPort = d.MixedPort
	}
	switch s.Mode {
	case model.ModeRule, model.ModeGlobal, model.ModeDirect:
	default:
		s.Mode = d.Mode
	}
	switch s.LogLevel {
	case "debug", "info", "warning", "error", "silent":
	default:
		s.LogLevel = d.LogLevel
	}
	switch s.TUNStack {
	case "mixed", "system", "gvisor":
	default:
		s.TUNStack = d.TUNStack
	}
	switch s.CoreChannel {
	case model.CoreChannelStable, model.CoreChannelAlpha:
	default:
		s.CoreChannel = d.CoreChannel
	}
	if s.Geo.Mode != "db" && s.Geo.Mode != "dat" {
		s.Geo.Mode = d.Geo.Mode
	}
	if s.Geo.IntervalHours <= 0 {
		s.Geo.IntervalHours = d.Geo.IntervalHours
	}
	if s.Geo.GeoIP == "" {
		s.Geo.GeoIP = d.Geo.GeoIP
	}
	if s.Geo.GeoSite == "" {
		s.Geo.GeoSite = d.Geo.GeoSite
	}
	if s.Geo.MMDB == "" {
		s.Geo.MMDB = d.Geo.MMDB
	}
	if s.Geo.ASN == "" {
		s.Geo.ASN = d.Geo.ASN
	}
	if s.DNS.EnhancedMode != "fake-ip" && s.DNS.EnhancedMode != "redir-host" {
		s.DNS.EnhancedMode = d.DNS.EnhancedMode
	}
	if !validDelayURL(s.DelayURL) {
		s.DelayURL = d.DelayURL
	}
	if !validDelayTimeout(s.DelayTimeoutMs) {
		s.DelayTimeoutMs = d.DelayTimeoutMs
	}
	if s.Appearance == "" {
		s.Appearance = d.Appearance
	}
	if s.Language == "" {
		s.Language = d.Language
	}
	s.Advanced = normalizeAdvanced(s.Advanced, s.MixedPort)
	return s
}

// saveSettings writes settings.json atomically with mode 0600.
func saveSettings(dir string, s model.Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf(i18n.T("保存设置失败：%v"), err)
	}
	if err := writeFileAtomic(filepath.Join(dir, settingsFile), data, 0o600); err != nil {
		return fmt.Errorf(i18n.T("保存设置失败：%v"), err)
	}
	return nil
}

// persistSettings writes the in-memory settings to settings.json.
// settingsMu serializes "snapshot + write", so concurrent callers can never
// write an older snapshot after a newer one: the file always ends up with
// the latest state. Call without mu held.
func (s *Service) persistSettings() error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	s.mu.Lock()
	set := copySettings(s.state.settings)
	s.mu.Unlock()
	return saveSettings(s.opt.DataDir, set)
}

// updateSettings applies fn to the in-memory settings under mu and persists
// the result; fn must not block. It is the read-modify-write form of
// persistSettings for changes that do not need the core.
func (s *Service) updateSettings(fn func(*model.Settings)) error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	s.mu.Lock()
	fn(&s.state.settings)
	set := copySettings(s.state.settings)
	s.mu.Unlock()
	return saveSettings(s.opt.DataDir, set)
}

// writeFileAtomic writes data atomically and durably (fsx.WriteFile).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	return fsx.WriteFile(path, data, perm)
}

// marker file: records that Lanflow set the system proxy, so a later start
// can tell its own leftover setting from one made by the user or another app.

func (s *Service) markerPath() string { return filepath.Join(s.opt.DataDir, markerFile) }

// proxyMarker is what Lanflow remembers while it owns the system proxy:
// the port it pointed the proxy at and the settings that were there before
// (restored when Lanflow lets go, so another app's proxy is not lost).
type proxyMarker struct {
	Port  int            `json:"port"`
	Saved sysproxy.Saved `json:"saved,omitempty"`
	// PAC is the PAC URL Lanflow set when the PAC mode is on ("" = a
	// manual proxy at 127.0.0.1:Port was set).
	PAC string `json:"pac,omitempty"`
}

func (s *Service) writeMarker(port int, saved sysproxy.Saved) {
	s.writeMarkerPAC(port, saved, "")
}

func (s *Service) writeMarkerPAC(port int, saved sysproxy.Saved, pac string) {
	data, _ := json.Marshal(proxyMarker{Port: port, Saved: saved, PAC: pac})
	_ = writeFileAtomic(s.markerPath(), data, 0o600)
}

// readMarker returns the marker; ok is false when there is none. Markers
// written by older builds ("127.0.0.1:<port>") carry no snapshot.
func (s *Service) readMarker() (proxyMarker, bool) {
	data, err := os.ReadFile(s.markerPath())
	if err != nil {
		return proxyMarker{}, false
	}
	var m proxyMarker
	if json.Unmarshal(data, &m) == nil && m.Port > 0 {
		return m, true
	}
	if _, p, ok := strings.Cut(strings.TrimSpace(string(data)), ":"); ok {
		m.Port, _ = strconv.Atoi(p)
	}
	return m, true
}

func (s *Service) removeMarker() { _ = os.Remove(s.markerPath()) }

func (s *Service) markerExists() bool {
	_, err := os.Stat(s.markerPath())
	return err == nil
}
