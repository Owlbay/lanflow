package model

import "time"

// Types of the convenience features: global
// shortcut actions, Wi-Fi subscription rules, the GitHub download mirror
// and WebDAV backup. The Settings fields are in model.go.

// Shortcut actions (Settings.Shortcuts). The system-proxy action keeps
// living in Settings.GlobalShortcut (the tray and the shell read it
// there); Shortcuts holds the other actions.
const (
	ShortcutSystemProxy = "sysproxy"    // toggle the system proxy (Settings.GlobalShortcut)
	ShortcutTUN         = "tun"         // toggle TUN mode
	ShortcutCycleMode   = "cycle-mode"  // rule → global → direct → rule
	ShortcutModeRule    = "mode-rule"   // set rule mode
	ShortcutModeGlobal  = "mode-global" // set global mode
	ShortcutModeDirect  = "mode-direct" // set direct mode
	ShortcutWindow      = "window"      // show / hide the main window
	ShortcutTestDelay   = "test-delay"  // test the current group's delay
)

// ShortcutActions lists every action in display order.
var ShortcutActions = []string{
	ShortcutSystemProxy, ShortcutTUN, ShortcutCycleMode,
	ShortcutModeRule, ShortcutModeGlobal, ShortcutModeDirect,
	ShortcutWindow, ShortcutTestDelay,
}

// Shortcut binds an accelerator (MyGo spelling, "CmdOrCtrl+Shift+T") to
// an action; an empty accelerator keeps the action without keys.
type Shortcut struct {
	Action      string `json:"action"`
	Accelerator string `json:"accelerator"`
}

// ShortcutOf returns the accelerator stored for action: GlobalShortcut
// for the system-proxy action, the Shortcuts entry otherwise ("" = none).
func (s Settings) ShortcutOf(action string) string {
	if action == ShortcutSystemProxy {
		return s.GlobalShortcut
	}
	for _, sc := range s.Shortcuts {
		if sc.Action == action {
			return sc.Accelerator
		}
	}
	return ""
}

// WithShortcut returns a copy of s with action bound to acc ("" = none).
// The slice is never shared with s, so snapshots stay immutable.
func (s Settings) WithShortcut(action, acc string) Settings {
	if action == ShortcutSystemProxy {
		s.GlobalShortcut = acc
		return s
	}
	var out []Shortcut
	found := false
	for _, sc := range s.Shortcuts {
		if sc.Action == action {
			sc.Accelerator = acc
			found = true
		}
		if sc.Action != ShortcutSystemProxy {
			out = append(out, sc)
		}
	}
	if !found {
		out = append(out, Shortcut{Action: action, Accelerator: acc})
	}
	s.Shortcuts = out
	return s
}

// SSIDRule switches to a subscription when the Wi-Fi network named SSID
// is joined (Settings.SSIDRules).
type SSIDRule struct {
	SSID           string `json:"ssid"`
	SubscriptionID string `json:"subscriptionId"`
}

// WebDAVSettings is the WebDAV backup configuration (Settings.WebDAV).
// The password is not here: it lives in the system keychain (or an
// obfuscated 0600 file where there is none) and is set through
// API.SetWebDAVPassword, so settings files and exports never carry it.
type WebDAVSettings struct {
	URL  string `json:"url,omitempty"`  // server, e.g. https://dav.example.com/dav
	User string `json:"user,omitempty"` // account name
	Dir  string `json:"dir,omitempty"`  // remote directory, default "lanflow"
	// Keep is how many backups to keep on the server; older ones are
	// pruned after a successful backup (0 = keep all).
	Keep int `json:"keep,omitempty"`
	// AutoBackup is "" (off), WebDAVDaily or WebDAVWeekly.
	AutoBackup string `json:"autoBackup,omitempty"`
	// LastBackupAt is when the last backup (manual or automatic) succeeded.
	LastBackupAt time.Time `json:"lastBackupAt,omitempty"`
}

// Auto backup intervals (WebDAVSettings.AutoBackup).
const (
	WebDAVDaily  = "daily"
	WebDAVWeekly = "weekly"
)

// WebDAVDefaultDir is the remote directory when none is set.
const WebDAVDefaultDir = "lanflow"

// WebDAVBackup is one backup file on the server (API.ListBackups).
type WebDAVBackup struct {
	Name     string    // file name, lanflow-backup-YYYYMMDD-HHMMSS.json
	Size     int64     // bytes, 0 when the server did not say
	Modified time.Time // server's modification time; zero when unknown
}

// WebDAVInfo is the cheap read of the backup state (API.WebDAVInfo).
type WebDAVInfo struct {
	// PasswordStore says where the password lives: "keychain", "file"
	// (obfuscated, settings directory, mode 0600) or "" (no password).
	PasswordStore string
	HasPassword   bool
	Busy          bool      // a backup, restore or listing is running
	LastBackupAt  time.Time // last successful backup
	LastError     string    // last failed automatic backup, "" when none
}

// Password store kinds (WebDAVInfo.PasswordStore).
const (
	PasswordStoreKeychain = "keychain"
	PasswordStoreFile     = "file"
)

// MirrorTest is the result of testing one download mirror
// (API.TestMirrors): the round trip of a HEAD request, -1 on failure.
type MirrorTest struct {
	URL   string
	Ms    int
	Error string
}
