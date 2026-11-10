// Package split runs Lanflow as two processes (docs/service-split.md S2,
// S3): the service process (`Lanflow --service`: app.Service, tray,
// shortcuts, notifications, the local interface) and the window process
// (the default: the window, a client of the local interface). This package
// holds what both sides need apart from MyGo: the command line, starting
// one process from the other, the window's connection to the service and
// the notices kept for the window.
package split

import (
	"os"
	"path/filepath"
	"strings"
)

// Command-line flags.
const (
	// ServiceFlag runs the service process.
	ServiceFlag = "--service"
	// LoginFlag marks a service started at login (autostart): it opens the
	// window unless Settings.SilentStart.
	LoginFlag = "--login"
	// OpenFlag asks a window process to open something once up
	// (view.OpenFeedback, view.OpenTUN, view.OpenLogs, a deep link).
	OpenFlag = "--open="
)

// Args is what the command line asks for.
type Args struct {
	Service bool
	Login   bool
	Open    string // --open=…
	Link    string // a lanflow:// or clash:// argument (Windows deep links)
}

// ParseArgs reads the flags of a launch (os.Args[1:]).
func ParseArgs(args []string) Args {
	var a Args
	for _, s := range args {
		switch {
		case s == ServiceFlag:
			a.Service = true
		case s == LoginFlag:
			a.Login = true
		case strings.HasPrefix(s, OpenFlag):
			a.Open = strings.TrimPrefix(s, OpenFlag)
		case IsLink(s):
			a.Link = s
		}
	}
	return a
}

// IsLink reports whether s is a deep link Lanflow handles.
func IsLink(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "lanflow:") || strings.HasPrefix(l, "clash:")
}

// SingleFile is the marker in the data directory that keeps Lanflow in one
// process, the way it ran before the split (troubleshooting).
const SingleFile = "single-process"

// SingleProcess reports whether Lanflow should run as one process:
// LANFLOW_SINGLE=1, or the marker file <dataDir>/single-process.
func SingleProcess(dataDir string) bool {
	switch os.Getenv("LANFLOW_SINGLE") {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	_, err := os.Stat(filepath.Join(dataDir, SingleFile))
	return err == nil
}
