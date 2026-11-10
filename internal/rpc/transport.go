package rpc

import (
	"errors"
	"log"
	"net"
)

var (
	// ErrNotRunning: nothing serves the endpoint (Lanflow is not running).
	ErrNotRunning = errors.New("澜流没有在运行：请先打开澜流，再使用 lanflow ctl 或 MCP")
	// ErrInUse: another process already serves the endpoint.
	ErrInUse = errors.New("本机接口已被另一个澜流进程占用")
)

// peerListener closes connections whose peer is not the current user.
type peerListener struct{ net.Listener }

// verify is the peer check (a variable for tests).
var verify = verifyPeer

func (l *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if err := verify(c); err != nil {
			log.Printf("lanflow: local api: refused a connection: %v", err)
			_ = c.Close()
			continue
		}
		return c, nil
	}
}
