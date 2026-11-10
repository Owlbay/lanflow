package model

import "time"

// Configuration sync (docs/config-sync.md): the user's configuration as
// one declarative file, lanflow.yaml, in a folder another program syncs
// (iCloud Drive, OneDrive…). Secrets stay in the system keychain; the
// file names them as keychain:<id>.

// Sync modes (ConfigSyncSettings.Mode).
const (
	SyncOff    = ""       // no sync
	SyncICloud = "icloud" // ~/Library/Mobile Documents/com~apple~CloudDocs/Lanflow (macOS)
	SyncFolder = "folder" // ConfigSyncSettings.Dir
)

// ConfigSyncSettings is where this machine syncs its configuration
// (Settings.Sync). It is machine-local and never written to the file.
type ConfigSyncSettings struct {
	Mode string `json:"mode,omitempty"` // SyncOff, SyncICloud or SyncFolder
	Dir  string `json:"dir,omitempty"`  // the folder for SyncFolder
}

// Sync states (ConfigSyncStatus.State).
const (
	SyncStateOff     = "off"     // sync is off
	SyncStateOK      = "ok"      // the file and this machine agree
	SyncStateWaiting = "waiting" // iCloud is still downloading the file
	SyncStateError   = "error"   // the last external change was refused, or the file cannot be written
)

// ConfigSyncStatus is the cheap read of the sync state (API.ConfigSyncStatus).
type ConfigSyncStatus struct {
	Mode  string // SyncOff, SyncICloud or SyncFolder
	Dir   string // the folder in use, "" when off
	Path  string // Dir/lanflow.yaml, "" when off
	State string // SyncState*

	// ICloudAvailable: this is macOS and iCloud Drive's folder exists;
	// ICloudDir is the folder the iCloud mode uses.
	ICloudAvailable bool
	ICloudDir       string

	LastAppliedAt time.Time // the last external change loaded
	LastWrittenAt time.Time // the last time this machine wrote the file
	Summary       string    // what the last external change did, "" when none

	// Error is why the last external change was refused (or the file
	// could not be written); ErrorLine is the line it points at, 0 when
	// unknown. The running configuration was left as it was.
	Error     string
	ErrorLine int

	// Conflicts are copies the sync service left beside the file
	// ("lanflow 2.yaml"); the user picks which one to keep
	// (API.ResolveConfigConflict). Nothing is deleted automatically.
	Conflicts []string

	// MissingSecrets names the subscriptions whose link is a keychain
	// reference this machine's keychain does not have: they wait for the
	// user to enter the link again (Subscription.NeedsURL).
	MissingSecrets []string
	// WebDAVPasswordMissing: the file says a WebDAV password is set, and
	// this machine's keychain has none.
	WebDAVPasswordMissing bool
}

// Answers to the choice when sync is turned on beside an existing file
// (API.SetConfigSync).
const (
	SyncUseFile  = "file"  // load the file, replacing this machine's configuration
	SyncUseLocal = "local" // write this machine's configuration over the file
)

// ConfigApplyResult is what API.ApplyConfigFile did or, for a dry run,
// would do.
type ConfigApplyResult struct {
	Applied bool       // false for a dry run or when nothing changed
	Changed []string   // the parts that change: "settings", "overrides", "customRules", "subscriptions"
	Diff    []DiffLine // this machine's configuration → the file, as lanflow.yaml text
	Summary string     // one sentence for people
}
