//go:build darwin

package netx

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// bindToInterface sets IP_BOUND_IF / IPV6_BOUND_IF on fd, so the socket
// leaves through interface index whatever the routing table prefers.
func bindToInterface(network, _ string, fd uintptr, index int) error {
	switch network {
	case "tcp4", "udp4":
		return unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, index)
	case "tcp6", "udp6":
		return unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, index)
	}
	return fmt.Errorf("unsupported network %q", network)
}
