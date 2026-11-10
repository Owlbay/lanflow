package agent

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

	"lanflow/internal/model"
)

// connRec creates the request records of one forwarded connection.
type connRec struct {
	st     *store
	base   model.CaptureFlow
	tls    model.CaptureTLS
	timing model.CaptureTiming
	start  time.Time
}

func (c *connRec) newFlow(method, scheme, authority, path string, hdr []model.KV) *record {
	f := c.base
	f.Flags = append([]string(nil), c.base.Flags...)
	f.Start = time.Now()
	f.Method, f.Scheme, f.Path = method, scheme, path
	if authority != "" {
		h, p := splitHostPort(authority)
		if h != "" {
			f.Host = h
		}
		if p > 0 {
			f.Port = p
		}
	}
	r := c.st.add(f)
	c.st.update(r, func(d *model.CaptureFlowDetail) {
		d.ReqHeader = hdr
		d.TLS = c.tls
		d.Timing = c.timing
	})
	return r
}

func (c *connRec) respHeaders(r *record, status int, hdr []model.KV) {
	c.st.update(r, func(d *model.CaptureFlowDetail) {
		d.Flow.Status = status
		d.RespHeader = hdr
		d.Flow.ContentType = headerValue(hdr, "content-type")
		d.Timing.TTFBMs = time.Since(d.Flow.Start).Milliseconds()
	})
}

func (c *connRec) finish(r *record, errText string) {
	c.st.update(r, func(d *model.CaptureFlowDetail) {
		if d.Flow.Done {
			return
		}
		d.Flow.Done = true
		d.Flow.DurationMs = time.Since(d.Flow.Start).Milliseconds()
		d.Timing.EndMs = d.Flow.DurationMs
		if errText != "" && d.Flow.Error == "" {
			d.Flow.Error = errText
		}
	})
}

func (c *connRec) addFlag(r *record, flag string) {
	c.st.update(r, func(d *model.CaptureFlowDetail) {
		if !d.Flow.Has(flag) {
			d.Flow.Flags = append(d.Flow.Flags, flag)
		}
	})
}

func headerValue(h []model.KV, name string) string {
	for _, kv := range h {
		if strings.EqualFold(kv.Key, name) {
			return kv.Value
		}
	}
	return ""
}

func splitHostPort(a string) (string, int) {
	if i := strings.LastIndexByte(a, ':'); i > 0 && !strings.Contains(a[i:], "]") {
		if p, err := strconv.Atoi(a[i+1:]); err == nil {
			return strings.Trim(a[:i], "[]"), p
		}
	}
	return strings.Trim(a, "[]"), 0
}

// ---- HTTP/1.x ----

const maxHeadBytes = 256 << 10

var errHead = errors.New("malformed HTTP/1 message")

// readHead reads a start line and header lines, keeping the names as sent
// and their order.
func readHead(br *bufio.Reader) (start string, hdr []model.KV, err error) {
	total := 0
	for {
		line, err := br.ReadSlice('\n')
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if len(line) == 0 && start == "" {
				return "", nil, err
			}
			return "", nil, errHead
		}
		total += len(line)
		if total > maxHeadBytes || errors.Is(err, bufio.ErrBufferFull) {
			return "", nil, errHead
		}
		l := strings.TrimRight(string(line), "\r\n")
		if start == "" {
			if l == "" {
				continue // stray CRLF between messages
			}
			start = l
			continue
		}
		if l == "" {
			return start, hdr, nil
		}
		if (l[0] == ' ' || l[0] == '\t') && len(hdr) > 0 {
			hdr[len(hdr)-1].Value += " " + strings.TrimSpace(l)
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			return "", nil, errHead
		}
		hdr = append(hdr, model.KV{Key: strings.TrimSpace(k), Value: strings.TrimSpace(v)})
	}
}

type h1req struct {
	rec     *record
	method  string
	upgrade bool
	// resolved once the response is known: true when the server switched
	// protocols (WebSocket) and HTTP parsing stops.
	switched chan bool
}

// h1Parser parses both directions of an HTTP/1.x connection; responses are
// paired with requests in order (pipelining included).
type h1Parser struct {
	c      *connRec
	scheme string
	host   string
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []*h1req
	reqEOF bool
	ws     *wsState
}

func newH1Parser(c *connRec, scheme, host string) *h1Parser {
	p := &h1Parser{c: c, scheme: scheme, host: host}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *h1Parser) push(r *h1req) {
	p.mu.Lock()
	p.queue = append(p.queue, r)
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *h1Parser) pop() *h1req {
	p.mu.Lock()
	defer p.mu.Unlock()
	deadline := time.Now().Add(30 * time.Second)
	for len(p.queue) == 0 && !p.reqEOF {
		if time.Now().After(deadline) {
			return nil
		}
		waitTimeout(p.cond, time.Second)
	}
	if len(p.queue) == 0 {
		return nil
	}
	r := p.queue[0]
	p.queue = p.queue[1:]
	return r
}

