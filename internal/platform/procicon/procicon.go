// Package procicon draws the icon of an application on disk, for the
// process column of the connection views: the icon the file manager shows
// for it, through MyGo's Shell module (NSWorkspace on macOS,
// IShellItemImageFactory on Windows, the icon theme on Linux), without
// cgo.
package procicon

import "github.com/egoist/mygo"

// Load returns the icon of the file or application bundle at path as a
// PNG of px by px pixels. It hops to the UI thread, so call it from a
// background goroutine, never from the UI thread before the app runs. It
// fails where the platform has no icons (browsers, unsupported
// platforms) and for a path without one.
func Load(path string, px int) ([]byte, error) {
	return mygo.Shell.FileIcon(path, px)
}
