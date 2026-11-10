// Package helper installs and talks to lanflow-helper, the privileged
// service that runs Mihomo as root (macOS LaunchDaemon) or LocalSystem
// (Windows service) so TUN mode works without running the whole app
// elevated.
//
// Exported signatures are a stable contract; the implementation is here
// plus cmd/lanflow-helper.
//
// Security requirements (must hold):
//   - The helper listens only on a local endpoint: a unix socket
//     /var/run/lanflow-helper.sock owned by root, chmod 0600 then chown to
//     the installing user's uid (macOS); a named pipe \\.\pipe\lanflow-helper
//     with a DACL granting only SYSTEM, Administrators and the installing
//     user's SID (Windows).
//   - The helper runs only the Mihomo binary recorded at install time
//     (path + SHA-256 in a root/SYSTEM-owned config next to the helper);
//     StartCore with any other binary or a changed hash is refused.
//   - Work dir, config and log paths must be inside the installing user's
//     Lanflow data directory recorded at install time.
//   - Install/uninstall prompt for administrator rights: osascript
//     "do shell script … with administrator privileges" (macOS),
//     ShellExecute "runas" of the helper binary with an install verb (Windows).
//   - Uninstall stops Mihomo, removes the service and its files.
package helper

import (
	"context"

	"lanflow/internal/core"
)

// InstallOptions describe what the helper may run.
type InstallOptions struct {
	HelperBin string // path of the lanflow-helper binary shipped with the app
	CoreBin   string // the Mihomo binary the helper is allowed to run
	DataDir   string // the user's Lanflow data dir (work dir/config/log must be inside)
}

// Installed reports whether the helper is installed and answers a ping
// with this app's protocol and helper version. A helper of another
// version reports false so the app offers to reinstall; Check says why.
func Installed(ctx context.Context) bool { return installed(ctx) }

// Check returns nil when Installed would be true, else an error matching
// ErrNotRunning, ErrProtocolMismatch or ErrVersionMismatch (errors.Is);
// a version mismatch is a *VersionError carrying both versions.
func Check(ctx context.Context) error { return check(ctx) }

// SetBundledHelper records the helper binary shipped with the app: its
// version (`lanflow-helper version`) becomes the version Installed and
// Check require. Call it once at startup unless ExpectedVersion is set at
// build time.
func SetBundledHelper(ctx context.Context, path string) error {
	_, err := learnBundledVersion(ctx, path)
	return err
}

// Install installs (or upgrades) and starts the helper. Prompts for admin
// rights. It refuses (ErrDowngrade) to replace a running helper that is
// newer than HelperBin.
func Install(ctx context.Context, o InstallOptions) error { return install(ctx, o) }

// Uninstall removes the helper. Prompts for admin rights.
func Uninstall(ctx context.Context) error { return uninstall(ctx) }

// NewRunner returns a core.Runner that starts Mihomo through the helper.
// Stop asks the helper to stop it; Exited reports unexpected exits that the
// helper reports (poll Status every second if push is not implemented).
func NewRunner() core.Runner { return newRunner() }
