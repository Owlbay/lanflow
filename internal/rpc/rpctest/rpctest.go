// Package rpctest serves a fake backend on the local interface for the
// tests of ctl and mcp.
package rpctest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"lanflow/internal/app/fake"
	"lanflow/internal/app/remote"
	"lanflow/internal/rpc"
)

// Server is a running fake.
type Server struct {
	Dir  string
	Fake *fake.Fake
	Srv  *rpc.Server
}

// Start serves fake.NewInstant() in a fresh directory; prompt may be nil.
func Start(t testing.TB, prompt rpc.PromptFunc) *Server {
	t.Helper()
	base := ""
	if runtime.GOOS != "windows" {
		base = "/tmp" // unix socket paths are limited to ~104 bytes
	}
	dir, err := os.MkdirTemp(base, "lf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if runtime.GOOS == "windows" {
		t.Setenv("LANFLOW_SOCKET", `\\.\pipe\lanflow-test-`+filepath.Base(dir))
	}
	s := &Server{Dir: dir, Fake: fake.NewInstant()}
	srv, err := rpc.Start(dir, rpc.Options{API: s.Fake, Version: "test", AuditDir: filepath.Join(dir, "logs"), Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	s.Srv = srv
	t.Cleanup(func() { srv.Close() })
	return s
}

// Dial connects as client.
func (s *Server) Dial(client string, reveal bool) (*remote.Client, error) {
	c, err := remote.Dial(context.Background(), remote.Options{DataDir: s.Dir, Client: client, Version: "test", Reveal: reveal})
	if err != nil {
		return nil, err
	}
	c.Timeout = 10 * time.Second
	return c, nil
}
