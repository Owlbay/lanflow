package model

import "time"

// Mihomo core version management (API.CoreReleases / InstalledCores /
// InstallCore / UseCore / RemoveCore / CheckCoreUpdate). Installed cores
// live side by side in <DataDir>/cores/<version>/, each with a meta.json;
// the core shipped with the app stays available as 「内置」.

// Core channels.
const (
	CoreChannelStable = "stable" // GitHub releases of MetaCubeX/mihomo, prereleases excluded
	CoreChannelAlpha  = "alpha"  // the rolling Prerelease-Alpha release
)

// CoreRelease is one downloadable release of the channel.
type CoreRelease struct {
	Channel     string    // CoreChannelStable / CoreChannelAlpha
	Version     string    // "v1.19.33", or "alpha-5a3a0ef" for the Alpha channel
	Tag         string    // GitHub release tag ("v1.19.33" / "Prerelease-Alpha")
	PublishedAt time.Time // zero when GitHub gave none
	Notes       string    // excerpt of the release notes, a few lines at most
	Asset       string    // asset name for this OS/arch
	Size        int64     // asset size, 0 = unknown
	Installed   bool      // already in InstalledCores
}

// InstalledCore is one core the user can switch to.
type InstalledCore struct {
	Version     string // "" for the bundled core when its version is not known yet
	Channel     string // CoreChannelStable / CoreChannelAlpha; "" for the bundled core
	Bundled     bool   // the core shipped with the app (never removable)
	Active      bool   // the core Settings.CorePath/CoreVersion point at
	Path        string // the binary
	Size        int64  // bytes
	InstalledAt time.Time
	SHA256      string // hex; "" for the bundled core
	Source      string // download URL, or "bundled" / "migrated"
}

// CoreUpdateInfo is the result of a CheckCoreUpdate.
type CoreUpdateInfo struct {
	Channel   string    // channel checked
	Current   string    // version in use ("" when unknown)
	Latest    string    // newest version of the channel
	Available bool      // Latest is newer than Current and not installed yet
	Installed bool      // Latest is already installed (switch only)
	Notes     string    // release notes excerpt of Latest
	CheckedAt time.Time // when the check ran
}
