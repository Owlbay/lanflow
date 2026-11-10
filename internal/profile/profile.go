// Package profile stores subscriptions and overrides, downloads
// subscriptions, and composes the runtime config handed to Mihomo.
//
// Exported signatures are a stable contract; bodies live in the other files
// of this package. See docs/advanced.md §0 for the composition order.
//
// On-disk layout under Store.Dir (created 0700, files 0600):
//
//	profiles.json            index: []model.Subscription without runtime flags
//	profiles/<id>.yaml       latest downloaded/imported content (raw)
//	overrides.json           []model.Override, the ordered override list (overrides.go)
//	overrides/*.yaml         the former single texts, migrated into overrides.json on first open
//	custom-rules.json        []model.CustomRule
package profile

import (
	"context"
	"net/http"
	"time"

	"lanflow/internal/model"
)

// Store persists subscriptions, overrides and custom rules. All methods are
// safe for concurrent use. Writes are atomic (temp file + rename).
type Store struct{ s *store }

// Open opens (creating if needed) a store rooted at dir.
func Open(dir string) (*Store, error) { s, err := openStore(dir); return &Store{s}, err }

func (s *Store) Dir() string                              { return s.s.dir }
func (s *Store) List() []model.Subscription               { return s.s.list() }
func (s *Store) Get(id string) (model.Subscription, bool) { return s.s.get(id) }
func (s *Store) Current() (model.Subscription, bool)      { return s.s.current() }
func (s *Store) SetCurrent(id string) error               { return s.s.setCurrent(id) }

// Put inserts or updates the index entry (by ID; empty ID assigns a new one) and returns it.
func (s *Store) Put(sub model.Subscription) (model.Subscription, error) { return s.s.put(sub) }

// Delete removes the subscription, its content and its override.
func (s *Store) Delete(id string) error { return s.s.delete(id) }

// Content returns the stored raw YAML of a subscription.
func (s *Store) Content(id string) ([]byte, error) { return s.s.content(id) }

// SetContent stores raw YAML for a subscription.
func (s *Store) SetContent(id string, data []byte) error { return s.s.setContent(id, data) }

// Override returns the override text for key ("global" or a subscription ID), "" if none.
func (s *Store) Override(key string) string { return s.s.override(key) }

// SetOverride stores the override text ("" deletes it).
func (s *Store) SetOverride(key, text string) error { return s.s.setOverride(key, text) }

// CustomRules returns the custom rules in order.
func (s *Store) CustomRules() []model.CustomRule { return s.s.customRules() }

// SetCustomRules replaces the custom rules (IDs assigned to empty ones).
func (s *Store) SetCustomRules(r []model.CustomRule) ([]model.CustomRule, error) {
	return s.s.setCustomRules(r)
}

// ---- download ----

// Fetched is a downloaded subscription.
type Fetched struct {
	Data           []byte
	Name           string // from Content-Disposition (filename / filename*), "" if absent
	Upload         int64  // subscription-userinfo
	Download       int64
	Total          int64
	Expire         time.Time
	UpdateInterval int    // profile-update-interval header, hours; 0 if absent
	HomePage       string // profile-web-page-url header, "" if absent
}

// Fetch downloads url with userAgent (netx.UserAgent if empty) using client
// (netx.Direct or netx.ViaPort), checks the result parses as Clash/Mihomo
// YAML with at least one of proxies / proxy-providers, and returns it.
// Errors are user-facing Chinese: HTTP status, timeout, "不是 Clash/Mihomo 格式的订阅".
func Fetch(ctx context.Context, client *http.Client, url, userAgent string) (Fetched, error) {
	return fetch(ctx, client, url, userAgent)
}

// CheckYAML verifies data is a Clash/Mihomo profile (YAML map with proxies or proxy-providers).
func CheckYAML(data []byte) error { return checkYAML(data) }

// ---- composition ----

// Managed are the fields Lanflow always controls (applied last).
type Managed struct {
	MixedPort int
	AllowLAN  bool
	Mode      model.Mode
	LogLevel  string
	TUN       bool
	TUNStack  string // "mixed" | "system" | "gvisor"
	ProbePort int    // 0 = no probe listener; else adds listener "lanflow-probe" (http, 127.0.0.1) bound to hidden select group "LANFLOW-PROBE" containing every proxy
	IPv6      bool
}

// Input is everything Compose needs.
type Input struct {
	Subscription   []byte // raw subscription YAML
	SubOverride    string
	GlobalOverride string
	// Overrides are the override-list layers (Layers), applied in order
	// after SubOverride and GlobalOverride, each attributed by its Name.
	Overrides   []Layer
	CustomRules []model.CustomRule
	DNS         model.DNSSettings     // applied when DNS.TakeOver; when TUN is on and the result has no dns section, a default fake-ip DNS is added
	Sniffer     model.SnifferSettings // Enable always applied; full section when TakeOver
	Geo         model.GeoSettings     // geox-url, geodata-mode, geo-auto-update, geo-update-interval
	Managed     Managed
	Coexist     Coexist // a VPN (aTrust) running beside Lanflow; zero = none
	// Advanced are the 高级 options (model/advanced.go), written in the
	// Lanflow layer only where set; see advanced.go.
	Advanced model.AdvancedSettings
	// Capture is the HTTPS 选择性解密 layer (capture.go); zero = none.
	Capture Capture
}

// Output is a composed runtime config.
type Output struct {
	YAML   []byte
	Lines  []model.RuntimeLine // the YAML annotated by source layer (see model.RuntimeLine)
	Groups []string            // proxy-group names in order (for rule targets)
	// Warnings lists what composition removed on its own, e.g. a
	// subscription's references to the decrypting outbound (capture.go).
	Warnings []string
}

// Compose merges, in order: subscription → SubOverride → GlobalOverride →
// Lanflow settings (DNS/sniffer/geo when taken over, custom rules prepended
// to rules) → Managed fields. It strips external-controller*, secret and
// external-ui from the subscription (the controller is passed on the
// command line). It returns an error (Chinese, with the override layer and
// line when possible) for invalid override YAML or a custom rule whose
// target group does not exist.
func Compose(in Input) (Output, error) { return compose(in) }

// ApplyOverride merges override YAML text into base YAML and returns the result.
// Semantics (the common Clash override syntax): `key: v` replaces; `+key: [..]` prepends
// to a list; `key+: [..]` appends to a list; nested maps merge recursively;
// `key!: v` replaces a map without merging. Comments in override are ignored.
func ApplyOverride(base []byte, override string) ([]byte, error) {
	return applyOverride(base, override)
}

// Diff returns a line diff between two texts (for override preview).
func Diff(a, b []byte) []model.DiffLine { return diff(a, b) }
