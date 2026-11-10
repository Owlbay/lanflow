//go:build !windows

package service

import (
	"os"
	"syscall"
)

// PrepareLogDir creates LogDir. On macOS it lives in /Library/Logs, which
// only root can write, so no further hardening is needed.
func PrepareLogDir() error { return os.MkdirAll(LogDir, 0o755) }

// OpenAdminLog opens a log file inside LogDir for appending, refusing a
// symlink.
func OpenAdminLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o644)
}
