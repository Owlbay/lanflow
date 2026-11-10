// Package mcp is `lanflow mcp`: a Model Context Protocol server on stdio
// that lets agents (Claude Desktop, Claude Code, …) use a running Lanflow
// through the local interface (internal/rpc, docs/service-split.md §5).
//
// Tools come from the method manifest, filtered by permission level
// (dangerous and window-only methods are never offered), plus a few
// composite tools (composite.go) that bundle the usual sequences.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/app/remote"
	"lanflow/internal/rpc"
)

// protocolVersions are the MCP revisions spoken, newest first.
var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const instructions = `Lanflow (澜流) is a Mihomo proxy client running on this computer. Standard flow: look at the state (status, diagnose), preview (dry_run / patch_settings with dryRun), apply, then verify (test_rule, test_proxy_delay, diagnose_site). Writes need the user's approval in Lanflow the first time and are audited; "undo" reverts the latest write. Subscription links are never shown: do not try to obtain them.`

// Dialer connects to Lanflow as client ("mcp:<name>").
type Dialer func(ctx context.Context, client string) (*remote.Client, error)

// DefaultDialer connects to the Lanflow of the current user.
func DefaultDialer(version string) Dialer {
	return func(ctx context.Context, client string) (*remote.Client, error) {
		return remote.Dial(ctx, remote.Options{DataDir: app.DefaultOptions("Lanflow").DataDir, Client: client, Version: version})
	}
}

// Server is one MCP session.
type Server struct {
	Version string
	Dial    Dialer

	out  io.Writer
	wmu  sync.Mutex
	mu   sync.Mutex
	name string // clientInfo.name
	conn *remote.Client
}

// Main runs the server on stdin/stdout until stdin closes.
func Main(version string) int {
	s := &Server{Version: version, Dial: DefaultDialer(version)}
	if err := s.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "lanflow mcp: %v\n", err)
		return 1
	}
	return 0
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads requests from in and writes responses to out. Requests run
// concurrently (tool calls can take a while).
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		s.mu.Lock()
		if s.conn != nil {
			_ = s.conn.Close()
		}
		s.mu.Unlock()
	}()
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.send(response{ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if req.Method == "" {
			continue // a response to us: we send no requests
		}
		if req.Method == "initialize" {
			s.reply(req, s.initialize(req.Params)) // before anything else
			continue
		}
		wg.Add(1)
		go func(req request) {
			defer wg.Done()
			s.reply(req, s.handle(ctx, req))
		}(req)
	}
	return sc.Err()
}

func (s *Server) reply(req request, res any) {
	if len(req.ID) == 0 {
		return // a notification
	}
	r := response{ID: req.ID}
	if e, ok := res.(*rpcError); ok {
		r.Error = e
	} else {
		r.Result = res
	}
	s.send(r)
}

func (s *Server) send(r response) {
	r.JSONRPC = "2.0"
	b, err := json.Marshal(r)
	if err != nil {
		b, _ = json.Marshal(response{JSONRPC: "2.0", ID: r.ID, Error: &rpcError{Code: -32603, Message: err.Error()}})
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, _ = s.out.Write(append(b, '\n'))
}

func (s *Server) initialize(params json.RawMessage) any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	_ = json.Unmarshal(params, &p)
	version := protocolVersions[0]
	for _, v := range protocolVersions {
		if v == p.ProtocolVersion {
			version = v
		}
	}
	name := strings.TrimSpace(p.ClientInfo.Name)
	if name == "" {
		name = "unknown"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": "lanflow", "title": "Lanflow 澜流", "version": s.Version},
		"instructions":    instructions,
	}
}

func (s *Server) handle(ctx context.Context, req request) any {
	switch req.Method {
	case "ping":
		return map[string]any{}
	case "notifications/initialized", "notifications/cancelled":
		return nil
	case "tools/list":
		return map[string]any{"tools": Tools()}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return &rpcError{Code: -32602, Message: "invalid params"}
		}
		return s.callTool(ctx, p.Name, p.Arguments)
	}
	return &rpcError{Code: -32601, Message: "method not found: " + req.Method}
}

// client returns the connection to Lanflow, dialing on first use and
// after the app restarted.
func (s *Server) client(ctx context.Context) (*remote.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		select {
		case <-s.conn.Conn().Done():
			s.conn = nil
		default:
			return s.conn, nil
		}
	}
	name := s.name
	if name == "" {
		name = "unknown"
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := s.Dial(dctx, rpc.KindMCP+":"+name)
	if err != nil {
		return nil, err
	}
	c.Timeout = 3 * time.Minute // a write may wait for the user's approval
	s.conn = c
	return c, nil
}

// maxText bounds a tool result.
const maxText = 200 << 10

func toolResult(v any, isErr bool) map[string]any {
	var text string
	if s, ok := v.(string); ok {
		text = s
	} else {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			text, isErr = err.Error(), true
		} else {
			text = string(b)
		}
	}
	if len(text) > maxText {
		text = text[:maxText] + "\n…（结果过长，已截断）"
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}
}

func errorResult(err error) map[string]any {
	msg := err.Error()
	var e *rpc.Error
	if errors.As(err, &e) {
		switch e.Code {
		case rpc.CodeDenied:
			msg = "permission denied: " + e.Message
		case rpc.CodeConsent:
			msg = "the user did not approve: " + e.Message
		}
	}
	return toolResult(msg, true)
}

func (s *Server) callTool(ctx context.Context, name string, args json.RawMessage) any {
	t, ok := toolByName(name)
	if !ok {
		return &rpcError{Code: -32602, Message: "unknown tool: " + name}
	}
	var a map[string]json.RawMessage
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &a); err != nil {
			return toolResult("arguments must be an object", true)
		}
	}
	c, err := s.client(ctx)
	if err != nil {
		return errorResult(err)
	}
	var res any
	if t.composite != nil {
		res, err = t.composite(ctx, c, a)
	} else {
		res, err = callMethod(ctx, c, t.method, a)
	}
	if err != nil {
		return errorResult(err)
	}
	return toolResult(res, false)
}

// callMethod calls a manifest method with named arguments.
func callMethod(ctx context.Context, c *remote.Client, m rpc.Method, a map[string]json.RawMessage) (any, error) {
	params := make([]any, len(m.Params))
	for i, p := range m.Params {
		if v, ok := a[p.Name]; ok {
			params[i] = v
		} else {
			params[i] = nil // the zero value
		}
	}
	for k := range a {
		found := false
		for _, p := range m.Params {
			found = found || p.Name == k
		}
		if !found {
			return nil, fmt.Errorf("unknown argument %q", k)
		}
	}
	var res []json.RawMessage
	if err := c.Conn().Call(ctx, m.Name, params, &res); err != nil {
		return nil, err
	}
	switch len(res) {
	case 0:
		return map[string]any{"ok": true}, nil
	case 1:
		return res[0], nil
	}
	return res, nil
}
