//go:build darwin

package app

// iconSubject is what ProcessIcon draws the icon of for an executable at
// path: its enclosing .app bundle. A bare binary (a command-line tool, a
// daemon) has no icon of its own; the views draw a generic glyph for it.
func iconSubject(path string) string { return appBundleOf(path) }
