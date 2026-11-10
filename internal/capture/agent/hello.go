package agent

import (
	"bufio"
	"encoding/binary"
	"errors"
)

// clientHello is what the agent needs from a TLS ClientHello: the
// decision inputs and the raw records, replayed byte for byte when the
// connection is passed through after all (docs/capture.md §5.2).
type clientHello struct {
	rawLen int // bytes of the records holding the ClientHello (peeked, not consumed)
	random []byte
	sni    string
	alpn   []string
	ech    bool // carries encrypted_client_hello (0xfe0d): real or GREASE
}

const (
	extSNI  = 0x0000
	extALPN = 0x0010
	extECH  = 0xfe0d

	maxHello = 32 << 10 // larger ClientHellos are passed through unparsed
)

var errNotHello = errors.New("not a TLS ClientHello")

// peekHello reads, without consuming, the TLS records that carry the
// ClientHello from br (whose buffer must hold maxHello+5 bytes).
func peekHello(br *bufio.Reader) (*clientHello, error) {
	var msg []byte
	off := 0
	for {
		hdr, err := br.Peek(off + 5)
		if err != nil {
			return nil, err
		}
		h := hdr[off:]
		if h[0] != 0x16 || h[1] != 3 {
			return nil, errNotHello
		}
		n := int(binary.BigEndian.Uint16(h[3:5]))
		if n == 0 || n > 16384+256 {
			return nil, errNotHello
		}
		if off+5+n > maxHello {
			return nil, errNotHello
		}
		rec, err := br.Peek(off + 5 + n)
		if err != nil {
			return nil, err
		}
		msg = append(msg, rec[off+5:]...)
		off += 5 + n
		if len(msg) >= 4 {
			if msg[0] != 1 {
				return nil, errNotHello
			}
			want := 4 + (int(msg[1])<<16 | int(msg[2])<<8 | int(msg[3]))
			if want > maxHello {
				return nil, errNotHello
			}
			if len(msg) >= want {
				ch, err := parseHello(msg[4:want])
				if err != nil {
					return nil, err
				}
				ch.rawLen = off
				return ch, nil
			}
		}
	}
}

// parseHello parses a ClientHello message body (after the 4-byte
// handshake header).
func parseHello(b []byte) (*clientHello, error) {
	r := reader(b)
	ch := &clientHello{}
	if _, ok := r.take(2); !ok { // legacy_version
		return nil, errNotHello
	}
	rnd, ok := r.take(32)
	if !ok {
		return nil, errNotHello
	}
	ch.random = append([]byte(nil), rnd...)
	if _, ok := r.vec8(); !ok { // session id
		return nil, errNotHello
	}
	if _, ok := r.vec16(); !ok { // cipher suites
		return nil, errNotHello
	}
	if _, ok := r.vec8(); !ok { // compression
		return nil, errNotHello
	}
	if len(r) == 0 {
		return ch, nil // no extensions
	}
	exts, ok := r.vec16()
	if !ok {
		return nil, errNotHello
	}
	for len(exts) > 0 {
		typ, ok1 := exts.u16()
		body, ok2 := exts.vec16()
		if !ok1 || !ok2 {
			return nil, errNotHello
		}
		switch typ {
		case extSNI:
			list, ok := body.vec16()
			if !ok {
				return nil, errNotHello
			}
			for len(list) > 0 {
				nt, ok1 := list.take(1)
				name, ok2 := list.vec16()
				if !ok1 || !ok2 {
					return nil, errNotHello
				}
				if nt[0] == 0 && ch.sni == "" {
					ch.sni = string(name)
				}
			}
		case extALPN:
			list, ok := body.vec16()
			if !ok {
				return nil, errNotHello
			}
			for len(list) > 0 {
				p, ok := list.vec8()
				if !ok {
					return nil, errNotHello
				}
				ch.alpn = append(ch.alpn, string(p))
			}
		case extECH:
			ch.ech = true
		}
	}
	return ch, nil
}

type reader []byte

func (r *reader) take(n int) (reader, bool) {
	if len(*r) < n {
		return nil, false
	}
	v := (*r)[:n]
	*r = (*r)[n:]
	return v, true
}

func (r *reader) u16() (int, bool) {
	v, ok := r.take(2)
	if !ok {
		return 0, false
	}
	return int(binary.BigEndian.Uint16(v)), true
}

func (r *reader) vec8() (reader, bool) {
	n, ok := r.take(1)
	if !ok {
		return nil, false
	}
	return r.take(int(n[0]))
}

func (r *reader) vec16() (reader, bool) {
	n, ok := r.u16()
	if !ok {
		return nil, false
	}
	return r.take(n)
}
