//go:build windows

package service

import (
	"io"
	"os"
)

// openLogFile rotates path at LogRotateSize (keeping path.1) and opens it
// for appending.
func openLogFile(path string, _ string) (io.WriteCloser, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Size() >= LogRotateSize {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
}
