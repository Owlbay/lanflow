//go:build !windows

package geo

// osLocalZone has nothing to add on Unix: LocalZone reads TZ and
// /etc/localtime itself.
func osLocalZone() string { return "" }
