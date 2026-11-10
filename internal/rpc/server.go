package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"reflect"
	"sync"

	"lanflow/internal/app"
	"lanflow/internal/model"
)

// Options configure a Server.
type Options struct {
	API     app.API
	Version string // Lanflow's version, sent in the handshake
	// AuditDir is where audit.log goes (Lanflow's log directory); "" keeps
	// the audit in memory only.
	AuditDir string
	// Prompt asks the user to allow an agent's write or sensitive read
	// (the window's dialog). Nil: such calls are refused with a hint.
	Prompt PromptFunc
	// Host is the serving process's side of the window ops (windowops.go):
	// the service process of the split app, or the single process. Nil:
	// the window ops are refused.
	Host Host
}

// Server serves app.API on the local interface.
type Server struct {
	o     Options
	api   reflect.Value
	guard *guard
	audit *auditLog
	undo  *undoStack

	mu    sync.Mutex
	ls    []net.Listener
	conns map[*session]struct{}
	done  bool
	ui    *session // the window process that attached (AttachUI), nil when none
}

// NewServer returns a server for o.API; call Serve with a listener.
func NewServer(o Options) *Server {
	s := &Server{o: o, api: reflect.ValueOf(o.API), conns: map[*session]struct{}{}}
	s.audit = openAudit(o.AuditDir)
	s.undo = &undoStack{}
	s.guard = &guard{api: o.API, prompt: o.Prompt}
	return s
}

// Start listens on the endpoint of dataDir and serves in the background.
func Start(dataDir string, o Options) (*Server, error) {
	l, err := Listen(dataDir)
	if err != nil {
		return nil, err
	}
	s := NewServer(o)
	go func() { _ = s.Serve(l) }()
	return s, nil
}

// Serve accepts connections on l until Close.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		_ = l.Close()
		return net.ErrClosed
	}
	s.ls = append(s.ls, l)
	s.mu.Unlock()
	for {
		c, err := l.Accept()
		if err != nil {
			s.mu.Lock()
			done := s.done
			s.mu.Unlock()
			if done || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.serveConn(c)
	}
}

// Close stops listening and ends every connection.
func (s *Server) Close() error {
	s.mu.Lock()
	s.done = true
	ls := s.ls
	s.ls = nil
	conns := make([]*session, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, l := range ls {
		_ = l.Close()
	}
	for _, c := range conns {
		_ = c.c.Close()
	}
	s.audit.close()
	return nil
}

// session is one client connection.
type session struct {
	c      net.Conn
	wmu    sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	hello  bool
	id     Identity
	reveal bool
	topics map[string]bool // nil = not subscribed, empty = all
	off    func()

	// consent remembered for this connection (guard.go)
	askMu     sync.Mutex
	writeOK   bool
	sensitive int // 0 = not asked, 1 = allowed, -1 = refused
}

func (s *Server) serveConn(c net.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	ss := &session{c: c, ctx: ctx, cancel: cancel}
	s.mu.Lock()
	s.conns[ss] = struct{}{}
	s.mu.Unlock()
	defer func() {
		cancel()
		ss.mu.Lock()
		off := ss.off
		ss.off = nil
		ss.mu.Unlock()
		if off != nil {
			off()
		}
		s.mu.Lock()
		delete(s.conns, ss)
		s.mu.Unlock()
		s.detachUI(ss)
		_ = c.Close()
	}()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			ss.send(message{ID: json.RawMessage("null"), Error: &Error{Code: CodeParse, Message: "无法解析请求"}})
			continue
		}
		if m.Method == "" {
			continue // a response or garbage: clients send none
		}
		ss.mu.Lock()
		hello := ss.hello
		ss.mu.Unlock()
		if !hello {
			// The handshake is answered before anything else is read.
			res, err := s.hello(ss, m.Params)
			s.reply(ss, m, res, err)
			if err != nil {
				return
			}
			continue
		}
		go func(m message) {
			res, err := s.dispatch(ss, m.Method, m.Params)
			s.reply(ss, m, res, err)
		}(m)
	}
}

