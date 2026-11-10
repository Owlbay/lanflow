// Package autostart starts Lanflow's service process when the user logs
// in (docs/service-split.md S3): `<exe> --service --login`, from a launch
// agent on macOS and a Run entry of the user on Windows. It replaces
// MyGo's login item, which opens the app (the window) instead.
package autostart

// Args are what the login starts exe with.
var Args = []string{"--service", "--login"}

// Label names the launch agent (macOS) and the Run value (Windows).
const Label = "lanflow.service"

// Apply makes the login start exe (on) or nothing (off); it only writes
// when the current entry differs. changed reports a write.
func Apply(exe string, on bool) (changed bool, err error) { return apply(exe, on) }

// Enabled reports whether the login starts exe.
func Enabled(exe string) bool { return enabled(exe) }
