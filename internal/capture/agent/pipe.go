package agent

import (
	"errors"
	"io"
	"sync"
)

// lossyPipe carries a copy of a forwarded byte stream to a parser. Writes
// never block the forwarding: when the parser falls behind by more than
// max bytes the pipe breaks and the parser stops (only the record suffers,
// docs/capture.md §5.2).
type lossyPipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	max    int
	closed bool
	broken bool
}

var errPipeBroken = errors.New("capture parser fell behind")

func newLossyPipe(max int) *lossyPipe {
	p := &lossyPipe{max: max}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *lossyPipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.broken || p.closed {
		return len(b), nil
	}
	if len(p.buf)+len(b) > p.max {
		p.broken = true
		p.buf = nil
		p.cond.Broadcast()
		return len(b), nil
	}
	p.buf = append(p.buf, b...)
	p.cond.Broadcast()
	return len(b), nil
}

func (p *lossyPipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 && !p.closed && !p.broken {
		p.cond.Wait()
	}
	if p.broken {
		return 0, errPipeBroken
	}
	if len(p.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	if len(p.buf) == 0 {
		p.buf = nil
	}
	return n, nil
}

// Close ends the stream: the reader gets the rest, then EOF.
func (p *lossyPipe) Close() error {
	p.mu.Lock()
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
	return nil
}

// Abort stops the reader at once.
func (p *lossyPipe) Abort() {
	p.mu.Lock()
	p.broken = true
	p.buf = nil
	p.cond.Broadcast()
	p.mu.Unlock()
}

// teeReader forwards reads of r and copies them into w (a lossyPipe).
type teeReader struct {
	r io.Reader
	w io.Writer
}

func (t teeReader) Read(b []byte) (int, error) {
	n, err := t.r.Read(b)
	if n > 0 {
		t.w.Write(b[:n])
	}
	return n, err
}
