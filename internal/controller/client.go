package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultTimeout bounds every REST call unless the caller's context is
// shorter.
const defaultTimeout = 10 * time.Second

// delayExtra is added to the user-chosen delay-test timeout so that the
// kernel (which applies the same timeout server-side) answers first.
const delayExtra = 2 * time.Second

// hostName is the fake HTTP host; the transport ignores it and always
// connects through DialFunc.
const hostName = "mihomo"

// client is the implementation behind Client.
type client struct {
	dial DialFunc
	http *http.Client
}

func newClient(d DialFunc) *client {
	tr := &http.Transport{
		// Never consult the system proxy: the kernel is reached over a
		// local socket only.
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d(ctx)
		},
		// TLS is never used on the controller socket; make sure a stray
		// https URL does not try to negotiate it.
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return nil, errors.New("controller: TLS is not supported over the control socket")
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 0, // governed by the per-call context
		DisableCompression:    true,
	}
	return &client{
		dial: d,
		http: &http.Client{
			Transport: tr,
			// Mihomo never redirects; refuse to follow anything anyway.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Error is a failed controller call. Message is the server's message (or
// a short description when the server sent none); Status is the HTTP
// status, 0 for transport failures.
type Error struct {
	// Op is the Chinese description of what failed, e.g. "切换节点失败".
	Op      string
	Status  int
	Message string
	cause   error
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Op
	}
	return e.Op + "：" + e.Message
}

// Unwrap exposes the transport-level cause (context errors in particular)
// so callers can test errors.Is(err, context.DeadlineExceeded).
func (e *Error) Unwrap() error { return e.cause }

// mihomoError is the body Mihomo sends with every non-2xx status.
type mihomoError struct {
	Message string `json:"message"`
}

// withTimeout bounds ctx by d unless ctx already ends sooner.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= d {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// request describes one REST call.
type request struct {
	method string
	path   string     // already escaped, starts with "/"
	query  url.Values // optional
	body   any        // JSON-encoded when non-nil
	out    any        // JSON-decoded from a 2xx body when non-nil
	op     string     // Chinese failure description
	// timeout overrides defaultTimeout when > 0.
	timeout time.Duration
}

// do performs one REST call over the control socket.
func (c *client) do(ctx context.Context, r request) error {
	to := r.timeout
	if to <= 0 {
		to = defaultTimeout
	}
	ctx, cancel := withTimeout(ctx, to)
	defer cancel()

	// r.path already contains escaped segments; url.Parse (inside
	// NewRequest) keeps them verbatim in RawPath, so e.g. "%2F" inside a
	// proxy name survives untouched.
	rawURL := "http://" + hostName + r.path
	if len(r.query) > 0 {
		rawURL += "?" + r.query.Encode()
	}

	var body io.Reader
	if r.body != nil {
		buf, err := json.Marshal(r.body)
		if err != nil {
			return &Error{Op: r.op, Message: "无法编码请求：" + err.Error(), cause: err}
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, rawURL, body)
	if err != nil {
		return &Error{Op: r.op, Message: err.Error(), cause: err}
	}
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Op: r.op, Message: transportMessage(ctx, err), cause: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &Error{Op: r.op, Status: resp.StatusCode, Message: decodeServerError(resp)}
	}
	if r.out == nil || resp.StatusCode == http.StatusNoContent {
		// Drain so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(r.out); err != nil {
		return &Error{Op: r.op, Status: resp.StatusCode, Message: "内核返回了无法解析的数据：" + err.Error(), cause: err}
	}
	// Drain what follows the value (a trailing newline) so the keep-alive
	// connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// decodeServerError turns a Mihomo error response into a human message.
func decodeServerError(resp *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var m mihomoError
	if err := json.Unmarshal(raw, &m); err == nil && m.Message != "" {
		return m.Message
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return fmt.Sprintf("HTTP %d：%s", resp.StatusCode, s)
	}
	return fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
}

// transportMessage describes a failure that happened before any HTTP
// response arrived.
func transportMessage(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "内核长时间未响应，请确认内核正在运行"
	case errors.Is(err, context.Canceled):
		return "操作已取消"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return "无法连接内核：" + err.Error()
}

// escape prepares a proxy / provider name for use as a path segment.
// Mihomo unescapes the segment on its side (getEscapeParam), so Chinese
// group names and names containing '/' or '%' round-trip intact.
func escape(name string) string {
	return url.PathEscape(name)
}
