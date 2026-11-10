package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

// Conn is a client connection to the local interface. It is safe for
// concurrent use; calls may overlap.
type Conn struct {
	c       net.Conn
	wmu     sync.Mutex
	mu      sync.Mutex
	next    int64
	pending map[string]chan message
	onEvent func(topic string)
	onUI    func(UICommand)
	closed  chan struct{}

	// Notifications go to their handlers in order on a goroutine of
	// their own: a handler may call the server (the window takes the
	// notices on TopicNotice), whose reply the reading goroutine delivers.
	qmu     sync.Mutex
	qcond   *sync.Cond
	queue   []func()
	qdone   bool
	err     error // why the connection ended
	welcome Welcome
}

// DialOptions describe a client.
type DialOptions struct {
	DataDir string // where the socket lives (unused on Windows)
	Client  string // ui, ctl or mcp:<name>
	Version string // this program's Lanflow version
	Reveal  bool   // ask for sensitive reads (ctl --reveal)
	// OnEvent receives "changed" notifications after Subscribe, in order,
	// on a goroutine of their own (it may call the server).
	OnEvent func(topic string)
	// OnUI receives the service's "ui" commands (the window process,
	// AttachUI), in order with the events.
	OnUI    func(UICommand)
	Timeout time.Duration // dial and handshake, default 3 s
}

// Dial connects and performs the handshake. ErrNotRunning means Lanflow
// is not running.
func Dial(ctx context.Context, o DialOptions) (*Conn, error) {
	if o.Timeout == 0 {
		o.Timeout = 3 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	nc, err := dialEndpoint(dctx, o.DataDir)
	if err != nil {
		return nil, err
	}
	if err := verify(nc); err != nil {
		_ = nc.Close()
		return nil, err
	}
	c := newConn(nc, o.OnEvent, o.OnUI)
	var w Welcome
	if err := c.Call(dctx, "hello", Hello{Protocol: ProtocolVersion, Version: o.Version, Client: o.Client, Reveal: o.Reveal}, &w); err != nil {
		_ = c.Close()
		return nil, err
	}
	c.welcome = w
	return c, nil
}

// NewConn wraps an established connection without a handshake (tests).
func NewConn(nc net.Conn, onEvent func(string)) *Conn { return newConn(nc, onEvent, nil) }

func newConn(nc net.Conn, onEvent func(string), onUI func(UICommand)) *Conn {
	c := &Conn{c: nc, pending: map[string]chan message{}, onEvent: onEvent, onUI: onUI, closed: make(chan struct{})}
	c.qcond = sync.NewCond(&c.qmu)
	go c.dispatch()
	go c.read()
	return c
}

// Welcome is the server's handshake answer.
func (c *Conn) Welcome() Welcome { return c.welcome }

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.closed }

// Close ends the connection.
func (c *Conn) Close() error { return c.c.Close() }

// Err is why the connection ended (nil while it is open).
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Conn) read() {
	sc := bufio.NewScanner(c.c)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		if m.Method != "" && len(m.ID) == 0 {
			switch {
			case m.Method == "changed" && c.onEvent != nil:
				var ch Changed
				if json.Unmarshal(m.Params, &ch) == nil {
					c.post(func() { c.onEvent(ch.Topic) })
				}
			case m.Method == "ui" && c.onUI != nil:
				var u UICommand
				if json.Unmarshal(m.Params, &u) == nil {
					c.post(func() { c.onUI(u) })
				}
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[string(m.ID)]
		delete(c.pending, string(m.ID))
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	err := sc.Err()
	if err == nil {
		err = errors.New("澜流已关闭本机接口连接")
	}
	c.mu.Lock()
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.closed)
	c.qmu.Lock()
	c.qdone = true
	c.qcond.Signal()
	c.qmu.Unlock()
}

// post queues a notification handler.
func (c *Conn) post(fn func()) {
	c.qmu.Lock()
	c.queue = append(c.queue, fn)
	c.qcond.Signal()
	c.qmu.Unlock()
}

// dispatch runs the queued handlers in order until the connection ends
// and the queue is empty.
func (c *Conn) dispatch() {
	for {
		c.qmu.Lock()
		for len(c.queue) == 0 && !c.qdone {
			c.qcond.Wait()
		}
		if len(c.queue) == 0 {
			c.qmu.Unlock()
			return
		}
		fn := c.queue[0]
		c.queue[0] = nil
		c.queue = c.queue[1:]
		c.qmu.Unlock()
		fn()
	}
}

// Call sends method with params (marshalled as is: an array for the API
// methods, an object for hello) and decodes the result into out (nil to
// ignore it). The returned error is an *Error for errors of the server.
func (c *Conn) Call(ctx context.Context, method string, params any, out any) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	ch := make(chan message, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.next++
	id := strconv.FormatInt(c.next, 10)
	c.pending[id] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(message{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: raw})
	c.wmu.Lock()
	_, err := c.c.Write(append(b, '\n'))
	c.wmu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			c.mu.Lock()
			err := c.err
			c.mu.Unlock()
			return err
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, out); err != nil {
				return fmt.Errorf("%s: 无法解析结果: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// CallAPI calls an app.API method: args in declaration order, outs
// receive the non-error results in order.
func (c *Conn) CallAPI(ctx context.Context, method string, args []any, outs ...any) error {
	if args == nil {
		args = []any{}
	}
	var res []json.RawMessage
	if err := c.Call(ctx, method, args, &res); err != nil {
		return err
	}
	for i, o := range outs {
		if i >= len(res) || o == nil {
			continue
		}
		if err := json.Unmarshal(res[i], o); err != nil {
			return fmt.Errorf("%s: 无法解析结果 %d: %w", method, i, err)
		}
	}
	return nil
}

// Subscribe asks for "changed" notifications of topics (none = all).
func (c *Conn) Subscribe(ctx context.Context, topics ...string) error {
	if topics == nil {
		topics = []string{}
	}
	return c.Call(ctx, "subscribe", []any{topics}, nil)
}
