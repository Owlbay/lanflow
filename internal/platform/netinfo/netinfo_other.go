//go:build !darwin && !windows

package netinfo

import (
	"context"
	"net"
	"os/exec"

	"lanflow/internal/model"
)

func prepareCommand(*exec.Cmd) {}

// get on unsupported systems reports only what the standard library can
// tell: the LAN address and the interface that owns it.
func get(context.Context) (model.NetInfo, error) {
	var info model.NetInfo
	info.LANIP = lanAddress()
	if info.LANIP == "" {
		return info, nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return info, nil
	}
	for _, ifi := range ifaces {
		v4, v6 := interfaceAddrs(ifi.Name)
		if v4 == info.LANIP {
			info.Interface = ifi.Name
			info.IPv6 = v6
			break
		}
	}
	return info, nil
}
