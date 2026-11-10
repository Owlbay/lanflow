//go:build windows

package netx

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/windows"
)

// Not defined by x/sys/windows (ws2ipdef.h).
const (
	ipUnicastIF   = 31 // IP_UNICAST_IF
	ipv6UnicastIF = 31 // IPV6_UNICAST_IF
)

// bindToInterface sets IP_UNICAST_IF / IPV6_UNICAST_IF on fd. The IPv4
// option takes the index in network byte order, the IPv6 one in host order.
func bindToInterface(network, _ string, fd uintptr, index int) error {
	h := windows.Handle(fd)
	switch network {
	case "tcp4", "udp4":
		return windows.SetsockoptInt(h, windows.IPPROTO_IP, ipUnicastIF, int(networkOrder(uint32(index))))
	case "tcp6", "udp6":
		return windows.SetsockoptInt(h, windows.IPPROTO_IPV6, ipv6UnicastIF, index)
	}
	return fmt.Errorf("unsupported network %q", network)
}

// networkOrder returns v as the host-order integer whose in-memory bytes
// are v in big-endian (htonl).
func networkOrder(v uint32) uint32 {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return binary.NativeEndian.Uint32(b[:])
}
