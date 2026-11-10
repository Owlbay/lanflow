//go:build !darwin && !windows

package trust

import "errors"

var errUnsupported = errors.New("installing the capture root is not supported on this system")

func install([]byte) error { return errUnsupported }
func remove([]byte) error  { return nil }
func find() []string       { return nil }
