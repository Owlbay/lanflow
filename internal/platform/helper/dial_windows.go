//go:build windows

package helper

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"

	"lanflow/internal/platform/helper/protocol"
)

func platformDial(ctx context.Context) (net.Conn, error) {
	return winio.DialPipeContext(ctx, protocol.PipeName)
}
