// Package remote is app.API over the local interface (internal/rpc): the
// client the window will use once it runs in its own process
// (docs/service-split.md S2), and the one `lanflow ctl` and `lanflow mcp`
// use now.
//
// client_gen.go holds the generated methods; OnChange and UpdateUIState
// are written here.
package remote

//go:generate go run ./apigen/gen

import (
	"context"
	"errors"
	"sync"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/model"
	"lanflow/internal/rpc"
)

// Client implements app.API by calling a running Lanflow.
//
// Reads (methods without an error result) return zero values when the
// call fails; LastError says why. Actions return the server's error, with
// app.ErrHelperRequired mapped back so callers can test for it.
type Client struct {
	conn *rpc.Conn
	// Timeout bounds every call; 0 = none (actions such as delay tests
	// may take a while).
	Timeout time.Duration

	mu      sync.Mutex
	lastErr error
	nextFn  int
	fns     map[int]func(app.Topic)
	subbed  bool
}

// Options describe the client to the server.
type Options struct {
	DataDir string // where the socket lives (unused on Windows)
	Client  string // ui, ctl or mcp:<name>
	Version string // this program's Lanflow version
	Reveal  bool   // ask for sensitive reads (ctl --reveal)
}

// Dial connects to the running Lanflow. rpc.ErrNotRunning means it is not
// running.
func Dial(ctx context.Context, o Options) (*Client, error) {
	c := &Client{fns: map[int]func(app.Topic){}}
	conn, err := rpc.Dial(ctx, rpc.DialOptions{DataDir: o.DataDir, Client: o.Client, Version: o.Version, Reveal: o.Reveal, OnEvent: c.event})
	if err != nil {
		return nil, err
	}
	c.conn = conn
	return c, nil
}

// Conn is the underlying connection, for the methods outside app.API
// (PatchSettings, Undo, AuditLog, Methods).
func (cl *Client) Conn() *rpc.Conn { return cl.conn }

// Close ends the connection.
func (cl *Client) Close() error { return cl.conn.Close() }

// LastError is the error of the most recent failed call.
func (cl *Client) LastError() error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.lastErr
}

func (cl *Client) call(method string, args []any, outs ...any) error {
	ctx := context.Background()
	if cl.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cl.Timeout)
		defer cancel()
	}
	err := cl.conn.CallAPI(ctx, method, args, outs...)
	if err != nil {
		var e *rpc.Error
		if errors.As(err, &e) && e.Data != nil && e.Data.Kind == "helper-required" {
			err = app.ErrHelperRequired
		}
		cl.mu.Lock()
		cl.lastErr = err
		cl.mu.Unlock()
	}
	return err
}

// OnChange registers fn for the server's "changed" events; the first
// registration subscribes to every topic.
func (cl *Client) OnChange(fn func(app.Topic)) (off func()) {
	cl.mu.Lock()
	id := cl.nextFn
	cl.nextFn++
	cl.fns[id] = fn
	sub := !cl.subbed
	cl.subbed = true
	cl.mu.Unlock()
	if sub {
		if err := cl.conn.Subscribe(context.Background()); err != nil {
			cl.mu.Lock()
			cl.subbed, cl.lastErr = false, err
			cl.mu.Unlock()
		}
	}
	return func() {
		cl.mu.Lock()
		delete(cl.fns, id)
		cl.mu.Unlock()
	}
}

func (cl *Client) event(topic string) {
	cl.mu.Lock()
	fns := make([]func(app.Topic), 0, len(cl.fns))
	for _, fn := range cl.fns {
		fns = append(fns, fn)
	}
	cl.mu.Unlock()
	for _, fn := range fns {
		fn(app.Topic(topic))
	}
}

// UpdateUIState runs fn on a copy of the remote UI state and sends the
// result back whole (PatchUIState): the UI state is only ever changed by
// the window, so there is nothing to merge.
func (cl *Client) UpdateUIState(fn func(*model.UIState)) error {
	var u model.UIState
	if err := cl.call("UIState", nil, &u); err != nil {
		return err
	}
	fn(&u)
	return cl.conn.Call(context.Background(), "PatchUIState", []any{u}, nil)
}
