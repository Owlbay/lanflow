//go:build !darwin && !windows

package autostart

func apply(string, bool) (bool, error) { return false, nil }
func enabled(string) bool              { return false }
