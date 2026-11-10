//go:build windows

package netinfo

import "context"

func ipv6Route(ctx context.Context) V6Route {
	out, err := runCommand(ctx, "route", "print", "-6")
	if err != nil {
		return V6Route{}
	}
	return parseWindowsV6Routes(out)
}
