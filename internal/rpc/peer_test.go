//go:build !windows

package rpc

import (
	"context"
	"os"
	"testing"
	"time"

	"lanflow/internal/app/fake"
)

// TestPeerCheck: a connection from another uid is closed before the
// handshake (simulated by expecting a different uid).
func TestPeerCheck(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lf")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	srv, err := Start(dir, Options{API: fake.NewInstant(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	// The socket is private to the user.
	if fi, err := os.Stat(Endpoint(dir)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, %v", fi.Mode(), err)
	}
	c, err := Dial(context.Background(), DialOptions{DataDir: dir, Client: "ctl"})
	if err != nil {
		t.Fatalf("same user: %v", err)
	}
	c.Close()

	uidOverride.Store(int64(os.Getuid() + 1))
	defer uidOverride.Store(-1)
	if _, err := Dial(context.Background(), DialOptions{DataDir: dir, Client: "ctl", Timeout: 2 * time.Second}); err == nil {
		t.Fatal("a peer of another uid was accepted")
	}
	// A second server on the same socket is refused while the first runs.
	if _, err := Listen(dir); err != ErrInUse {
		t.Fatalf("second Listen: %v", err)
	}
}
