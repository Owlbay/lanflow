package remote

import (
	"context"
	"errors"
	"log"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/rpc"
)

// The window process's side of the split app (docs/service-split.md S2):
// reconnecting after the service process went away, and the window ops
// (internal/rpc/windowops.go).

// Reconnect configures how a lost connection is re-established.
type Reconnect struct {
	// Start is called when the service is not running (rpc.ErrNotRunning)
	// and should start it; nil only waits for it. A call returning an
	// error is retried at the next attempt.
	Start func() error
	// Ready runs on every new connection before the client uses it (the
	// window attaches itself, AttachUI); an error drops the connection
	// and the next attempt starts over.
	Ready func(c *Client, conn *rpc.Conn) error
	// OnState is told when the connection goes down (err says why) and
	// comes back (err nil). It runs on the reconnecting goroutine.
	OnState func(up bool, err error)
	// Hold reports whether to stay down (the service is quitting on
	// purpose); checked before every attempt.
	Hold func() bool
}

// retry is the pause between attempts.
var retry = []time.Duration{100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second}

// watch waits for conn to end and reconnects.
func (cl *Client) watch(conn *rpc.Conn) {
	<-conn.Done()
	cl.mu.Lock()
	if cl.conn == conn {
		cl.conn = nil
	}
	closed := cl.closed
	cl.mu.Unlock()
	if closed {
		return
	}
	r := cl.o.Reconnect
	if r.OnState != nil {
		r.OnState(false, conn.Err())
	}
	cl.forgetAll()
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			time.Sleep(retry[min(attempt-1, len(retry)-1)])
		}
		cl.mu.Lock()
		closed := cl.closed
		cl.mu.Unlock()
		if closed {
			return
		}
		if r.Hold != nil && r.Hold() {
			continue
		}
		next, err := cl.dial(context.Background())
		if errors.Is(err, rpc.ErrNotRunning) && r.Start != nil {
			if serr := r.Start(); serr != nil {
				log.Printf("lanflow: start the service: %v", serr)
			}
			continue
		}
		if err != nil {
			continue
		}
		if r.Ready != nil {
			if err := r.Ready(cl, next); err != nil {
				log.Printf("lanflow: reconnect: %v", err)
				_ = next.Close()
				continue
			}
		}
		cl.mu.Lock()
		if cl.closed {
			cl.mu.Unlock()
			_ = next.Close()
			return
		}
		cl.conn = next
		subbed := cl.subbed
		cl.mu.Unlock()
		if subbed {
			if err := next.Subscribe(context.Background()); err != nil {
				_ = next.Close()
				go cl.watch(next)
				return
			}
		}
		cl.forgetAll()
		if r.OnState != nil {
			r.OnState(true, nil)
		}
		// Every snapshot may have changed: refresh all of them.
		for _, t := range allTopics {
			cl.event(string(t))
		}
		go cl.watch(next)
		return
	}
}

func (cl *Client) forgetAll() {
	if cl.cache != nil {
		cl.cache.reset()
	}
	if cl.mirror != nil {
		cl.mirror.reset()
	}
}

var allTopics = []app.Topic{app.TopicStatus, app.TopicTraffic, app.TopicProxies, app.TopicSubs, app.TopicConns, app.TopicLogs,
	app.TopicRules, app.TopicExits, app.TopicNet, app.TopicSettings, app.TopicNotice, app.TopicCoreDownload, app.TopicCores,
	app.TopicProbe, app.TopicStats, app.TopicProcIcons}

// windowCall calls a window op on conn (the current one when nil).
func (cl *Client) windowCall(conn *rpc.Conn, method string, args []any, out any) error {
	if conn == nil {
		conn = cl.Conn()
	}
	if conn == nil {
		return ErrDisconnected
	}
	if args == nil {
		args = []any{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return conn.Call(ctx, method, args, out)
}

// AttachUI makes this process the window (rpc.AttachResult); conn is the
// connection to use (Reconnect.Ready passes the new one), nil = current.
func (cl *Client) AttachUI(conn *rpc.Conn, arg string) (rpc.AttachResult, error) {
	var res []rpc.AttachResult
	if err := cl.windowCall(conn, "AttachUI", []any{arg}, &res); err != nil {
		return rpc.AttachResult{}, err
	}
	if len(res) == 0 {
		return rpc.AttachResult{}, errors.New("AttachUI: empty result")
	}
	return res[0], nil
}

// DetachUI tells the service the window is closing.
func (cl *Client) DetachUI() error { return cl.windowCall(nil, "DetachUI", nil, nil) }

// QuitService quits Lanflow: the service process restores the system
// proxy, stops and tells the window to quit (rpc.UIQuit).
func (cl *Client) QuitService() error { return cl.windowCall(nil, "QuitService", nil, nil) }

// SetBackground passes the window's visibility to the service (the
// optional interface the shell looks for).
func (cl *Client) SetBackground(hidden bool) {
	if err := cl.windowCall(nil, "SetBackground", []any{hidden}, nil); err != nil {
		cl.setErr(err)
	}
}

// SetShortcut registers a global shortcut in the service process.
func (cl *Client) SetShortcut(action, acc string) error {
	return cl.windowCall(nil, "SetShortcut", []any{action, acc}, nil)
}

// CacheUsage and CleanCaches are the optional cache API of 设置 › 数据.
func (cl *Client) CacheUsage() []app.CacheItem {
	var out [][]app.CacheItem
	if err := cl.windowCall(nil, "CacheUsage", nil, &out); err != nil || len(out) == 0 {
		return nil
	}
	return out[0]
}

func (cl *Client) CleanCaches() (int64, error) {
	var out []int64
	if err := cl.windowCall(nil, "CleanCaches", nil, &out); err != nil {
		return 0, err
	}
	if len(out) == 0 {
		return 0, nil
	}
	return out[0], nil
}