func (s *Server) reply(ss *session, m message, res any, err error) {
	if len(m.ID) == 0 {
		return // a notification
	}
	out := message{ID: m.ID}
	if err != nil {
		out.Error = toError(err)
	} else {
		b, merr := json.Marshal(res)
		if merr != nil {
			out.Error = &Error{Code: CodeApp, Message: "无法编码结果: " + merr.Error()}
		} else {
			out.Result = b
		}
	}
	ss.send(out)
}

func (ss *session) send(m message) {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	ss.wmu.Lock()
	defer ss.wmu.Unlock()
	_, _ = ss.c.Write(append(b, '\n'))
}

// toError turns a method's error into the wire form.
func toError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	out := &Error{Code: CodeApp, Message: err.Error()}
	if errors.Is(err, app.ErrHelperRequired) {
		out.Data = &ErrorData{Kind: "helper-required"}
	}
	return out
}

func (s *Server) hello(ss *session, params json.RawMessage) (any, error) {
	var h Hello
	if err := json.Unmarshal(params, &h); err != nil {
		return nil, &Error{Code: CodeHandshake, Message: "第一条消息必须是 hello 握手"}
	}
	if h.Protocol != ProtocolVersion {
		return nil, &Error{Code: CodeHandshake, Message: fmt.Sprintf("本机接口协议版本不一致（澜流 %s 为 %d，客户端为 %d）：请重启澜流，或使用同一版本的 lanflow 命令", s.o.Version, ProtocolVersion, h.Protocol)}
	}
	id, err := ParseIdentity(h.Client)
	if err != nil {
		return nil, &Error{Code: CodeHandshake, Message: err.Error()}
	}
	ss.mu.Lock()
	ss.hello, ss.id, ss.reveal = true, id, h.Reveal && id.Kind == KindCtl
	ss.mu.Unlock()
	if h.Version != "" && s.o.Version != "" && h.Version != s.o.Version {
		log.Printf("lanflow: local api: %s is version %s, Lanflow is %s", id, h.Version, s.o.Version)
	}
	return Welcome{Protocol: ProtocolVersion, Version: s.o.Version, Client: id.String(), PID: os.Getpid()}, nil
}

// dispatch runs one request after the handshake.
func (s *Server) dispatch(ss *session, method string, params json.RawMessage) (any, error) {
	switch method {
	case "hello":
		return nil, &Error{Code: CodeInvalidRequest, Message: "握手已完成"}
	case "subscribe":
		return nil, s.subscribe(ss, params)
	}
	if op, ok := agentOps[method]; ok {
		return s.callAgentOp(ss, method, op, params)
	}
	m, ok := apiMethod(method)
	if !ok || method == "OnChange" || method == "UpdateUIState" {
		return nil, &Error{Code: CodeNoMethod, Message: "没有这个方法: " + method}
	}
	return s.callAPI(ss, m, params)
}

// decodeArgs decodes a JSON array into the parameters of fn.
func decodeArgs(fn reflect.Type, params json.RawMessage) ([]reflect.Value, error) {
	var raw []json.RawMessage
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: "参数应为数组"}
		}
	}
	if len(raw) != fn.NumIn() {
		return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("需要 %d 个参数，收到 %d 个", fn.NumIn(), len(raw))}
	}
	args := make([]reflect.Value, len(raw))
	for i, r := range raw {
		v := reflect.New(fn.In(i))
		if err := json.Unmarshal(r, v.Interface()); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("参数 %d 无效: %v", i+1, err)}
		}
		args[i] = v.Elem()
	}
	return args, nil
}

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// invoke calls the API method name with args and splits the results.
func (s *Server) invoke(name string, args []reflect.Value) ([]any, error) {
	fn := s.api.MethodByName(name)
	if !fn.IsValid() {
		return nil, &Error{Code: CodeNoMethod, Message: "没有这个方法: " + name}
	}
	out := fn.Call(args)
	var err error
	if n := len(out); n > 0 && out[n-1].Type() == errorType {
		if e := out[n-1].Interface(); e != nil {
			err = e.(error)
		}
		out = out[:n-1]
	}
	res := make([]any, len(out))
	for i, v := range out {
		res[i] = v.Interface()
	}
	return res, err
}

