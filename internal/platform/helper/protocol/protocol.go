// Package protocol defines the wire format spoken between the Lanflow app
// and lanflow-helper: newline-delimited JSON, one request and one response
// per line, over a local endpoint (unix socket on macOS, named pipe on
// Windows). Both sides of the connection are implemented here so the client
// (internal/platform/helper) and the service (cmd/lanflow-helper) share one
// definition.
package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Version is the protocol version. A server answers requests whose "v" is
// equal to its own; a mismatch yields an error response so the app can ask
// the user to reinstall the helper.
const Version = 1

// Endpoint names shared by client and service.
const (
	// SocketPath is the unix socket the helper listens on (macOS).
	SocketPath = "/var/run/lanflow-helper.sock"
	// PipeName is the named pipe the helper listens on (Windows).
	PipeName = `\\.\pipe\lanflow-helper`
)

// Operations.
const (
	OpPing   = "ping"
	OpStart  = "start"
	OpStop   = "stop"
	OpStatus = "status"
)

// maxLine bounds a single request/response line (the spec carries a few
// paths and environment variables; 1 MiB is far above any legitimate size).
const maxLine = 1 << 20

// Spec is the wire form of core.Spec.
type Spec struct {
	Bin        string   `json:"bin"`
	WorkDir    string   `json:"workDir"`
	Config     string   `json:"config"`
	Controller string   `json:"controller"`
	LogFile    string   `json:"logFile"`
	Env        []string `json:"env,omitempty"`
}

// Request is one line sent by the client.
type Request struct {
	V    int    `json:"v"`
	Op   string `json:"op"`
	Spec *Spec  `json:"spec,omitempty"`
}

// Response is one line sent back by the service.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Ping fields.
	Version  string `json:"version,omitempty"`
	Protocol int    `json:"protocol,omitempty"`
	CorePath string `json:"corePath,omitempty"`
	// Status fields.
	Running   bool   `json:"running"`
	ExitError string `json:"exitError,omitempty"`
	PID       int    `json:"pid,omitempty"`
}

// Err returns the response's error, or nil when OK.
func (r Response) Err() error {
	if r.OK {
		return nil
	}
	if r.Error == "" {
		return errors.New("权限组件返回了未知错误")
	}
	return errors.New(r.Error)
}

// Handler answers one request. It must not block for long: the service
// serializes connections per request, not per client.
type Handler func(ctx context.Context, req Request) Response

// Server serves the protocol on a listener.
type Server struct {
	Handler Handler
	// Authorize is called for every accepted connection before any request
	// is read; a non-nil error closes the connection. Nil accepts all.
	Authorize func(conn net.Conn) error
	// Logf receives diagnostic messages. Nil discards.
	Logf func(format string, args ...any)
	// Timeout bounds one request/response exchange (default 30 s).
	Timeout time.Duration
}

// Serve accepts connections until ctx is done or the listener fails. It
// closes the listener when ctx is done.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go s.ServeConn(ctx, conn)
	}
}

// ServeConn handles one connection: authorize, then answer requests line
// by line until the client closes it.
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	if s.Authorize != nil {
		if err := s.Authorize(conn); err != nil {
			s.logf("rejected connection: %v", err)
			return
		}
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	for {
		_ = conn.SetDeadline(time.Now().Add(timeout))
		line, err := readLine(r)
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				s.logf("read: %v", err)
			}
			return
		}
		var req Request
		var resp Response
		if err := json.Unmarshal(line, &req); err != nil {
			resp = Response{Error: "请求格式错误: " + err.Error()}
		} else if req.V != Version {
			resp = Response{Error: fmt.Sprintf("权限组件协议版本不匹配（组件 %d，应用 %d），请重新安装权限组件", Version, req.V), Protocol: Version}
		} else {
			resp = s.Handler(ctx, req)
		}
		if err := WriteMessage(conn, resp); err != nil {
			s.logf("write: %v", err)
			return
		}
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Dialer opens a connection to the helper endpoint.
type Dialer func(ctx context.Context) (net.Conn, error)

// Client issues requests; every Call uses a fresh connection, which keeps
// the client stateless and makes a dead helper visible immediately.
type Client struct {
	Dial Dialer
}

// Call sends req and returns the response. Transport failures and protocol
// errors are returned as errors; application errors (resp.OK == false)
// are returned via resp.Err() and the response is still returned.
func (c *Client) Call(ctx context.Context, req Request) (Response, error) {
	req.V = Version
	conn, err := c.Dial(ctx)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	if err := WriteMessage(conn, req); err != nil {
		return Response{}, err
	}
	line, err := readLine(bufio.NewReaderSize(conn, 64<<10))
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, fmt.Errorf("权限组件响应格式错误: %w", err)
	}
	return resp, resp.Err()
}

// WriteMessage writes v as one JSON line.
func WriteMessage(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		part, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, part...)
		if len(buf) > maxLine {
			return nil, errors.New("请求过长")
		}
		if !isPrefix {
			return buf, nil
		}
	}
}
