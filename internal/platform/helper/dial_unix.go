//go:build !windows

package helper

import (
	"context"
	"net"

	"lanflow/internal/platform/helper/protocol"
)

func platformDial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", protocol.SocketPath)
}
