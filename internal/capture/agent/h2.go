package agent

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"sync"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"lanflow/internal/model"
)

// h2Parser reads both directions of an HTTP/2 connection from copies of
// the bytes: one frame reader and HPACK decoder per direction, so the
// dynamic tables follow exactly what the peers sent (docs/capture.md
// §5.3). A parse error stops the record, never the forwarding.
type h2Parser struct {
	c       *connRec
	mu      sync.Mutex
	streams map[uint32]*h2stream
}

type h2stream struct {
	rec      *record
	gotResp  bool
	reqDone  bool
	respDone bool
}

func newH2Parser(c *connRec) *h2Parser {
	return &h2Parser{c: c, streams: map[uint32]*h2stream{}}
}

const h2MaxTable = 1 << 20

func (p *h2Parser) run(r io.Reader, client bool) {
	br := bufio.NewReaderSize(r, 64<<10)
	defer io.Copy(io.Discard, br)
	if client {
		pre := make([]byte, len(http2.ClientPreface))
		if _, err := io.ReadFull(br, pre); err != nil || !bytes.Equal(pre, []byte(http2.ClientPreface)) {
			return
		}
	}
	fr := http2.NewFramer(nil, br)
	fr.SetMaxReadFrameSize(1<<24 - 1)
	dec := hpack.NewDecoder(h2MaxTable, nil)
	dec.SetAllowedMaxDynamicTableSize(h2MaxTable)
	fr.ReadMetaHeaders = dec
	fr.MaxHeaderListSize = 1 << 20
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				if !client { // the server side ending closes what is open
					p.abort("")
				}
				return
			}
			p.abort(fmt.Sprintf("HTTP/2 解析中止：%v", err))
			return
		}
		switch f := f.(type) {
		case *http2.MetaHeadersFrame:
			p.headers(f, client)
		case *http2.DataFrame:
			p.data(f, client)
		case *http2.RSTStreamFrame:
			p.reset(f.StreamID, f.ErrCode)
		case *http2.PushPromiseFrame:
			// keep the decoder's table in step; pushed streams are not
			// recorded (browsers have turned push off)
			if f.HeadersEnded() {
				dec.DecodeFull(f.HeaderBlockFragment())
			}
		}
	}
}

func (p *h2Parser) stream(id uint32) *h2stream {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.streams[id]
}

func (p *h2Parser) headers(f *http2.MetaHeadersFrame, client bool) {
	id := f.StreamID
	var hdr []model.KV
	pseudo := map[string]string{}
	for _, h := range f.Fields {
		if h.IsPseudo() {
			pseudo[h.Name] = h.Value
			continue
		}
		hdr = append(hdr, model.KV{Key: h.Name, Value: h.Value})
	}
	if client {
		s := p.stream(id)
		if s == nil {
			rec := p.c.newFlow(pseudo[":method"], pseudo[":scheme"], pseudo[":authority"], pseudo[":path"], hdr)
			s = &h2stream{rec: rec}
			p.mu.Lock()
			p.streams[id] = s
			p.mu.Unlock()
		} else {
			// request trailers
			p.c.st.update(s.rec, func(d *model.CaptureFlowDetail) { d.ReqHeader = append(d.ReqHeader, hdr...) })
		}
		if f.StreamEnded() {
			p.endReq(id)
		}
		return
	}
	s := p.stream(id)
	if s == nil {
		return
	}
	status, _ := strconv.Atoi(pseudo[":status"])
	switch {
	case status >= 100 && status < 200:
		// interim
	case !s.gotResp && status > 0:
		s.gotResp = true
		p.c.respHeaders(s.rec, status, hdr)
	default:
		// response trailers
		p.c.st.update(s.rec, func(d *model.CaptureFlowDetail) { d.RespHeader = append(d.RespHeader, hdr...) })
	}
	if f.StreamEnded() {
		p.endResp(id)
	}
}

func (p *h2Parser) data(f *http2.DataFrame, client bool) {
	s := p.stream(f.StreamID)
	if s == nil {
		return
	}
	if d := f.Data(); len(d) > 0 {
		p.c.st.appendBody(s.rec, !client, d)
	}
	if f.StreamEnded() {
		if client {
			p.endReq(f.StreamID)
		} else {
			p.endResp(f.StreamID)
		}
	}
}

func (p *h2Parser) endReq(id uint32) {
	p.mu.Lock()
	s := p.streams[id]
	if s != nil {
		s.reqDone = true
	}
	p.mu.Unlock()
}

func (p *h2Parser) endResp(id uint32) {
	p.mu.Lock()
	s := p.streams[id]
	if s != nil {
		s.respDone = true
		delete(p.streams, id)
	}
	p.mu.Unlock()
	if s != nil {
		p.c.finish(s.rec, "")
	}
}

func (p *h2Parser) reset(id uint32, code http2.ErrCode) {
	p.mu.Lock()
	s := p.streams[id]
	delete(p.streams, id)
	p.mu.Unlock()
	if s != nil {
		p.c.finish(s.rec, "RST_STREAM "+code.String())
	}
}

// abort finishes every open stream (parse failure or end of connection).
func (p *h2Parser) abort(msg string) {
	p.mu.Lock()
	ss := p.streams
	p.streams = map[uint32]*h2stream{}
	p.mu.Unlock()
	for _, s := range ss {
		if msg == "" || msg == "HTTP/2 解析中止：EOF" {
			p.c.finish(s.rec, "连接已关闭")
		} else {
			p.c.finish(s.rec, msg)
		}
	}
}
