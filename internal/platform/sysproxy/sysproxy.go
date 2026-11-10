// Package sysproxy sets and clears the OS system proxy.
//
// Exported signatures are a stable contract. Implementations: darwin (networksetup on every enabled network service) and
// windows (WinINet per-user settings in the registry + InternetSetOption
// INTERNET_OPTION_SETTINGS_CHANGED / REFRESH via golang.org/x/sys/windows)
// in build-tagged files, plus a no-op/unsupported implementation elsewhere.
package sysproxy

// State is the current system proxy setting (HTTP/HTTPS/SOCKS as one view).
type State struct {
	Enabled bool
	Host    string
	Port    int
	Bypass  []string
	// PACURL is the automatic proxy configuration URL when PAC is on
	// ("" when off or unset). Enabled does not cover it.
	PACURL string
}

// Set points the HTTP, HTTPS and SOCKS proxies at host:port with bypass.
func Set(host string, port int, bypass []string) error { return set(host, port, bypass) }

// Clear turns the system proxy off (HTTP, HTTPS, SOCKS) without touching PAC settings.
func Clear() error { return clear() }

// SetPAC points the system's automatic proxy configuration (PAC) at url
// and turns it on; "" turns PAC off, leaving the URL stored. The manual
// HTTP/HTTPS/SOCKS proxies are not touched (docs/design.md §5.8 高级 ·
// PAC 模式).
func SetPAC(url string) error { return setPAC(url) }

// Get reads the current setting (from the primary service on macOS).
func Get() (State, error) { return get() }

// Saved is an opaque, JSON-serializable snapshot of the system proxy
// settings taken before Lanflow changes them, so they can be put back
// exactly (another app's proxy, a PAC-less manual proxy, or "off").
type Saved []byte

// Snapshot records the current HTTP/HTTPS/SOCKS proxy settings (every
// enabled network service on macOS; the per-user WinINet values on Windows).
func Snapshot() (Saved, error) { return snapshot() }

// Restore puts back what Snapshot recorded. Services that did not exist at
// snapshot time get their proxies turned off.
func Restore(s Saved) error { return restore(s) }

// DefaultBypass is the default bypass list.
var DefaultBypass = []string{"localhost", "127.0.0.1", "::1", "192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "*.local", "*.lan", "<local>"}