func waitTimeout(c *sync.Cond, d time.Duration) {
	t := time.AfterFunc(d, c.Broadcast)
	c.Wait()
	t.Stop()
}

// requests parses the client's side.
func (p *h1Parser) requests(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	defer func() {
		p.mu.Lock()
		p.reqEOF = true
		p.cond.Broadcast()
		p.mu.Unlock()
		io.Copy(io.Discard, br)
	}()
	for {
		start, hdr, err := readHead(br)
		if err != nil {
			return
		}
		parts := strings.SplitN(start, " ", 3)
		if len(parts) < 3 || !strings.HasPrefix(parts[2], "HTTP/1.") {
			return
		}
		method, target := parts[0], parts[1]
		path, authority := target, headerValue(hdr, "host")
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			rest := target[strings.Index(target, "//")+2:]
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				authority, path = rest[:i], rest[i:]
			} else {
				authority, path = rest, "/"
			}
		}
		if authority == "" {
			authority = p.host
		}
		rec := p.c.newFlow(method, p.scheme, authority, path, hdr)
		req := &h1req{rec: rec, method: method, switched: make(chan bool, 1)}
		req.upgrade = headerValue(hdr, "upgrade") != ""
		p.push(req)
		if err := p.body(br, rec, false, hdr, false); err != nil {
			p.c.finish(rec, "请求体解析失败")
			return
		}
		if req.upgrade {
			select {
			case sw := <-req.switched:
				if sw {
					if p.ws != nil {
						p.ws.frames(br, false)
					}
					return
				}
			case <-time.After(60 * time.Second):
				return
			}
		}
	}
}

// responses parses the server's side.
func (p *h1Parser) responses(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	defer io.Copy(io.Discard, br)
	for {
		start, hdr, err := readHead(br)
		if err != nil {
			p.drain("")
			return
		}
		req := p.pop()
		if req == nil {
			return
		}
		parts := strings.SplitN(start, " ", 3)
		if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
			p.c.finish(req.rec, "响应解析失败")
			return
		}
		status, _ := strconv.Atoi(parts[1])
		// interim responses (100 Continue, 103 Early Hints)
		for status >= 100 && status < 200 && status != 101 {
			start, hdr, err = readHead(br)
			if err != nil {
				p.c.finish(req.rec, "响应解析失败")
				return
			}
			parts = strings.SplitN(start, " ", 3)
			if len(parts) < 2 {
				p.c.finish(req.rec, "响应解析失败")
				return
			}
			status, _ = strconv.Atoi(parts[1])
		}
		p.c.respHeaders(req.rec, status, hdr)
		if status == 101 {
			p.c.addFlag(req.rec, model.FlowWebSocket)
			p.ws = &wsState{c: p.c, rec: req.rec}
			req.switched <- true
			p.ws.frames(br, true)
			p.c.finish(req.rec, "")
			return
		}
		if req.upgrade {
			req.switched <- false
		}
		noBody := req.method == "HEAD" || status == 204 || status == 304
		if err := p.body(br, req.rec, true, hdr, noBody); err != nil {
			p.c.finish(req.rec, "响应体解析失败")
			return
		}
		p.c.finish(req.rec, "")
		if strings.EqualFold(headerValue(hdr, "connection"), "close") {
			return
		}
	}
}

// drain finishes requests still waiting for a response.
func (p *h1Parser) drain(errText string) {
	p.mu.Lock()
	q := p.queue
	p.queue = nil
	p.mu.Unlock()
	for _, r := range q {
		if errText == "" {
			errText = "连接在响应前关闭"
		}
		p.c.finish(r.rec, errText)
	}
}

// body reads one message body; responses without length run to EOF.
func (p *h1Parser) body(br *bufio.Reader, rec *record, resp bool, hdr []model.KV, none bool) error {
	if none {
		return nil
	}
	var r io.Reader
	switch {
	case strings.Contains(strings.ToLower(headerValue(hdr, "transfer-encoding")), "chunked"):
		r = httputil.NewChunkedReader(br)
	case headerValue(hdr, "content-length") != "":
		n, err := strconv.ParseInt(headerValue(hdr, "content-length"), 10, 64)
		if err != nil || n < 0 {
			return errHead
		}
		r = io.LimitReader(br, n)
	case resp:
		r = br
	default:
		return nil
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			p.c.st.appendBody(rec, resp, buf[:n])
		}
		if err == io.EOF {
			if cr, ok := r.(interface{ Close() error }); ok {
				cr.Close()
			}
			if strings.Contains(strings.ToLower(headerValue(hdr, "transfer-encoding")), "chunked") {
				skipTrailers(br)
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// skipTrailers consumes the trailer section after the last chunk.
func skipTrailers(br *bufio.Reader) {
	for {
		line, err := br.ReadSlice('\n')
		if err != nil || len(bytes.TrimRight(line, "\r\n")) == 0 {
			return
		}
	}
}