// agentRefused lists the API methods agents reach another way: the full
// settings structs they would write back hold redacted values.
var agentRefused = map[string]string{
	"SaveSettings": "agent 不能整体保存设置（读到的设置已脱敏）：请用 PatchSettings（lanflow ctl config set）只改需要的字段",
	"ApplyDNS":     "agent 不能整体替换 DNS 设置：请用 PatchSettings 修改 DNS 下的字段",
	"ApplySniffer": "agent 不能整体替换嗅探设置：请用 PatchSettings 修改 Sniffer 下的字段",
}

func (s *Server) callAPI(ss *session, m Method, params json.RawMessage) (any, error) {
	fn := s.api.MethodByName(m.Name)
	if !fn.IsValid() {
		return nil, &Error{Code: CodeNoMethod, Message: "没有这个方法: " + m.Name}
	}
	args, err := decodeArgs(fn.Type(), params)
	if err != nil {
		return nil, err
	}
	id := ss.identity()
	if !id.Agent() {
		return s.invoke(m.Name, args)
	}
	if why, ok := agentRefused[m.Name]; ok {
		err := &Error{Code: CodeDenied, Message: why}
		s.audit.add(id, m.Name, args, err)
		return nil, err
	}
	if err := s.guard.check(ss, m.Name, m.Level, summarize(m.Name, args)); err != nil {
		if m.Level != LevelRead {
			s.audit.add(id, m.Name, args, err)
		}
		return nil, err
	}
	if m.Name == "EditSubscription" {
		args[0] = reflect.ValueOf(keepSubscriptionURL(s.o.API, args[0].Interface().(model.Subscription)))
	}
	if m.Level == LevelRead || m.Level == LevelSensitive {
		res, err := s.invoke(m.Name, args)
		if err != nil {
			return nil, err
		}
		return redactResults(m.Name, res), nil
	}
	prep := s.prepareUndo(m.Name, args)
	res, err := s.invoke(m.Name, args)
	s.audit.add(id, m.Name, args, err)
	if err != nil {
		return nil, err
	}
	if prep != nil {
		if restore, desc := prep(res); restore != nil {
			s.undo.push(undoEntry{client: id.String(), method: m.Name, desc: desc, restore: restore})
		}
	}
	return redactResults(m.Name, res), nil
}

func (ss *session) identity() Identity {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.id
}

// subscribe registers the connection for "changed" notifications.
func (s *Server) subscribe(ss *session, params json.RawMessage) error {
	var p []json.RawMessage
	var topics []string
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil || len(p) > 1 {
			return &Error{Code: CodeInvalidParams, Message: "参数应为 [主题列表]"}
		}
		if len(p) == 1 {
			if err := json.Unmarshal(p[0], &topics); err != nil {
				return &Error{Code: CodeInvalidParams, Message: "主题列表应为字符串数组"}
			}
		}
	}
	set := map[string]bool{}
	for _, t := range topics {
		set[t] = true
	}
	ss.mu.Lock()
	first := ss.topics == nil
	ss.topics = set
	ss.mu.Unlock()
	if !first {
		return nil
	}
	off := s.o.API.OnChange(func(t app.Topic) {
		ss.mu.Lock()
		want := ss.topics != nil && (len(ss.topics) == 0 || ss.topics[string(t)])
		ss.mu.Unlock()
		if !want {
			return
		}
		b, _ := json.Marshal(Changed{Topic: string(t)})
		// Off the notifying goroutine: a slow client must not stall the
		// service's other listeners.
		go ss.send(message{Method: "changed", Params: b})
	})
	ss.mu.Lock()
	if ss.ctx.Err() != nil {
		ss.mu.Unlock()
		off()
		return nil
	}
	ss.off = off
	ss.mu.Unlock()
	return nil
}

func pid() int { return os.Getpid() }
