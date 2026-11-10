//go:build !darwin && !windows

package netx

import "errors"

func bindToInterface(string, string, uintptr, int) error {
	return errors.New("binding to an interface is not implemented on this system")
}
