//go:build !darwin

package app

import (
	"path/filepath"
	"strings"
)

// iconSubject is what ProcessIcon draws the icon of for an executable at
// path: the executable itself (its embedded icon on Windows, the icon
// theme's on Linux).
func iconSubject(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	return filepath.Clean(path)
}
