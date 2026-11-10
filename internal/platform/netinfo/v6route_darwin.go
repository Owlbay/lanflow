//go:build darwin

package netinfo

import "context"

func ipv6Route(ctx context.Context) V6Route {
	out, err := runCommand(ctx, "netstat", "-rn", "-f", "inet6")
	if err != nil {
		return V6Route{}
	}
	return parseDarwinV6Routes(out)
}
