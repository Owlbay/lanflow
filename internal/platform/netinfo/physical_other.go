//go:build !darwin && !windows

package netinfo

import "context"

// physicalInterface is not implemented here: callers report the location
// as unknown rather than measure through whatever route the OS picks.
func physicalInterface(context.Context) (Physical, error) { return Physical{}, ErrNoPhysical }
