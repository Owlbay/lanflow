// Package remote is app.API over the local interface (internal/rpc): the
// client the window uses when it runs in its own process
// (docs/service-split.md S2), and the one `lanflow ctl` and `lanflow mcp`
// use.
//
// client_gen.go holds the generated methods; OnChange, UpdateUIState and
// the window process's extras (read cache, incremental lists, reconnect,
// window ops) are written here and in cache.go, mirror.go and window.go.
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

// ErrDisconnected is returned while the connection is down and being
// re-established (Options.Reconnect).
var ErrDisconnected = errors.New("与澜流后台服务的连接已断开，正在重新连接")

// Client implements app.API by calling a running Lanflow.
//
// Reads (methods without an error result) return zero values when the
// call fails; LastError says why. Actions return the server's error, with
// app.ErrHelperRequired mapped back so callers can test for it.
type Client struct {
	o Options
	// Timeout bounds every call; 0 = none (actions such as delay tests
	// may take a while).
	Timeout time.Duration

	mu      sync.Mutex
	conn    *rpc.Conn // nil while reconnecting
	lastErr error
	nextFn  int
	fns     map[int]func(app.Topic)
	subbed  bool
	closed  bool

	bgMu   sync.Mutex // SetBackground's sender (window.go)
	bgWant *bool
	bgBusy bool

	cache  *readCache // Options.Cache
	mirror *mirror    // Options.Incremental
}

// Options describe the client to the server.
type Options struct {
	DataDir string // where the socket lives (unused on Windows)
	Client  string // ui, ctl or mcp:<name>
	Version string // this program's Lanflow version
	Reveal  bool   // ask for sensitive reads (ctl --reveal)

	// The window process sets these.

	// Cache keeps the result of every read until a "changed" event of a
	// topic it depends on (cache.go): views read the API on every frame.
	Cache bool
	// ReadTimeout bounds the reads (methods without an error result): the
	// window reads on its main thread and must not hang on a service that
	// stopped answering. 0 = Client.Timeout.
	ReadTimeout time.Duration
	// Incremental serves Connections, ClosedConnections, Logs and AppLogs
	// from a copy kept current with the …Since methods (mirror.go).
	Incremental bool
	// OnUI receives the service's window commands (rpc.UICommand).
	OnUI func(rpc.UICommand)
	// Reconnect, when set, re-establishes a lost connection (window.go).
	Reconnect *Reconnect
}

// Dial connects to the running Lanflow. rpc.ErrNotRunning means it is not
// running.
func Dial(ctx context.Context, o Options) (*Client, error) {
	c := &Client{o: o, fns: map[int]func(app.Topic){}}
	if o.Cache {
		c.cache = newReadCache()
	}
	if o.Incremental {
		c.mirror = newMirror()
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	c.conn = conn
	if o.Reconnect != nil {
		go c.watch(conn)
	}
	return c, nil
}

func (cl *Client) dial(ctx context.Context) (*rpc.Conn, error) {
	return rpc.Dial(ctx, rpc.DialOptions{DataDir: cl.o.DataDir, Client: cl.o.Client, Version: cl.o.Version, Reveal: cl.o.Reveal,
		OnEvent: cl.event, OnUI: cl.o.OnUI})
}

// Conn is the underlying connection, for the methods outside app.API
// (PatchSettings, Undo, AuditLog, Methods); nil while reconnecting.
func (cl *Client) Conn() *rpc.Conn {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.conn
}

// Close ends the connection (and stops reconnecting).
func (cl *Client) Close() error {
	cl.mu.Lock()
	cl.closed = true
	conn := cl.conn
	cl.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// LastError is the error of the most recent failed call.
func (cl *Client) LastError() error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.lastErr
}

func (cl *Client) setErr(err error) {
	cl.mu.Lock()
	cl.lastErr = err
	cl.mu.Unlock()
}

func (cl *Client) ctx(method string) (context.Context, context.CancelFunc) {
	if cl.o.ReadTimeout > 0 && (cacheable(method) || uncachedRead(method)) {
		return context.WithTimeout(context.Background(), cl.o.ReadTimeout)
	}
	if cl.Timeout > 0 {
		return context.WithTimeout(context.Background(), cl.Timeout)
	}
	return context.WithCancel(context.Background())
}

func (cl *Client) call(method string, args []any, outs ...any) error {
	if cl.mirror != nil && cl.mirror.serves(method) {
		return cl.mirror.read(cl, method, outs)
	}
	if cl.cache == nil {
		return cl.raw(method, args, outs...)
	}
	if cl.cache.get(method, args, outs) {
		return nil
	}
	gen := cl.cache.generation()
	err := cl.raw(method, args, outs...)
	switch {
	case cacheable(method):
		if err == nil {
			cl.cache.putAt(gen, method, args, outs)
		}
	case !uncachedRead(method):
		cl.cache.clear() // an action: what it changed shows at once
	}
	return err
}

// raw calls the server, uncached.
func (cl *Client) raw(method string, args []any, outs ...any) error {
	conn := cl.Conn()
	if conn == nil {
		cl.setErr(ErrDisconnected)
		return ErrDisconnected
	}
	ctx, cancel := cl.ctx(method)
	defer cancel()
	err := conn.CallAPI(ctx, method, args, outs...)
	if err != nil {
		var e *rpc.Error
		if errors.As(err, &e) && e.Data != nil && e.Data.Kind == "helper-required" {
			err = app.ErrHelperRequired
		}
		cl.setErr(err)
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
	conn := cl.conn
	cl.mu.Unlock()
	if sub && conn != nil {
		if err := conn.Subscribe(context.Background()); err != nil {
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
	t := app.Topic(topic)
	if cl.cache != nil {
		cl.cache.invalidate(t)
	}
	if cl.mirror != nil {
		cl.mirror.invalidate(t)
	}
	cl.mu.Lock()
	fns := make([]func(app.Topic), 0, len(cl.fns))
	for _, fn := range cl.fns {
		fns = append(fns, fn)
	}
	cl.mu.Unlock()
	for _, fn := range fns {
		fn(t)
	}
}

// UpdateUIState runs fn on a copy of the remote UI state and sends the
// result back whole (PatchUIState): the UI state is only ever changed by
// the window, so there is nothing to merge.
func (cl *Client) UpdateUIState(fn func(*model.UIState)) error {
	var u model.UIState
	if err := cl.raw("UIState", nil, &u); err != nil {
		return err
	}
	fn(&u)
	conn := cl.Conn()
	if conn == nil {
		return ErrDisconnected
	}
	err := conn.Call(context.Background(), "PatchUIState", []any{u}, nil)
	if cl.cache != nil {
		cl.cache.drop("UIState")
	}
	return err
}
