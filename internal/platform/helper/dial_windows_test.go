//go:build windows

package helper

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/Microsoft/go-winio"
)

// A pipe served by an ordinary process (this test) must not be accepted
// as the helper.
func TestVerifyPipeServerRefusesNonSystem(t *testing.T) {
	name := `\\.\pipe\lanflow-verify-test-` + strconv.Itoa(os.Getpid())
	l, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		if c, err := l.Accept(); err == nil {
			defer c.Close()
			var b [1]byte
			_, _ = c.Read(b[:])
		}
	}()
	conn, err := winio.DialPipeContext(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := verifyPipeServer(conn); err == nil {
		t.Fatal("non-SYSTEM pipe server accepted")
	}
}
