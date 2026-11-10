//go:build windows

package core

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

// A pipe served by a process this app did not start as a core (here the
// test itself) is refused; once the server is a tracked core it is accepted.
func TestDialChecksControllerServer(t *testing.T) {
	addr := fmt.Sprintf(`\\.\pipe\lanflow-test-%d`, time.Now().UnixNano())
	l, err := winio.ListenPipe(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c, err := dial(ctx, addr); err == nil {
		c.Close()
		t.Fatal("dial accepted a pipe served by an unknown process")
	} else if !strings.Contains(err.Error(), "不是澜流启动的 Mihomo 内核") {
		t.Fatalf("dial error: %v", err)
	}
	trackServer(os.Getpid())
	defer untrackServer(os.Getpid())
	c, err := dial(ctx, addr)
	if err != nil {
		t.Fatalf("dial to a tracked core: %v", err)
	}
	c.Close()
}
