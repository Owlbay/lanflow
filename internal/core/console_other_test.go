//go:build !windows

package core

// ownsConsole: only Windows needs a console to stop the core gracefully.
func ownsConsole() bool { return false }
