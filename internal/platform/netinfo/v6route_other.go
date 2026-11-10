//go:build !darwin && !windows

package netinfo

import "context"

func ipv6Route(context.Context) V6Route { return V6Route{} }
