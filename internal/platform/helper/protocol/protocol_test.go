package protocol

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func startServer(t *testing.T, h Handler, auth func(net.Conn) error) *Client {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := &Server{Handler: h, Authorize: auth}
	go func() { _ = srv.Serve(ctx, l) }()
	addr := l.Addr().String()
	return &Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}}
}

func TestRoundTrip(t *testing.T) {
	var got Request
	c := startServer(t, func(_ context.Context, req Request) Response {
		got = req
		switch req.Op {
		case OpPing:
			return Response{OK: true, Version: "1.0", Protocol: Version}
		case OpStart:
			return Response{OK: true, Running: true, PID: 42}
		}
		return Response{Error: "bad op"}
	}, nil)
	ctx := context.Background()

	resp, err := c.Call(ctx, Request{Op: OpPing})
	if err != nil || !resp.OK || resp.Version != "1.0" || resp.Protocol != Version {
		t.Fatalf("ping: %+v %v", resp, err)
	}
	spec := &Spec{Bin: "/b", WorkDir: "/w", Config: "/c", Controller: "/s", LogFile: "/l", Env: []string{"A=1"}}
	resp, err = c.Call(ctx, Request{Op: OpStart, Spec: spec})
	if err != nil || !resp.Running || resp.PID != 42 {
		t.Fatalf("start: %+v %v", resp, err)
	}
	if got.V != Version || got.Spec == nil || len(got.Spec.Env) != 1 || got.Spec.Env[0] != "A=1" || got.Spec.Controller != "/s" {
		t.Fatalf("server saw %+v", got)
	}
	resp, err = c.Call(ctx, Request{Op: "nope"})
	if err == nil || resp.OK || !strings.Contains(err.Error(), "bad op") {
		t.Fatalf("expected application error, got %+v %v", resp, err)
	}
}

func TestVersionMismatch(t *testing.T) {
	c := startServer(t, func(context.Context, Request) Response { return Response{OK: true} }, nil)
	conn, err := c.Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"v":99,"op":"ping"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), `"ok":false`) || !strings.Contains(string(buf[:n]), "协议版本") {
		t.Fatalf("got %s", buf[:n])
	}
}

func TestAuthorizeRejects(t *testing.T) {
	c := startServer(t, func(context.Context, Request) Response { return Response{OK: true} },
		func(net.Conn) error { return context.Canceled })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Call(ctx, Request{Op: OpPing}); err == nil {
		t.Fatal("expected failure when authorizer rejects")
	}
}

func TestClientContextTimeout(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	c := startServer(t, func(context.Context, Request) Response { <-block; return Response{OK: true} }, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Call(ctx, Request{Op: OpPing}); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("call did not honour context deadline")
	}
}
