package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

const (
	backoffMin = 500 * time.Millisecond
	backoffMax = 5 * time.Second
	// handshakeTimeout bounds the WebSocket upgrade only; the stream
	// itself lives as long as ctx.
	handshakeTimeout = defaultTimeout
	// readLimit must accommodate large /connections snapshots.
	readLimit = 64 << 20
)

// stream connects to ws://mihomo<path>, decodes every text frame as T and
// hands it to fn. It reconnects with exponential backoff (0.5 s → 5 s)
// while ctx is alive and returns ctx.Err() once ctx is done.
func stream[T any](ctx context.Context, c *client, path string, query url.Values, fn func(T)) error {
	u := "ws://" + hostName + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	backoff := backoffMin
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		gotFrame := c.streamOnce(ctx, u, func(data []byte) {
			var v T
			if json.Unmarshal(data, &v) == nil {
				fn(v)
			}
		})
		if err := ctx.Err(); err != nil {
			return err
		}
		if gotFrame {
			// A healthy session was interrupted (kernel restart, idle
			// close): try again quickly.
			backoff = backoffMin
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		backoff *= 2
		if backoff > backoffMax {
			backoff = backoffMax
		}
	}
}

// streamOnce runs a single WebSocket session and reports whether at least
// one frame was received (used to reset the backoff).
func (c *client) streamOnce(ctx context.Context, u string, onFrame func([]byte)) (gotFrame bool) {
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	conn, resp, err := websocket.Dial(hctx, u, &websocket.DialOptions{
		HTTPClient:      c.http,
		CompressionMode: websocket.CompressionDisabled,
	})
	cancel()
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return false
	}
	defer conn.CloseNow()
	conn.SetReadLimit(readLimit)

	// One buffer serves every frame of the session: a /connections frame
	// (hundreds of KB every second) no longer costs a fresh allocation.
	// onFrame must not keep the slice (stream decodes it at once).
	var buf bytes.Buffer
	for {
		_, r, err := conn.Reader(ctx)
		if err != nil {
			return gotFrame
		}
		buf.Reset()
		if _, err := buf.ReadFrom(r); err != nil {
			return gotFrame
		}
		gotFrame = true
		onFrame(buf.Bytes())
		if buf.Cap() > maxKeptFrameBuf {
			buf = bytes.Buffer{} // do not pin one exceptional frame's memory
		}
	}
}

// maxKeptFrameBuf is the largest frame buffer kept between frames.
const maxKeptFrameBuf = 4 << 20

func (c *client) streamTraffic(ctx context.Context, fn func(Traffic)) error {
	return stream(ctx, c, "/traffic", nil, fn)
}

func (c *client) streamMemory(ctx context.Context, fn func(Memory)) error {
	return stream(ctx, c, "/memory", nil, fn)
}

func (c *client) streamLogs(ctx context.Context, level string, fn func(Log)) error {
	if level == "" {
		level = "info"
	}
	return stream(ctx, c, "/logs", url.Values{"level": {level}}, fn)
}

func (c *client) streamConnections(ctx context.Context, interval time.Duration, fn func(Connections)) error {
	if interval <= 0 {
		interval = time.Second
	}
	ms := interval.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return stream(ctx, c, "/connections", url.Values{"interval": {strconv.FormatInt(ms, 10)}}, fn)
}
